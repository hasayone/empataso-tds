package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// gatedStub is a provider that answers "late" once the gate opens.
func gatedStub(t *testing.T, gate *aiGate) *aiStub {
	t.Helper()
	return newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		select {
		case <-gate.ch:
		case <-r.Context().Done():
			return
		}
		aiJSONReply(w, http.StatusOK, aiAnthText("late", 5, 2))
	})
}

// TestAssistantTurnSurvivesAClosedPage checks that closing the stream of a turn, as a closed tab or a
// reload does, neither cancels the provider call nor marks the chat interrupted: the turn goes on, and
// when the provider answers the reply is stored without an error.
func TestAssistantTurnSurvivesAClosedPage(t *testing.T) {
	gate := newAIGate(t)
	stub := gatedStub(t, gate)
	e := newTurnEnv(t, stub, aiTurnSettings)
	chat := e.newChat(t)

	ctx, closePage := context.WithCancel(context.Background())
	resp, err := e.sendRaw(ctx, chat, `{"text":"long job"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the provider call", func() bool { return stub.calls() == 1 })
	closePage()
	resp.Body.Close()

	time.Sleep(300 * time.Millisecond)
	if c := e.chatOf(t, chat); c.Status != aiChatRunning || len(c.Messages) != 1 {
		t.Fatalf("a closed page changed the chat: status %q, %d messages", c.Status, len(c.Messages))
	}
	if e.noTurns() {
		t.Fatal("a closed page ended the turn")
	}

	gate.open()
	eventually(t, "the turn to store its reply", func() bool {
		return e.chatOf(t, chat).Status == aiChatIdle && e.noTurns()
	})
	c := e.chatOf(t, chat)
	if len(c.Messages) != 2 || c.Messages[1].Text != "late" || c.Messages[1].Error != "" {
		t.Fatalf("messages %+v", c.Messages)
	}
}

// TestAssistantTurnPageRejoinsARunningTurn checks that a new request of the same user joins the turn
// that runs in a chat after the first page left: it is given the events so far, follows the turn to done,
// and the chat holds the reply. Joining needs an event stream, a chat of the caller and a running turn.
func TestAssistantTurnPageRejoinsARunningTurn(t *testing.T) {
	gate := newAIGate(t)
	stub := gatedStub(t, gate)
	e := newTurnEnv(t, stub, aiTurnSettings)
	chat := e.newChat(t)

	ctx, closePage := context.WithCancel(context.Background())
	resp, err := e.sendRaw(ctx, chat, `{"text":"long job"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the provider call", func() bool { return stub.calls() == 1 })
	closePage()
	resp.Body.Close()

	join := func(accept string) (*http.Response, error) {
		req := e.request(context.Background(), http.MethodGet, "/api/ai/chats/"+chat+"/stream", "")
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		return e.c.Do(req)
	}
	if r, err := join(""); err != nil || r.StatusCode != http.StatusNotAcceptable {
		t.Fatalf("a join without an event stream: %v %v", r, err)
	}
	r, err := join("text/event-stream")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("join %d", r.StatusCode)
	}
	time.AfterFunc(100*time.Millisecond, gate.open)
	events, _ := readSSE(t, r.Body)
	names := eventNames(events)
	if len(eventsNamed(events, "status")) == 0 {
		t.Fatalf("the events before the join were not replayed: %v", names)
	}
	if last := lastEvent(t, events); last.name != "done" {
		t.Fatalf("events %v", names)
	}
	if texts := eventsNamed(events, "text"); len(texts) == 0 || !strings.Contains(texts[len(texts)-1].data, "late") {
		t.Fatalf("the reply did not reach the joined page: %v", names)
	}
	if c := e.chatOf(t, chat); c.Status != aiChatIdle || len(c.Messages) != 2 || c.Messages[1].Error != "" {
		t.Fatalf("chat %+v", c)
	}

	eventually(t, "the registry to empty", e.noTurns)
	if r, err := join("text/event-stream"); err != nil || r.StatusCode != http.StatusNotFound {
		t.Fatalf("a join after the end: %v %v", r, err)
	}
}

// TestAssistantTurnJoinAndCancelBelongToTheUser checks that another user's chat can be neither joined
// nor cancelled (404), that cancelling a chat with no running turn is 204, and that a turn of a chat is not
// joined through another chat of the same user.
func TestAssistantTurnJoinAndCancelBelongToTheUser(t *testing.T) {
	gate := newAIGate(t)
	stub := gatedStub(t, gate)
	e := newTurnEnv(t, stub, aiTurnSettings)
	running, other := e.newChat(t), e.newChat(t)
	foreign, err := e.s.aiChatCreate("u2", "private")
	if err != nil {
		t.Fatal(err)
	}

	resp, err := e.sendRaw(context.Background(), running, `{"text":"long job"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	eventually(t, "the provider call", func() bool { return stub.calls() == 1 })

	stream := func(id string) int {
		req := e.request(context.Background(), http.MethodGet, "/api/ai/chats/"+id+"/stream", "")
		req.Header.Set("Accept", "text/event-stream")
		r, err := e.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if code := stream(foreign.ID); code != http.StatusNotFound {
		t.Fatalf("join of a foreign chat: %d", code)
	}
	if code := stream(other); code != http.StatusNotFound {
		t.Fatalf("join of a chat with no turn: %d", code)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/ai/chats/"+foreign.ID+"/cancel", ""); code != http.StatusNotFound {
		t.Fatalf("cancel of a foreign chat: %d", code)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/ai/chats/"+other+"/cancel", ""); code != http.StatusNoContent {
		t.Fatalf("cancel of a chat with no turn: %d", code)
	}
	if e.noTurns() {
		t.Fatal("a cancel of another chat stopped the running turn")
	}
	gate.open()
	eventually(t, "the turn to end", func() bool { return e.chatOf(t, running).Status == aiChatIdle && e.noTurns() })
}

// TestAssistantChatHistoryIsKeptForTheUser checks that the chat is read again by a client with no state
// of the earlier page (a new session of the same user), with its messages, and that another user gets no
// trace of it: an empty list and 404 for the id.
func TestAssistantChatHistoryIsKeptForTheUser(t *testing.T) {
	stub := aiScriptStub(t, aiAnthText("the answer", 5, 2))
	e := newTurnEnv(t, stub, aiTurnSettings)
	chat := e.newChat(t)
	resp := e.send(t, chat, `{"text":"remember me"}`)
	events, _ := readSSE(t, resp.Body)
	resp.Body.Close()
	if last := lastEvent(t, events); last.name != "done" {
		t.Fatalf("events %v", eventNames(events))
	}

	jar, _ := cookiejar.New(nil)
	fresh := &http.Client{Jar: jar}
	if code, _ := login(t, fresh, e.srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("second login %d", code)
	}
	get := func(path string) (int, string) {
		r, err := fresh.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(raw)
	}
	code, body := get("/api/ai/chats")
	var list struct {
		Chats []aiChatMeta `json:"chats"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || code != http.StatusOK || len(list.Chats) != 1 || list.Chats[0].ID != chat {
		t.Fatalf("list after a reload: %d %s", code, body)
	}
	code, body = get("/api/ai/chats/" + chat)
	var view aiChatView
	if err := json.Unmarshal([]byte(body), &view); err != nil || code != http.StatusOK || len(view.Messages) != 2 ||
		view.Messages[0].Text != "remember me" || view.Messages[1].Text != "the answer" {
		t.Fatalf("chat after a reload: %d %s", code, body)
	}

	if metas, err := e.s.aiChatSearch("u2", ""); err != nil || len(metas) != 0 {
		t.Fatalf("another user sees %v (%v)", metas, err)
	}
	if _, err := e.s.aiChatGet("u2", chat); err == nil || aiErrOf(t, err).status != http.StatusNotFound {
		t.Fatalf("another user reads the chat: %v", err)
	}
}

// TestAIFeedReplay checks the feed of a turn: a page that joins late gets the kept events in order, the
// reply text only once and in its latest form, a feed that ended releases the page after the replay, and a
// page that was detached gets nothing more.
func TestAIFeedReplay(t *testing.T) {
	feed := newAIFeed()
	feed.event("status", map[string]string{"text": "step one"})
	feed.event("text", map[string]string{"text": "first"})
	feed.event("tool", map[string]string{"name": "list_offers"})
	feed.event("text", map[string]string{"text": "first and second"})
	feed.comment("ping")

	rec := httptest.NewRecorder()
	sub := feed.attach(rec)
	body := rec.Body.String()
	if !strings.HasPrefix(body, ": open\n\n") || strings.Contains(body, "ping") {
		t.Fatalf("replay %q", body)
	}
	if strings.Count(body, "event: text") != 1 || !strings.Contains(body, "first and second") || strings.Contains(body, `"first"`) {
		t.Fatalf("the reply text must be replayed once, latest: %q", body)
	}
	if strings.Index(body, "event: status") > strings.Index(body, "event: tool") {
		t.Fatalf("events out of order: %q", body)
	}

	feed.event("done", map[string]string{"message_id": "m1"})
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatal("a live event did not reach the attached page")
	}
	feed.detach(sub)
	select {
	case <-sub.gone:
	default:
		t.Fatal("a detached page was not released")
	}
	before := rec.Body.Len()
	feed.event("status", map[string]string{"text": "after"})
	if rec.Body.Len() != before {
		t.Fatal("a detached page was written to")
	}

	feed.close()
	late := httptest.NewRecorder()
	s2 := feed.attach(late)
	select {
	case <-s2.gone:
	default:
		t.Fatal("a page that joined an ended feed was not released")
	}
	if !strings.Contains(late.Body.String(), "event: done") {
		t.Fatalf("an ended feed lost its events: %q", late.Body.String())
	}
}

// TestAIFeedKeepsAtMostTheLimit checks that the replay log is bounded and keeps the latest reply text.
func TestAIFeedKeepsAtMostTheLimit(t *testing.T) {
	feed := newAIFeed()
	feed.event("text", map[string]string{"text": "reply"})
	for i := 0; i < aiFeedMaxEvents*2; i++ {
		feed.event("status", map[string]int{"step": i})
	}
	if len(feed.log) > aiFeedMaxEvents {
		t.Fatalf("log holds %d events", len(feed.log))
	}
	rec := httptest.NewRecorder()
	feed.attach(rec)
	if !strings.Contains(rec.Body.String(), "event: text") {
		t.Fatal("the reply text was dropped from the log")
	}
}
