package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

// chatClock returns a clock that moves one minute on every call, starting at start.
func chatClock(start time.Time) func() time.Time {
	now := start
	return func() time.Time {
		now = now.Add(time.Minute)
		return now
	}
}

// chatIDs returns the ids of the list lines of uid.
func chatIDs(t *testing.T, s *Server, uid, q string) []string {
	t.Helper()
	metas, err := s.aiChatSearch(uid, q)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(metas))
	for i, m := range metas {
		ids[i] = m.ID
	}
	return ids
}

// setChatStatus changes the stored status of chat id of uid.
func setChatStatus(t *testing.T, s *Server, uid, id, status string) {
	t.Helper()
	if _, err := s.aiChatUpdate(uid, id, func(c *aiChat) error { c.Status = status; return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantChatsHTTPLifecycle(t *testing.T) {
	e := newTurnEnv(t, nil, aiTurnSettings)
	code, body := e.do(t, http.MethodPost, "/api/ai/chats", `{"title":"  Hello\n\tworld  "}`)
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, body)
	}
	var made aiChatView
	if err := json.Unmarshal([]byte(body), &made); err != nil {
		t.Fatal(err)
	}
	if made.Title != "Hello world" || made.Status != aiChatIdle || len(made.Messages) != 0 || made.Changes == nil {
		t.Fatalf("created %+v", made)
	}
	other := e.newChat(t)

	list := func(q string) []aiChatMeta {
		code, body := e.do(t, http.MethodGet, "/api/ai/chats"+q, "")
		if code != http.StatusOK {
			t.Fatalf("list %d %s", code, body)
		}
		var v struct {
			Chats []aiChatMeta `json:"chats"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatal(err)
		}
		return v.Chats
	}
	if got := list(""); len(got) != 2 || got[0].ID != other {
		t.Fatalf("list %+v", got)
	}
	if got := list("?q=HELLO"); len(got) != 1 || got[0].ID != made.ID {
		t.Fatalf("search %+v", got)
	}
	if got := list("?q=zzz"); len(got) != 0 {
		t.Fatalf("search %+v", got)
	}

	card, err := e.s.aiPropose(context.Background(), aiProposeReq{
		Sess: e.root, ChatID: made.ID, MsgID: "m1", Tool: e.tool(t, "offers", aiOpUpdate),
		Args: json.RawMessage(`{"id":"o1","patch":{"name":"Other"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body = e.do(t, http.MethodGet, "/api/ai/chats/"+made.ID, "")
	var got aiChatView
	if err := json.Unmarshal([]byte(body), &got); code != http.StatusOK || err != nil || len(got.Changes) != 1 || got.Changes[0].ID != card.ID {
		t.Fatalf("get %d %s", code, body)
	}

	if code, body := e.do(t, http.MethodDelete, "/api/ai/chats/"+made.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete %d %s", code, body)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/ai/chats/"+made.ID, ""); code != http.StatusNotFound {
		t.Fatalf("a deleted chat answers %d", code)
	}
	if code, _ := e.do(t, http.MethodDelete, "/api/ai/chats/"+made.ID, ""); code != http.StatusNotFound {
		t.Fatalf("a second delete answers %d", code)
	}
	if status := e.status(t, card.ID); status != aiChangeProposed {
		t.Fatalf("a deleted chat took its change along: %q", status)
	}
	for _, id := range []string{"../../ai", "../index", "", strings.Repeat("a", 25), "AAAAAAAAAAAAAAAAAAAAAAAA"} {
		if _, err := e.s.aiChatGet("env-admin", id); aiErrOf(t, err).status != http.StatusNotFound {
			t.Errorf("id %q: %v", id, err)
		}
	}
}

func TestAssistantChatsOffWhenDisabled(t *testing.T) {
	e := newTurnEnv(t, nil, `{"enabled":false}`)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/ai/chats", ""},
		{http.MethodPost, "/api/ai/chats", `{}`},
		{http.MethodGet, "/api/ai/chats/aaaaaaaaaaaaaaaaaaaaaaaa", ""},
		{http.MethodDelete, "/api/ai/chats/aaaaaaaaaaaaaaaaaaaaaaaa", ""},
	} {
		code, body := e.do(t, c.method, c.path, c.body)
		if code != http.StatusServiceUnavailable || aiBodyCode(t, body) != aiErrDisabled {
			t.Errorf("%s %s: %d %s", c.method, c.path, code, body)
		}
	}
}

func TestAssistantChatTitleHidesSecrets(t *testing.T) {
	e := newTurnEnv(t, nil, aiTurnSettings)
	code, body := e.do(t, http.MethodPost, "/api/ai/chats", `{"title":"my key is `+aiCanary+`"}`)
	if code != http.StatusCreated || strings.Contains(body, aiCanary) {
		t.Fatalf("create %d %s", code, body)
	}
}

func TestAssistantChatsBelongToTheirOwner(t *testing.T) {
	e := newAITestEnv(t, nil)
	c, err := e.s.aiChatCreate("u-other", "theirs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.aiChatGet("env-admin", c.ID); aiErrOf(t, err).status != http.StatusNotFound {
		t.Fatalf("another user's chat: %v", err)
	}
	if err := e.s.aiChatDelete("env-admin", c.ID); aiErrOf(t, err).status != http.StatusNotFound {
		t.Fatalf("delete of another user's chat: %v", err)
	}
	if ids := chatIDs(t, e.s, "env-admin", ""); len(ids) != 0 {
		t.Fatalf("list %v", ids)
	}
	if _, err := e.s.aiChatGet("u-other", c.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantChatKeyIsSafe(t *testing.T) {
	e := newAITestEnv(t, nil)
	if got := aiChatKey("env-admin"); got != "env-admin" {
		t.Fatalf("plain uid mapped to %q", got)
	}
	for _, uid := range []string{"../x", "a/b", "a:b", "env:x", "ünï", "", strings.Repeat("a", 65), "a b"} {
		key := aiChatKey(uid)
		if !aiUIDPattern.MatchString(key) || key == uid {
			t.Errorf("%q mapped to %q", uid, key)
		}
		if got, root := e.s.aiChatDirOf(uid), filepath.Join(e.s.cfg.AIDir, aiChatsDir); filepath.Dir(got) != root {
			t.Errorf("%q: directory %s is outside %s", uid, got, root)
		}
	}
	if aiChatKey("a/b") == aiChatKey("a-b") {
		t.Error("two uids share a directory")
	}
}

func TestAssistantChatsKeepTwoHundredPerUser(t *testing.T) {
	e := newAITestEnv(t, nil)
	e.s.ai.chats.now = chatClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	first, err := e.s.aiChatCreate("u1", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.s.aiChatCreate("u1", "second")
	if err != nil {
		t.Fatal(err)
	}
	setChatStatus(t, e.s, "u1", first.ID, aiChatRunning)
	for i := 2; i < aiChatsPerUser+1; i++ {
		if _, err := e.s.aiChatCreate("u1", "n"+itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	ids := chatIDs(t, e.s, "u1", "")
	if len(ids) != aiChatsPerUser {
		t.Fatalf("%d chats", len(ids))
	}
	if _, err := e.s.aiChatGet("u1", second.ID); aiErrOf(t, err).status != http.StatusNotFound {
		t.Fatalf("the oldest idle chat must be dropped: %v", err)
	}
	if _, err := e.s.aiChatGet("u1", first.ID); err != nil {
		t.Fatalf("a running chat must stay: %v", err)
	}
	if n := chatCount(t, e.s, "u1"); n != aiChatsPerUser {
		t.Fatalf("%d files for %d chats", n, aiChatsPerUser)
	}
	if _, err := e.s.aiChatCreate("u2", "x"); err != nil {
		t.Fatalf("another user is not limited: %v", err)
	}
}

// chatCount returns how many chat files uid holds on disk.
func chatCount(t *testing.T, s *Server, uid string) int {
	t.Helper()
	entries, err := os.ReadDir(s.aiChatDirOf(uid))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, en := range entries {
		if aiChatFileName.MatchString(en.Name()) {
			n++
		}
	}
	return n
}

func TestAssistantChatRotation(t *testing.T) {
	e := newAITestEnv(t, nil)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e.s.ai.chats.now = chatClock(start)
	old, err := e.s.aiChatCreate("u1", "old")
	if err != nil {
		t.Fatal(err)
	}
	busy, err := e.s.aiChatCreate("u1", "busy")
	if err != nil {
		t.Fatal(err)
	}
	setChatStatus(t, e.s, "u1", busy.ID, aiChatRunning)
	e.s.ai.chats.now = chatClock(start.Add(59 * 24 * time.Hour))
	fresh, err := e.s.aiChatCreate("u2", "fresh")
	if err != nil {
		t.Fatal(err)
	}

	if n, err := e.s.aiRotateChats(start.Add(59 * 24 * time.Hour)); err != nil || n != 0 {
		t.Fatalf("nothing is 60 days old yet: %d %v", n, err)
	}
	now := start.Add(61 * 24 * time.Hour)
	if n, err := e.s.aiRotateChats(now); err != nil || n != 1 {
		t.Fatalf("removed %d %v", n, err)
	}
	if _, err := e.s.aiChatGet("u1", old.ID); err == nil {
		t.Fatal("a chat older than 60 days stayed")
	}
	if ids := chatIDs(t, e.s, "u1", ""); len(ids) != 1 || ids[0] != busy.ID {
		t.Fatalf("a running chat must stay: %v", ids)
	}
	if _, err := e.s.aiChatGet("u2", fresh.ID); err != nil {
		t.Fatalf("a young chat was removed: %v", err)
	}

	setChatStatus(t, e.s, "u1", busy.ID, aiChatIdle)
	e.s.ai.chats.now = nil
	if n, err := e.s.aiRotateChats(time.Now().Add(120 * 24 * time.Hour)); err != nil || n != 2 {
		t.Fatalf("removed %d %v", n, err)
	}
	if _, err := os.Stat(e.s.aiChatDirOf("u1")); !os.IsNotExist(err) {
		t.Fatalf("an empty user directory stayed: %v", err)
	}
}

func TestAssistantWatchRunsASweepAndStops(t *testing.T) {
	e := newAITestEnv(t, nil)
	e.s.ai.chats.now = func() time.Time { return time.Now().Add(-100 * 24 * time.Hour) }
	old, err := e.s.aiChatCreate("u1", "old")
	if err != nil {
		t.Fatal(err)
	}
	e.s.ai.chats.now = nil
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.s.WatchAssistant(ctx)
		close(done)
	}()
	eventually(t, "the first sweep", func() bool {
		_, err := e.s.aiChatGet("u1", old.ID)
		return err != nil
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchAssistant did not stop")
	}

	off := newAITestEnv(t, func(c *Config) { c.AIDir = "" })
	finished := make(chan struct{})
	go func() {
		off.s.WatchAssistant(context.Background())
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchAssistant must return at once without a directory")
	}
}

func TestAssistantChatRecoveryInterruptsRunning(t *testing.T) {
	e := newAITestEnv(t, nil)
	e.s.ai.chats.now = chatClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	c, err := e.s.aiChatCreate("u1", "x")
	if err != nil {
		t.Fatal(err)
	}
	setChatStatus(t, e.s, "u1", c.ID, aiChatRunning)
	before, err := e.s.aiChatGet("u1", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.s.aiRecoverChats()
	after, err := e.s.aiChatGet("u1", c.ID)
	if err != nil || after.Status != aiChatInterrupted || !after.Updated.Equal(before.Updated) {
		t.Fatalf("after recovery %+v %v", after, err)
	}
	metas, err := e.s.aiChatSearch("u1", "")
	if err != nil || len(metas) != 1 || metas[0].Status != aiChatInterrupted {
		t.Fatalf("index %+v %v", metas, err)
	}
}

func TestAssistantChatIndexIsRebuilt(t *testing.T) {
	e := newAITestEnv(t, nil)
	a, err := e.s.aiChatCreate("u1", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.s.aiChatCreate("u1", "beta")
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(e.s.aiChatDirOf("u1"), aiChatIndexFile)
	for _, damage := range []func(){
		func() { _ = os.Remove(index) },
		func() { _ = os.WriteFile(index, []byte("{broken"), 0o600) },
	} {
		damage()
		ids := chatIDs(t, e.s, "u1", "")
		if len(ids) != 2 || !(ids[0] == a.ID || ids[1] == a.ID) || !(ids[0] == b.ID || ids[1] == b.ID) {
			t.Fatalf("rebuilt list %v", ids)
		}
	}
	if err := os.WriteFile(filepath.Join(e.s.aiChatDirOf("u1"), "notes.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(index)
	if ids := chatIDs(t, e.s, "u1", ""); len(ids) != 2 {
		t.Fatalf("a stray file became a chat: %v", ids)
	}
}

func TestAssistantChatsDiskFullAndSizeLimits(t *testing.T) {
	e := newAITestEnv(t, nil)
	c, err := e.s.aiChatCreate("u1", "x")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.s.aiChatUpdate("u1", c.ID, func(c *aiChat) error {
		c.Messages = append(c.Messages, aiMessage{ID: "m", Role: aiRoleUser, Text: strings.Repeat("a", aiChatMaxBytes)})
		return nil
	})
	if ae := aiErrOf(t, err); ae.code != aiErrChatFull || ae.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized chat: %v", err)
	}
	if got, err := e.s.aiChatGet("u1", c.ID); err != nil || len(got.Messages) != 0 {
		t.Fatalf("a refused write changed the chat: %+v %v", got, err)
	}

	e.s.ai.chats.dirCap = 1
	_, err = e.s.aiChatCreate("u1", "y")
	if ae := aiErrOf(t, err); ae.code != aiErrDiskFull || ae.status != http.StatusInsufficientStorage {
		t.Fatalf("full directory: %v", err)
	}
}

func TestAssistantChatFilePermissions(t *testing.T) {
	aiOnPosix(t)
	e := newAITestEnv(t, nil)
	c, err := e.s.aiChatCreate("u1", "x")
	if err != nil {
		t.Fatal(err)
	}
	dir := e.s.aiChatDirOf("u1")
	for path, want := range map[string]os.FileMode{
		dir:                                 0o700,
		filepath.Dir(dir):                   0o700,
		filepath.Join(dir, c.ID+".json"):    0o600,
		filepath.Join(dir, aiChatIndexFile): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %o", path, info, err, want)
		}
	}
}

func TestAssistantDeletingAUserRemovesTheirChats(t *testing.T) {
	e := newTurnEnv(t, nil, aiTurnSettings)
	u, err := e.s.createUser(userDraft{Login: "boss", Password: "secret1", Type: security.UserAdmin, Enabled: true,
		Access: security.Access{Grants: map[string]security.Grant{}}})
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	boss := &turnEnv{chgEnv: e.chgEnv, srv: e.srv, c: &http.Client{Jar: jar}, log: e.log}
	_, boss.csrf = login(t, boss.c, e.srv.URL, "boss", "secret1")
	boss.newChat(t)
	own := e.newChat(t)
	dir := e.s.aiChatDirOf(u.ID)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if code, body := e.do(t, http.MethodDelete, "/api/users/"+u.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete user %d %s", code, body)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the chats of a deleted user stayed: %v", err)
	}
	if _, err := e.s.aiChatGet("env-admin", own); err != nil {
		t.Fatalf("another user's chat went with it: %v", err)
	}
}

func TestAssistantChatIndexWriteFailureDropsTheIndex(t *testing.T) {
	aiOnPosix(t)
	e := newAITestEnv(t, nil)
	c, err := e.s.aiChatCreate("u1", "before")
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(e.s.aiChatDirOf("u1"), aiChatIndexFile)
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(index, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.aiChatUpdate("u1", c.ID, func(c *aiChat) error { c.Title = "after"; return nil }); err == nil {
		t.Fatal("the index write was expected to fail")
	}
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Fatalf("a failed index write left %v behind", err)
	}
	metas, err := e.s.aiChatSearch("u1", "after")
	if err != nil || len(metas) != 1 || metas[0].ID != c.ID {
		t.Fatalf("the rebuilt list %+v %v", metas, err)
	}
}
