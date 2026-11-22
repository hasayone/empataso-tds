package cron

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hasayone/empataso-tds/internal/detect"
)

// readBotFile reads the bot signature database the runner wrote.
func (e *env) readBotFile() detect.BotDBFile {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, "data", detect.FileBotSignatures))
	if err != nil {
		e.t.Fatalf("database not written: %v", err)
	}
	f, err := detect.DecodeBotDBFile(b)
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

// TestBotSignaturesMergeAndDedupe: two sources with a common rule give one
// rule each; a disabled source does not run; the state counts the rules.
func TestBotSignaturesMergeAndDedupe(t *testing.T) {
	e := newEnv(t)
	a := e.serve("/a.txt", "Zoombot/\nAlphaCrawler-x\n(?<!x)broken\n")
	b := e.serve("/b.json", `["zoombot/", "BetaSpider-y"]`)
	off := e.serve("/off.txt", "OffCrawler-z\n")
	msg, err := e.r.runBotSignatures(context.Background(), BotSignatures{Sources: []BotSource{
		{Name: "a", URL: a, Enabled: true}, {Name: "b", URL: b, Enabled: true}, {Name: "off", URL: off},
	}})
	if err != nil {
		t.Fatal(err)
	}
	f := e.readBotFile()
	if len(f.Rules) != 3 || f.Rules[0].Source != "a" || f.Rules[2].Pattern != "(?i)BetaSpider-y" {
		t.Fatalf("rules %+v", f.Rules)
	}
	if len(f.Sources) != 2 || f.Sources[0].Rejected != 1 || f.Sources[1].Rules != 2 || f.Sources[0].OK.IsZero() {
		t.Fatalf("sources %+v", f.Sources)
	}
	if msg != "rules: 3 (a 2, b 2)" {
		t.Fatalf("message %q", msg)
	}
	var st *BotSigState
	e.r.state(func(s *Status) { st = s.BotSigs })
	if st == nil || st.Rules != 3 || st.Updated.IsZero() {
		t.Fatalf("state %+v", st)
	}
}

// TestBotSignaturesFailureKeepsWorkingDatabase: a source that fails keeps its
// previous rules, and when every source fails the file is not touched.
func TestBotSignaturesFailureKeepsWorkingDatabase(t *testing.T) {
	e := newEnv(t)
	var broken atomic.Bool
	e.mux.HandleFunc("/flaky.txt", func(w http.ResponseWriter, r *http.Request) {
		if broken.Load() {
			_, _ = w.Write([]byte("<html>rate limited</html>"))
			return
		}
		_, _ = w.Write([]byte("Zoombot/\nAlphaCrawler-x\n"))
	})
	good := e.serve("/good.txt", "BetaSpider-y\n")
	cfg := BotSignatures{Sources: []BotSource{
		{Name: "flaky", URL: e.srv.URL + "/flaky.txt", Enabled: true},
		{Name: "good", URL: good, Enabled: true},
	}}
	if _, err := e.r.runBotSignatures(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	broken.Store(true)
	_, err := e.r.runBotSignatures(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "flaky") {
		t.Fatalf("err = %v, want the flaky source named", err)
	}
	f := e.readBotFile()
	if len(f.Rules) != 3 || f.Sources[0].Error == "" || f.Sources[0].Rules != 2 || f.Sources[0].OK.IsZero() {
		t.Fatalf("the failed source lost its rules: %+v / %+v", f.Rules, f.Sources)
	}

	path := filepath.Join(e.dir, "data", detect.FileBotSignatures)
	before, _ := os.ReadFile(path)
	only := BotSignatures{Sources: []BotSource{{Name: "new", URL: e.srv.URL + "/gone", Enabled: true}}}
	if _, err := e.r.runBotSignatures(context.Background(), only); err == nil || !strings.Contains(err.Error(), "left as it was") {
		t.Fatalf("err = %v, want the database kept", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("a run without any valid rule replaced the database")
	}
	var st *BotSigState
	e.r.state(func(s *Status) { st = s.BotSigs })
	if st == nil || st.Rules != 3 || len(st.Sources) != 1 || st.Sources[0].Error == "" {
		t.Fatalf("state %+v", st)
	}
}

// TestBotSignaturesRunNow: "run now" refreshes a disabled job, and a later
// manual run against a failing source leaves the database as it was.
func TestBotSignaturesRunNow(t *testing.T) {
	e := newEnv(t)
	var down atomic.Bool
	e.mux.HandleFunc("/sig.txt", func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("Zoombot/\n"))
	})
	cfg := Default()
	cfg.BotSignatures = BotSignatures{EveryMinutes: 1440, Sources: []BotSource{{Name: "s", URL: e.srv.URL + "/sig.txt", Enabled: true}}}
	if err := Save(e.r.paths.Config, cfg); err != nil {
		t.Fatal(err)
	}
	run := func() JobStatus {
		t.Helper()
		if err := RequestRun(e.r.paths.Config, JobBotSignatures); err != nil {
			t.Fatal(err)
		}
		e.r.Tick(context.Background())
		e.r.Wait()
		st, err := ReadStatus(StatusPath(e.r.paths.Config))
		if err != nil {
			t.Fatal(err)
		}
		return st.Jobs[JobBotSignatures]
	}
	if js := run(); !js.OK || len(e.readBotFile().Rules) != 1 {
		t.Fatalf("manual run: %+v", js)
	}
	down.Store(true)
	js := run()
	if js.OK || !strings.Contains(js.Error, "HTTP 502") {
		t.Fatalf("failed run reported %+v", js)
	}
	if f := e.readBotFile(); len(f.Rules) != 1 || f.Rules[0].Pattern != "(?i)Zoombot/" {
		t.Fatalf("the working database was replaced: %+v", f.Rules)
	}
}

