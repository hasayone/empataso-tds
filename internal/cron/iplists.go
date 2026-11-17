package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hasayone/empataso-tds/internal/atomicfile"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/ipindex"
)

const (
	// maxListBytes caps the response of an IP list source.
	maxListBytes = 32 << 20
	// maxPrivateRelayBytes caps the iCloud Private Relay file, which is a CSV
	// with hundreds of thousands of rows.
	maxPrivateRelayBytes = 64 << 20
)

// isCSVSource reports whether the response of src is CSV: its URL path ends in
// .csv or it feeds the Private Relay list. Only then does a comma end the
// address on a line.
func isCSVSource(src IPSource) bool {
	if src.Target == detect.ListPrivateRelay {
		return true
	}
	u, err := url.Parse(cleanURL(src.URL))
	return err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".csv")
}

// listLimit returns the size cap of the response of src.
func listLimit(src IPSource) int64 {
	if src.Target == detect.ListPrivateRelay {
		return maxPrivateRelayBytes
	}
	return maxListBytes
}

// runIPLists refreshes every source in cfg into the data directory.
// ctx cancels the fetches. The string is one "name: count" per list file,
// sorted by name and joined with ", ". count is how many entries the file
// holds after the last source that wrote it; a later source replaces the
// count from an earlier one. A source that fails is skipped, its error is
// joined into the returned error, and later sources still run. No sources
// returns "no sources configured" and a nil error.
func (r *Runner) runIPLists(ctx context.Context, cfg IPLists) (string, error) {
	if len(cfg.Sources) == 0 {
		return "no sources configured", nil
	}
	counts := map[string]int{}
	var errs []error
	for _, src := range cfg.Sources {
		got, err := r.updateList(ctx, src)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for name, n := range got {
			counts[name] = n
		}
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s: %d", name, counts[name])
	}
	return strings.Join(parts, ", "), errors.Join(errs...)
}

// updateList fetches src and writes the lists that response describes.
// ctx cancels the fetch. The map is the list name to the number of entries
// in the file after this write; merge mode counts lines already stored as
// well. A read, parse or write failure is returned as an error. An empty
// address set leaves that file unchanged and is also an error, as is a
// sectioned body with no named sections. Names written before the failure
// stay in the map.
func (r *Runner) updateList(ctx context.Context, src IPSource) (map[string]int, error) {
	body, err := r.get(ctx, src.URL, nil, "", "", listLimit(src))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		return nil, err
	}

	csv := isCSVSource(src)
	lists := map[string][]string{}
	if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '{' {
		if src.Target == "" {
			return nil, fmt.Errorf("%s: a JSON source needs a target list", hostOf(src.URL))
		}
		lines, err := prefixesFromJSON(t)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", hostOf(src.URL), err)
		}
		lists[src.Target] = lines
	} else if src.Target != "" {
		lists[src.Target] = cleanLines(string(raw), csv)
	} else {
		lists = splitSections(string(raw), csv)
	}

	out := map[string]int{}
	for name, lines := range lists {
		n := countEntries(lines)
		// An upstream that answers 200 with an error page or an empty file
		// must not wipe a working list.
		if n == 0 {
			return out, fmt.Errorf("%s: no addresses for %s, list left as it was", hostOf(src.URL), name)
		}
		path := filepath.Join(r.paths.DataDir, name+".dat")
		if src.Mode == "merge" {
			lines = mergeLines(path, lines)
			n = countEntries(lines)
		}
		if err := atomicfile.Write(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			return out, err
		}
		out[name] = n
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no \"# name\" sections found", hostOf(src.URL))
	}
	return out, nil
}

// prefixesFromJSON reads the crawler range document
// {"prefixes":[{"ipv4Prefix":"66.249.64.0/27"},{"ipv6Prefix":…}]}.
// Google, Bing, Apple, OpenAI, Anthropic, Perplexity, DuckDuckGo,
// Common Crawl and Ahrefs publish this shape.
func prefixesFromJSON(b []byte) ([]string, error) {
	var doc struct {
		Prefixes []struct {
			V4 string `json:"ipv4Prefix"`
			V6 string `json:"ipv6Prefix"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, errors.New("not a prefixes JSON document")
	}
	var out []string
	for _, p := range doc.Prefixes {
		if p.V4 != "" {
			out = append(out, p.V4)
		}
		if p.V6 != "" {
			out = append(out, p.V6)
		}
	}
	return out, nil
}

// cleanLines keeps addresses and "# label" lines, drops everything else.
// With csv set, a row such as "172.224.226.0/27,GB,GB-EN,London," is kept as
// its first field, the address or CIDR; without it such a line is dropped.
func cleanLines(s string, csv bool) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			out = append(out, line)
		} else if entry, ok := entryOf(line, csv); ok {
			out = append(out, entry)
		}
	}
	return out
}

// entryOf returns the address, CIDR or range a list line stands for. A line
// with csv set is a CSV row and only its first field counts. ok is false when
// the line is not a single valid entry.
func entryOf(line string, csv bool) (entry string, ok bool) {
	if i := strings.IndexByte(line, ','); csv && i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line, isEntry(line)
}

// splitSections turns "# google\n1.2.3.0/24\n# yandex\n…" into one list per
// section, named ip_<section>. Lines before the first section, and sections
// whose name is not a plain word, are dropped. csv is as in cleanLines.
func splitSections(s string, csv bool) map[string][]string {
	out := map[string][]string{}
	cur := ""
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "#")))
			name = strings.TrimPrefix(name, "ip_")
			cur = ""
			if listNameRe.MatchString(name) {
				cur = "ip_" + name
			}
			continue
		}
		if entry, ok := entryOf(line, csv); cur != "" && ok {
			out[cur] = append(out[cur], entry)
		}
	}
	return out
}

// isEntry reports whether line is exactly one address, CIDR or range.
// Plain addresses and CIDRs are checked without the list parser, which
// allocates a buffer per call: the iCloud Private Relay file has hundreds of
// thousands of rows.
func isEntry(line string) bool {
	if _, err := netip.ParsePrefix(line); err == nil {
		return true
	}
	if _, err := netip.ParseAddr(line); err == nil {
		return true
	}
	idx, err := ipindex.Parse(strings.NewReader(line))
	return err == nil && idx.Len() == 1
}

func countEntries(lines []string) int {
	n := 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "#") {
			n++
		}
	}
	return n
}

// mergeLines appends the lines the file does not have yet, keeping what is
// there — including entries added by hand or by the engine's save_ip.
func mergeLines(path string, fresh []string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return fresh
	}
	have := cleanLines(string(b), false)
	seen := make(map[string]bool, len(have))
	for _, l := range have {
		seen[l] = true
	}
	for _, l := range fresh {
		if !seen[l] && !strings.HasPrefix(l, "#") {
			seen[l] = true
			have = append(have, l)
		}
	}
	return have
}
