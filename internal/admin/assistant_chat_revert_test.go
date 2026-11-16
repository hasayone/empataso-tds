package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// proposeIn proposes a change as the root session in chat chatID, reply msgID, and fails the test on an error.
func (e *chgEnv) proposeIn(t *testing.T, chatID, msgID, resource, op, args string) aiChangeView {
	t.Helper()
	v, err := e.s.aiPropose(context.Background(), aiProposeReq{
		Sess: e.root, ChatID: chatID, MsgID: msgID, Tool: e.tool(t, resource, op), Args: json.RawMessage(args),
	})
	if err != nil {
		t.Fatalf("propose %s %s: %v", resource, op, err)
	}
	return v
}

// TestAssistantRevertChat checks a chat is rolled back over all its replies, newest change first,
// skips the changes already reverted, restores a delete, leaves other chats alone, and stops at a
// conflict with the reverted, stopped and remaining ids in the report.
func TestAssistantRevertChat(t *testing.T) {
	t.Run("all replies newest first", func(t *testing.T) {
		e := newChgEnv(t)
		offer, network := e.hashOf(t, "offers", "o1"), e.hashOf(t, "affiliate_networks", "n1")
		a := e.proposeIn(t, "c1", "m1", "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"First"}}`)
		e.mustApply(t, a.ID)
		skipped := e.proposeIn(t, "c1", "m1", "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Undone"}}`)
		e.mustApply(t, skipped.ID)
		e.mustRevert(t, skipped.ID)
		b := e.proposeIn(t, "c1", "m2", "affiliate_networks", aiOpUpdate, `{"id":"n1","patch":{"name":"Second"}}`)
		e.mustApply(t, b.ID)
		d := e.proposeIn(t, "c1", "m3", "offers", aiOpDelete, `{"id":"o9"}`)
		e.mustApply(t, d.ID)
		other := e.proposeIn(t, "c2", "m1", "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Other chat"}}`)

		report, err := e.s.revertChat(context.Background(), e.root, "c1")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{d.ID, b.ID, a.ID}
		if len(report.Reverted) != len(want) || report.StoppedAt != "" || len(report.Remaining) != 0 {
			t.Fatalf("report %+v", report)
		}
		for i, id := range want {
			if report.Reverted[i] != id {
				t.Fatalf("revert order %v, want %v", report.Reverted, want)
			}
		}
		if e.hashOf(t, "offers", "o1") != offer || e.hashOf(t, "affiliate_networks", "n1") != network {
			t.Fatal("the records are not back to the original")
		}
		if e.hashOf(t, "offers", "o9") == "" {
			t.Fatal("the deleted offer is not restored")
		}
		if e.status(t, skipped.ID) != aiChangeReverted || e.status(t, other.ID) != aiChangeProposed {
			t.Fatal("a skipped change or another chat was touched")
		}
		again, err := e.s.revertChat(context.Background(), e.root, "c1")
		if err != nil || len(again.Reverted) != 0 || again.StoppedAt != "" {
			t.Fatalf("a second run has nothing to do: %+v %v", again, err)
		}
	})
	t.Run("stops on conflict", func(t *testing.T) {
		e := newChgEnv(t)
		a := e.proposeIn(t, "c1", "m1", "affiliate_networks", aiOpUpdate, `{"id":"n1","patch":{"name":"Oldest"}}`)
		e.mustApply(t, a.ID)
		b := e.proposeIn(t, "c1", "m1", "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Middle"}}`)
		e.mustApply(t, b.ID)
		c := e.proposeIn(t, "c1", "m2", "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Newest"}}`)
		e.mustApply(t, c.ID)
		offers, _ := e.s.cfg.Offers.List(context.Background())
		for i := range offers {
			if offers[i].ID == "o1" {
				offers[i].Name = "Hand edit"
			}
		}
		_ = e.s.cfg.Offers.Save(context.Background(), offers)
		edited := e.hashOf(t, "offers", "o1")

		report, err := e.s.revertChat(context.Background(), e.root, "c1")
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Reverted) != 1 || report.Reverted[0] != c.ID || report.StoppedAt != b.ID ||
			report.ErrorCode != aiErrConflict || len(report.Remaining) != 1 || report.Remaining[0] != a.ID {
			t.Fatalf("report %+v", report)
		}
		if e.status(t, c.ID) != aiChangeReverted || e.status(t, b.ID) != aiChangeDone || e.status(t, a.ID) != aiChangeDone {
			t.Fatal("wrong statuses after a stopped run")
		}
		if e.hashOf(t, "offers", "o1") != edited {
			t.Fatal("the hand edit was overwritten")
		}
	})
	t.Run("handler", func(t *testing.T) {
		e := newChgEnv(t)
		a := e.proposeIn(t, "c1", "m1", "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"First"}}`)
		e.mustApply(t, a.ID)
		req := httptest.NewRequest(http.MethodPost, "/api/ai/chats/c1/revert", nil)
		req.SetPathValue("id", "c1")
		rec := httptest.NewRecorder()
		e.s.handleAIChatRevert(rec, req.WithContext(context.WithValue(req.Context(), sessionKey, e.root)))
		var report aiRevertReport
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &report) != nil || len(report.Reverted) != 1 || report.Reverted[0] != a.ID {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
}