// TestBotSourceOwnFile: a source gets its own file only after a download parses,
// that file holds the rules, and a later failure leaves the file as it was and
// is not a success. An empty answer creates no file and is not a success either.
func TestBotSourceOwnFile(t *testing.T) {
	e := newEnv(t)
	var mode atomic.Int32
	e.mux.HandleFunc("/own.txt", func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 0:
			return
		case 2:
			http.Error(w, "down", http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte("Zoombot/\n(?<!x)broken\nZoombot/\n"))
		}
	})
	cfg := Default()
	cfg.BotSignatures = BotSignatures{EveryMinutes: 1440, Sources: []BotSource{
		{Name: "added", URL: e.srv.URL + "/own.txt", Enabled: true},
	}}
	if err := Save(e.r.paths.Config, cfg); err != nil {
		t.Fatal(err)
	}
	path := botSourcePath(e.r.paths.DataDir, "added")
	run := func() JobStatus {
		t.Helper()
		if err := RequestRun(e.r.paths.Config, JobBotSignatures); err != nil {
			t.Fatal(err)
		}
		e.r.Tick(context.Background())
		e.r.Wait()
		st, err := ReadStatus(StatusPath(e.r.paths.Config))
		if err != nil {
			t.Fatal(err)
		}
		return st.Jobs[JobBotSignatures]
	}
	if js := run(); js.OK || js.Error == "" || js.LastRun.IsZero() {
		t.Fatalf("empty download must not succeed: %+v", js)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an empty download created a source file")
	}
	if _, err := os.Stat(filepath.Join(e.dir, "data", detect.FileBotSignatures)); !os.IsNotExist(err) {
		t.Fatal("an empty download wrote the database")
	}

	mode.Store(1)
	js := run()
	if !js.OK || js.Error != "" {
		t.Fatalf("parsed rules must be a success: %+v", js)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("an added source must get its own file:", err)
	}
	var doc botSourceDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Rules) != 1 || doc.Rules[0].Pattern != "(?i)Zoombot/" || doc.Rules[0].Source != "added" {
		t.Fatalf("source file rules %+v", doc.Rules)
	}
	raw, err := os.ReadFile(filepath.Join(e.dir, "data", detect.FileBotSignatures))
	if err != nil {
		t.Fatal(err)
	}
	db, _, err := detect.LoadBotDB(raw)
	if err != nil || db == nil || db.Len() != 1 {
		t.Fatalf("engine load = %v len %v", err, db)
	}

	mode.Store(2)
	before := append([]byte(nil), b...)
	js = run()
	if js.OK || !strings.Contains(js.Error, "HTTP 502") {
		t.Fatalf("a failed download must not be a success: %+v", js)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("the failed source replaced its file")
	}
	if f := e.readBotFile(); len(f.Rules) != 1 || f.Rules[0].Pattern != "(?i)Zoombot/" {
		t.Fatalf("the working database was replaced: %+v", f.Rules)
	}
}

// TestBotSignaturesValidate: source names must be unique and header-safe, URLs http(s).
func TestBotSignaturesValidate(t *testing.T) {
	for _, src := range [][]BotSource{
		{{Name: "", URL: "https://x.example/a"}},
		{{Name: "a b", URL: "https://x.example/a"}},
		{{Name: "a", URL: "https://x.example/a"}, {Name: "A", URL: "https://x.example/b"}},
		{{Name: "a", URL: "ftp://x.example/a"}},
	} {
		c := Default()
		c.BotSignatures.Sources = src
		if err := c.Validate(); err == nil {
			t.Errorf("%+v must be rejected", src)
		}
	}
	if Default().Interval(JobBotSignatures).Hours() != 24 {
		t.Fatal("the default period is 24 hours")
	}
}
