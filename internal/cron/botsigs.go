package cron

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/atomicfile"
	"github.com/hasayone/empataso-tds/internal/detect"
)

// maxBotSourceBytes caps the response of one bot signature source.
const maxBotSourceBytes = 8 << 20

// BotSigState is what the Automation page shows about the bot signature
// database: the rules in the file the engine reads, when that file was last
// written and the state of every source at the last run.
type BotSigState struct {
	Rules   int                  `json:"rules"`
	Updated time.Time            `json:"updated,omitzero"`
	Sources []detect.BotDBSource `json:"sources"`
}

// runBotSignatures downloads every enabled source of cfg, validates and merges
// the rules and writes detect.FileBotSignatures into the data directory, where
// the engine picks it up without a restart. ctx cancels the downloads.
//
// A source that fails to download, is not a signature list or yields no valid
// rule keeps the rules it had in the previous file, and its error is joined
// into the returned error; the other sources still run. When no enabled source
// has any rule, fresh or kept, the file is left as it is and an error is
// returned. Disabled and removed sources leave the database. The message is
// "rules: N" with the count per source.
func (r *Runner) runBotSignatures(ctx context.Context, cfg BotSignatures) (string, error) {
	path := filepath.Join(r.paths.DataDir, detect.FileBotSignatures)
	prev := readBotDBFile(path)
	prevSrc := map[string]detect.BotDBSource{}
	for _, s := range prev.Sources {
		prevSrc[s.Name] = s
	}
	prevRules := map[string][]detect.BotRuleRecord{}
	for _, rule := range prev.Rules {
		prevRules[rule.Source] = append(prevRules[rule.Source], rule)
	}

	var errs []error
	var lists [][]detect.BotRuleRecord
	var sources []detect.BotDBSource
	enabled := 0
	for _, src := range cfg.Sources {
		if !src.Enabled {
			continue
		}
		enabled++
		st := detect.BotDBSource{Name: src.Name, URL: cleanURL(src.URL), OK: prevSrc[src.Name].OK}
		rules, rejected, err := r.fetchBotSource(ctx, src)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Name, err))
			st.Error = err.Error()
			rules = prevRules[src.Name]
			st.Rejected = prevSrc[src.Name].Rejected
		} else {
			st.OK = r.now().UTC()
			st.Rejected = rejected
		}
		st.Rules = len(rules)
		lists = append(lists, rules)
		sources = append(sources, st)
	}
	merged := detect.MergeBotRules(lists...)
	if enabled > 0 && len(merged) == 0 {
		r.saveBotSigState(path, sources)
		errs = append(errs, errors.New("no valid rules, database left as it was"))
		return "", errors.Join(errs...)
	}
	file := detect.BotDBFile{Updated: r.now().UTC(), Sources: sources, Rules: merged}
	b, err := detect.EncodeBotDBFile(file)
	if err == nil {
		err = atomicfile.Write(path, b, 0o644)
	}
	if err != nil {
		r.saveBotSigState(path, sources)
		return "", errors.Join(append(errs, err)...)
	}
	r.state(func(s *Status) {
		s.BotSigs = &BotSigState{Rules: len(merged), Updated: file.Updated, Sources: sources}
	})
	if enabled == 0 {
		return "no sources enabled, built-in signatures only", errors.Join(errs...)
	}
	parts := make([]string, len(sources))
	for i, s := range sources {
		parts[i] = fmt.Sprintf("%s %d", s.Name, s.Rules)
	}
	return fmt.Sprintf("rules: %d (%s)", len(merged), strings.Join(parts, ", ")), errors.Join(errs...)
}

// fetchBotSource downloads src and returns its valid rules tagged with its name
// and the number of rejected ones. A download failure, content in no known
// format and a list without one valid rule are errors.
func (r *Runner) fetchBotSource(ctx context.Context, src BotSource) ([]detect.BotRuleRecord, int, error) {
	body, err := r.get(ctx, src.URL, nil, "", "", maxBotSourceBytes)
	if err != nil {
		return nil, 0, err
	}
	raw, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		return nil, 0, err
	}
	cands, rejected, err := detect.ParseBotSource(raw)
	if err != nil {
		return nil, 0, err
	}
	if len(cands) == 0 {
		return nil, rejected, fmt.Errorf("no valid rules (%d rejected)", rejected)
	}
	out := make([]detect.BotRuleRecord, len(cands))
	for i, c := range cands {
		out[i] = detect.BotRuleRecord{Source: src.Name, Pattern: c.Pattern, Name: c.Name}
	}
	return out, rejected, nil
}

// saveBotSigState records the source states of a run that did not write the
// file, with the rule count and time of the file still in place.
func (r *Runner) saveBotSigState(path string, sources []detect.BotDBSource) {
	prev := readBotDBFile(path)
	r.state(func(s *Status) {
		s.BotSigs = &BotSigState{Rules: len(prev.Rules), Updated: prev.Updated, Sources: sources}
	})
}

// readBotDBFile reads the database at path. A missing, unreadable or invalid
// file gives an empty one.
func readBotDBFile(path string) detect.BotDBFile {
	b, err := os.ReadFile(path)
	if err != nil {
		return detect.BotDBFile{}
	}
	f, err := detect.DecodeBotDBFile(b)
	if err != nil {
		return detect.BotDBFile{}
	}
	return f
}
