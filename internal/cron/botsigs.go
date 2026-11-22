package cron

import (
	"context"
	"encoding/json"
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

// botSourceDir is the data-directory folder of one file per signature source.
const botSourceDir = "bot_sources"

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
// Each source also has its own file under bot_sources. That file is created
// and filled only when this run parsed at least one rule from the source. A
// source that fails keeps that file byte for byte and the rules already in it
// (or, when the file is not there yet, the rules the merged database still
// has for that name). An empty download is not a success and does not create
// the file.
//
// A source that fails to download, is not a signature list or yields no valid
// rule keeps the rules it had, and its error is joined into the returned
// error; the other sources still run. The run is a success only when the
// merged file was written and the engine can load rules from it. When no
// enabled source has any rule, fresh or kept, the merged file is left as it
// is and an error is returned. Disabled and removed sources leave the
// database. The message is "rules: N" with the count per source.
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
			rules = keptBotRules(r.paths.DataDir, src.Name, prevRules[src.Name])
			st.Rejected = prevSrc[src.Name].Rejected
		} else if err = writeBotSourceFile(r.paths.DataDir, src.Name, rules); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Name, err))
			st.Error = err.Error()
			rules = keptBotRules(r.paths.DataDir, src.Name, prevRules[src.Name])
			st.Rejected = prevSrc[src.Name].Rejected
		} else {
			rules = detect.MergeBotRules(rules)
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
	if err == nil && enabled > 0 {
		db, _, lerr := detect.LoadBotDB(b)
		if lerr != nil {
			err = lerr
		} else if db == nil || db.Len()+db.Weak() == 0 {
			err = errors.New("no signature the engine keeps, database left as it was")
		}
	}
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

// botSourcePath is the file of one signature source. name is the source name,
// a single path element. It returns "" when name could leave botSourceDir.
func botSourcePath(dataDir, name string) string {
	if name == "" || name != filepath.Base(name) || strings.Contains(name, "..") {
		return ""
	}
	return filepath.Join(dataDir, botSourceDir, name+".json")
}

// botSourceDoc is the content of one source file: the rules that source last
// contributed, after validation and deduplication.
type botSourceDoc struct {
	Rules []detect.BotRuleRecord `json:"rules"`
}

// writeBotSourceFile creates the source file and fills it with rules.
// dataDir is the data directory, name the source name, rules the accepted
// rules of this download. Duplicates are collapsed. It returns an error when
// there is nothing to store, the name is unsafe, or the write fails. An empty
// rules slice does not create a file.
func writeBotSourceFile(dataDir, name string, rules []detect.BotRuleRecord) error {
	rules = detect.MergeBotRules(rules)
	if len(rules) == 0 {
		return errors.New("no signatures to store")
	}
	path := botSourcePath(dataDir, name)
	if path == "" {
		return errors.New("bad source name")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(botSourceDoc{Rules: rules})
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o644)
}

// readBotSourceRules reads the rules stored for name. A missing, empty or
// unreadable file returns nil. It does not fail.
func readBotSourceRules(dataDir, name string) []detect.BotRuleRecord {
	path := botSourcePath(dataDir, name)
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return nil
	}
	var doc botSourceDoc
	if json.Unmarshal(b, &doc) != nil || len(doc.Rules) == 0 {
		return nil
	}
	return doc.Rules
}

// keptBotRules returns the rules a failed source already had. The source file
// wins; prev is the rules of that source still in the merged database, used
// when the source has no file yet. An empty file is not a previous success.
// It does not fail.
func keptBotRules(dataDir, name string, prev []detect.BotRuleRecord) []detect.BotRuleRecord {
	if rules := readBotSourceRules(dataDir, name); len(rules) > 0 {
		return rules
	}
	return prev
}
