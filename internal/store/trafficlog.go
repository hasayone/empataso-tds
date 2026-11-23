package store

import (
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"io"
	"time"

	"github.com/redis/go-redis/v9"
)

// Traffic log defaults and ceilings.
const (
	// DefaultTrafficLogMax is how many of the latest clicks the log of one campaign keeps.
	DefaultTrafficLogMax = 500
	// MaxTrafficLogMax is the largest value EMTDS_TRAFFIC_LOG_MAX may set.
	MaxTrafficLogMax = 5000
	// DefaultTrafficLogTTL is how long an idle campaign log lives after its last click.
	DefaultTrafficLogTTL = 7 * 24 * time.Hour
	// DefaultTrafficLogClickTTL is how long the path of one click can be looked up by its id.
	DefaultTrafficLogClickTTL = 72 * time.Hour
	// trafficLogSteps is how many paths one click id keeps: the landing step and the offer step.
	trafficLogSteps = 4
	// trafficLogMaxEntry caps the size of one entry after it is unpacked.
	trafficLogMaxEntry = 256 << 10
)

// TrafficLogLimits bounds what the traffic log keeps in Redis. Zero PerCampaign
// switches the log off.
type TrafficLogLimits struct {
	PerCampaign int           // entries kept per campaign, newest first
	CampaignTTL time.Duration // lifetime of a campaign log since its last click
	ClickTTL    time.Duration // lifetime of the path stored under one click id
}

// DefaultTrafficLogLimits returns the limits used when nothing is configured.
func DefaultTrafficLogLimits() TrafficLogLimits {
	return TrafficLogLimits{PerCampaign: DefaultTrafficLogMax, CampaignTTL: DefaultTrafficLogTTL, ClickTTL: DefaultTrafficLogClickTTL}
}

// TrafficLogLimitsFromEnv reads the limits from EMTDS_TRAFFIC_LOG_MAX (0 turns
// the log off, at most MaxTrafficLogMax), EMTDS_TRAFFIC_LOG_TTL and
// EMTDS_TRAFFIC_LOG_CLICK_TTL (Go durations such as 72h). A missing or
// malformed value keeps the default. It does not fail.
func TrafficLogLimitsFromEnv() TrafficLogLimits {
	l := DefaultTrafficLogLimits()
	l.PerCampaign = min(envNonNegative("EMTDS_TRAFFIC_LOG_MAX", l.PerCampaign), MaxTrafficLogMax)
	l.CampaignTTL = envDuration("EMTDS_TRAFFIC_LOG_TTL", l.CampaignTTL)
	l.ClickTTL = envDuration("EMTDS_TRAFFIC_LOG_CLICK_TTL", l.ClickTTL)
	return l
}

// TrafficLog keeps the click paths in Redis, so the engine that writes them and
// the admin that reads them share one copy.
//
// Two keys carry them. "tl:g:<campaign>" is a list of the latest clicks of the
// campaign, newest first, cut to PerCampaign. "tl:c:<click id>" holds the paths of
// one click. Both expire. An entry is JSON packed with deflate; the store does
// not look inside it.
type TrafficLog struct {
	rdb    redis.UniversalClient
	limits TrafficLogLimits
}

// NewTrafficLog returns a traffic log on rdb under limits. Values the limits
// leave unset take their defaults, except PerCampaign, where zero is "off".
func NewTrafficLog(rdb redis.UniversalClient, limits TrafficLogLimits) *TrafficLog {
	if limits.CampaignTTL <= 0 {
		limits.CampaignTTL = DefaultTrafficLogTTL
	}
	if limits.ClickTTL <= 0 {
		limits.ClickTTL = DefaultTrafficLogClickTTL
	}
	if limits.PerCampaign > MaxTrafficLogMax {
		limits.PerCampaign = MaxTrafficLogMax
	}
	return &TrafficLog{rdb: rdb, limits: limits}
}

// Limits returns the limits the log works under. A nil log has none.
func (t *TrafficLog) Limits() TrafficLogLimits {
	if t == nil {
		return TrafficLogLimits{}
	}
	return t.limits
}

// Enabled reports whether the log keeps anything. A nil log keeps nothing.
func (t *TrafficLog) Enabled() bool { return t != nil && t.limits.PerCampaign > 0 }

// trafficGroupKey is the key of the latest-clicks list of a campaign.
func trafficGroupKey(group string) string { return "tl:g:" + group }

// trafficClickKey is the key of the paths of one click id.
func trafficClickKey(cid string) string { return "tl:c:" + cid }

// Push stores one entry.
//
// ctx bounds the Redis calls. groups are the campaign ids the entry belongs to,
// each once; an empty id is skipped. cid is the click id, empty to skip the
// per-click copy. entry is the JSON of the path. Every campaign list drops its
// oldest entries beyond PerCampaign. It returns an error when the log is off,
// no campaign is named, the entry cannot be packed or Redis fails; the caller
// counts the loss and goes on.
func (t *TrafficLog) Push(ctx context.Context, groups []string, cid string, entry []byte) error {
	if !t.Enabled() {
		return errors.New("traffic log: off")
	}
	packed, err := packEntry(entry)
	if err != nil {
		return err
	}
	pipe := t.rdb.Pipeline()
	named := 0
	for _, g := range groups {
		if g == "" {
			continue
		}
		named++
		gk := trafficGroupKey(g)
		pipe.LPush(ctx, gk, packed)
		pipe.LTrim(ctx, gk, 0, int64(t.limits.PerCampaign-1))
		pipe.Expire(ctx, gk, t.limits.CampaignTTL)
	}
	if named == 0 {
		return errors.New("traffic log: no campaign")
	}
	if cid != "" {
		ck := trafficClickKey(cid)
		pipe.RPush(ctx, ck, packed)
		pipe.LTrim(ctx, ck, -trafficLogSteps, -1)
		pipe.Expire(ctx, ck, t.limits.ClickTTL)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// Recent returns up to n of the latest entries of a campaign, newest first,
// unpacked. n is clamped to what the log keeps. An entry that cannot be
// unpacked is skipped. A campaign without a log returns nothing and no error.
func (t *TrafficLog) Recent(ctx context.Context, group string, n int) ([][]byte, error) {
	if !t.Enabled() || group == "" || n <= 0 {
		return nil, nil
	}
	n = min(n, t.limits.PerCampaign)
	return t.read(ctx, trafficGroupKey(group), 0, int64(n-1))
}

// Click returns the stored paths of one click id in the order they were taken,
// unpacked. An unknown or expired id returns nothing and no error.
func (t *TrafficLog) Click(ctx context.Context, cid string) ([][]byte, error) {
	if t == nil || cid == "" {
		return nil, nil
	}
	return t.read(ctx, trafficClickKey(cid), 0, -1)
}

// read fetches a list range and unpacks each element.
func (t *TrafficLog) read(ctx context.Context, key string, from, to int64) ([][]byte, error) {
	raw, err := t.rdb.LRange(ctx, key, from, to).Result()
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(raw))
	for _, r := range raw {
		if b, err := unpackEntry([]byte(r)); err == nil {
			out = append(out, b)
		}
	}
	return out, nil
}

// packEntry deflates one entry.
func packEntry(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// unpackEntry inflates one entry, refusing one that unpacks beyond trafficLogMaxEntry.
func unpackEntry(b []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(b))
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, trafficLogMaxEntry+1))
	if err != nil {
		return nil, err
	}
	if len(out) > trafficLogMaxEntry {
		return nil, errors.New("traffic log: entry too large")
	}
	return out, nil
}