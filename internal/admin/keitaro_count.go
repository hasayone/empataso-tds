package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

const (
	// keitaroCountFanout is how many one-row probes run in parallel in one search round.
	// The tracker answers requests one after another, so a wide round only adds waiting.
	keitaroCountFanout = 8
	// keitaroCountCeiling is the largest campaign count the search will look for.
	keitaroCountCeiling = 1_000_000
	keitaroCampaignsURL = "/admin_api/v1/campaigns"
)

// rowProber reports how many rows the tracker returns for a page of one row at offset.
// A failure means the tracker did not answer with a JSON array.
type rowProber func(ctx context.Context, offset int) (int, error)

// keitaroCountHint remembers the last campaign count found for one tracker origin.
// The next check starts from it and needs two requests when the count did not change.
type keitaroCountHint struct {
	mu   sync.Mutex
	base string
	n    int
}

// get returns the remembered count for base, or 0 when none is stored for that origin.
func (h *keitaroCountHint) get(base string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.base != base {
		return 0
	}
	return h.n
}

// set stores n as the count for base.
func (h *keitaroCountHint) set(base string, n int) {
	h.mu.Lock()
	h.base, h.n = base, n
	h.mu.Unlock()
}

// keitaroCountCampaigns checks the key and counts campaigns without downloading the list.
// get is a tracker GET that returns the body of a path with its query. hint is the
// last count for this tracker, or 0.
// The first page of one row proves the address, the key and the JSON array answer.
// The count is then found by probing one-row pages and narrowing the range between an
// offset that holds a campaign and the first empty one. A tracker that ignores limit
// has already sent the whole list in the first answer, and its length is the count.
// It returns the number of campaigns, or an error from the tracker or a body that is not an array.
func keitaroCountCampaigns(ctx context.Context, get func(ctx context.Context, path string) ([]byte, error), hint int) (int, error) {
	probe := func(ctx context.Context, offset int) (int, error) {
		body, err := get(ctx, fmt.Sprintf("%s?limit=1&offset=%d", keitaroCampaignsURL, offset))
		if err != nil {
			return 0, err
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(body, &rows); err != nil {
			return 0, errors.New("keitaro: campaigns не массив")
		}
		return len(rows), nil
	}
	return countByOffset(ctx, probe, hint)
}

// countByOffset finds how many rows exist using only one-row pages.
// probe returns the rows found at an offset. A first answer longer than one row
// means the tracker ignores limit, and that length is returned as the count.
// hint is a count found earlier, or 0; when it is still right, two more probes confirm it.
// It returns the count, or the first probe error. It fails when the count exceeds keitaroCountCeiling.
func countByOffset(ctx context.Context, probe rowProber, hint int) (int, error) {
	first, err := probe(ctx, 0)
	if err != nil || first != 1 {
		return first, err
	}
	// lo is an offset known to hold a row. hi is an offset known to be empty, or 0 while unknown.
	lo, hi := 0, 0
	if hint >= 2 && hint < keitaroCountCeiling {
		present, err := probeAll(ctx, probe, []int{hint - 1, hint})
		if err != nil {
			return 0, err
		}
		switch {
		case present[0] && !present[1]:
			return hint, nil
		case present[1]:
			lo = hint
		default:
			hi = hint - 1
		}
	}
	for hi == 0 || hi-lo > 1 {
		offsets := nextOffsets(lo, hi)
		if len(offsets) == 0 {
			return 0, errors.New("keitaro: too many campaigns to count")
		}
		present, err := probeAll(ctx, probe, offsets)
		if err != nil {
			return 0, err
		}
		for i, off := range offsets {
			if present[i] {
				lo = off
				continue
			}
			hi = off
			break
		}
	}
	return hi, nil
}

// nextOffsets picks the offsets for one search round.
// lo holds a row. hi is the first empty offset, or 0 when none is known yet; then the
// offsets are lo plus 10, 100, 1000 and so on up to the ceiling. Otherwise they split
// (lo, hi) into equal steps.
// It returns the ascending offsets, or nothing when lo already reached the ceiling.
func nextOffsets(lo, hi int) []int {
	if hi == 0 {
		var out []int
		for step := 10; lo < keitaroCountCeiling; step *= 10 {
			off := min(lo+step, keitaroCountCeiling)
			out = append(out, off)
			if off == keitaroCountCeiling {
				break
			}
		}
		return out
	}
	step := (hi - lo + keitaroCountFanout) / (keitaroCountFanout + 1)
	if step < 1 {
		step = 1
	}
	var out []int
	for off := lo + step; off < hi && len(out) < keitaroCountFanout; off += step {
		out = append(out, off)
	}
	return out
}

// probeAll runs probe for every offset in parallel.
// It returns, per offset in order, whether the page holds a row.
// The first error cancels the other probes and is returned.
func probeAll(ctx context.Context, probe rowProber, offsets []int) ([]bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	present := make([]bool, len(offsets))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	for i, off := range offsets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := probe(ctx, off)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = err
					cancel()
				}
				return
			}
			present[i] = n > 0
		}()
	}
	wg.Wait()
	return present, first
}
