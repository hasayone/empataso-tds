package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
)

// chgReferer is the IP filter planted in a stream of the campaign g1 by withIPStream.
const chgReferer = "203.0.113.55"

// withIPStream adds to the campaign g1 a stream "extra" whose referer filter holds an address.
func (e *chgEnv) withIPStream() {
	g := &e.groups.list[0]
	g.Streams = append(g.Streams, config.Stream{Name: "extra", Status: true, Rules: config.Rules{
		Referer: config.ListFilter{Flag: config.FlagB, Raw: chgReferer, Values: []string{chgReferer}},
	}})
}

// shownStreams returns the streams of the campaign id as the model sees them: hidden fields
// dropped, the address masked. The list may be changed by the caller.
func (e *chgEnv) shownStreams(t *testing.T, id string) []any {
	t.Helper()
	raw, ok, err := e.s.aiChgStored(context.Background(), "campaigns", id)
	if err != nil || !ok {
		t.Fatalf("campaign %s: %v %v", id, ok, err)
	}
	obj, err := aiChgObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	shown, _ := aiChgShow(newAIRedactor(e.s.processSecrets(), false), obj, true).(map[string]any)
	streams, _ := shown["streams"].([]any)
	if !strings.Contains(string(aiRaw(streams)), "203.0.113.x") {
		t.Fatalf("the address is not masked in the view: %s", aiRaw(streams))
	}
	return streams
}

// streamsArgs returns the arguments of an update of the campaign id that sets streams.
func streamsArgs(t *testing.T, id string, streams []any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"id": id, "patch": map[string]any{"streams": streams}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAssistantChangesUnmaskKeepsMaskedStreamValues checks a streams patch copied from the masked
// view (an IP filter) is applied with the stored address, and that the revert is byte for byte.
func TestAssistantChangesUnmaskKeepsMaskedStreamValues(t *testing.T) {
	e := newChgEnv(t)
	e.withHiddenFields()
	e.withIPStream()
	original := e.hashOf(t, "campaigns", "g1")
	streams := e.shownStreams(t, "g1")
	streams[0].(map[string]any)["comment"] = "first"
	streams[1].(map[string]any)["name"] = "extra2"
	v := e.mustPropose(t, "campaigns", aiOpUpdate, streamsArgs(t, "g1", streams))
	card, _ := json.Marshal(v)
	for _, text := range []string{string(card), e.chgFile(t)} {
		for _, secret := range append([]string{chgReferer}, chgHiddenCanaries...) {
			if strings.Contains(text, secret) {
				t.Fatalf("the proposal shows %q: %s", secret, text)
			}
		}
	}
	if len(v.Diff) != 1 || v.Diff[0].Field != "streams" {
		t.Fatalf("diff %+v", v.Diff)
	}
	e.mustApply(t, v.ID)
	g, _ := findGroup(e.groups.list, "g1")
	if len(g.Streams) != 2 || g.Streams[0].Comment != "first" || g.Streams[0].Curl != "CANARYCURL" || g.Streams[0].Remote.URL == "" {
		t.Fatalf("main stream: %+v", g.Streams[0])
	}
	if got := g.Streams[1]; got.Name != "extra2" || got.Rules.Referer.Raw != chgReferer || len(got.Rules.Referer.Values) != 1 || got.Rules.Referer.Values[0] != chgReferer {
		t.Fatalf("the stored address was lost: %+v", got)
	}
	e.mustRevert(t, v.ID)
	if e.hashOf(t, "campaigns", "g1") != original {
		t.Fatal("the revert did not restore the campaign byte for byte")
	}
}

// TestAssistantChangesUnmaskRefusesNewMaskedValue checks a masked address that the model made up,
// or changed, is still a redacted value, and so is a mask moved to another stream.
func TestAssistantChangesUnmaskRefusesNewMaskedValue(t *testing.T) {
	e := newChgEnv(t)
	e.withIPStream()
	streams := e.shownStreams(t, "g1")
	extra := streams[1].(map[string]any)
	extra["rules"].(map[string]any)["referer_text"].(map[string]any)["raw"] = "198.51.100.x"
	_, err := e.propose(t, "campaigns", aiOpUpdate, streamsArgs(t, "g1", streams))
	wantChgErr(t, "changed mask", err, http.StatusBadRequest, aiErrRedactedValue)

	streams = e.shownStreams(t, "g1")
	streams = append(streams, map[string]any{"name": "new", "status": true, "rules": streams[1].(map[string]any)["rules"]})
	_, err = e.propose(t, "campaigns", aiOpUpdate, streamsArgs(t, "g1", streams))
	wantChgErr(t, "copied to a new stream", err, http.StatusBadRequest, aiErrRedactedValue)

	_, err = e.propose(t, "campaigns", aiOpCreate, `{"patch":{"name":"x","streams":[{"name":"s","status":true,"rules":{"referer_text":{"flag":1,"raw":"203.0.113.x"}}}]}}`)
	wantChgErr(t, "create", err, http.StatusBadRequest, aiErrRedactedValue)
	if recs, _ := e.s.aiChgRead(); len(recs) != 0 {
		t.Fatalf("%d changes stored for refused patches", len(recs))
	}
}

// TestAssistantChangesUnmaskOfferURLToken checks an offer URL sent back as the view shows it (token
// marks) keeps the stored token while the name changes, and a changed masked URL is refused.
func TestAssistantChangesUnmaskOfferURLToken(t *testing.T) {
	e := newChgEnv(t)
	original := e.hashOf(t, "offers", "o1")
	offers, _ := e.s.cfg.Offers.List(context.Background())
	shownURL, _ := e.s.aiRedact(offers[0].URL).(string)
	if !strings.Contains(shownURL, aiRedactMark) || strings.Contains(shownURL, "CANARYQ") {
		t.Fatalf("the view of the URL: %s", shownURL)
	}
	args, _ := json.Marshal(map[string]any{"id": "o1", "patch": map[string]any{"name": "Alpha 2", "url": shownURL}})
	v := e.mustPropose(t, "offers", aiOpUpdate, string(args))
	if len(v.Diff) != 1 || v.Diff[0].Field != "name" || v.ExternalHostChange {
		t.Fatalf("diff %+v host %v", v.Diff, v.ExternalHostChange)
	}
	card, _ := json.Marshal(v)
	if strings.Contains(string(card)+e.chgFile(t), "CANARYQ") {
		t.Fatal("the proposal shows the token")
	}
	e.mustApply(t, v.ID)
	raw, _, _ := e.s.aiChgStored(context.Background(), "offers", "o1")
	if !strings.Contains(string(raw), "CANARYQ") || !strings.Contains(string(raw), "CANARYSIG") || !strings.Contains(string(raw), "Alpha 2") {
		t.Fatalf("the token was lost: %s", raw)
	}
	e.mustRevert(t, v.ID)
	if e.hashOf(t, "offers", "o1") != original {
		t.Fatal("the revert did not restore the offer byte for byte")
	}
	args, _ = json.Marshal(map[string]any{"id": "o1", "patch": map[string]any{"url": shownURL + "&x=1"}})
	_, err := e.propose(t, "offers", aiOpUpdate, string(args))
	wantChgErr(t, "changed masked url", err, http.StatusBadRequest, aiErrRedactedValue)
}

// TestAssistantChangesUnmaskRecheckedAtApply checks the unmasking is done again when the proposal
// is applied: a stored patch that would send a mark is refused.
func TestAssistantChangesUnmaskRecheckedAtApply(t *testing.T) {
	e := newChgEnv(t)
	offers, _ := e.s.cfg.Offers.List(context.Background())
	shownURL, _ := e.s.aiRedact(offers[0].URL).(string)
	args, _ := json.Marshal(map[string]any{"id": "o1", "patch": map[string]any{"name": "Alpha 2", "url": shownURL}})
	v := e.mustPropose(t, "offers", aiOpUpdate, string(args))
	err := e.s.aiChgMutate(func(recs *[]aiChangeRec) (bool, error) {
		(*recs)[0].Proposal.Patch = json.RawMessage(strings.Replace(string(aiRaw(map[string]any{"name": "Alpha 2", "url": shownURL})), "token=***", "token=***x", 1))
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.apply(v.ID, aiApplyOpts{})
	wantChgErr(t, "tampered patch", err, http.StatusBadRequest, aiErrRedactedValue)
}

// TestAssistantChangesUnmaskWalk checks aiChgUnmask on its own: arrays by position, streams by
// name, a leaf that differs from the view and a leaf without a counterpart.
func TestAssistantChangesUnmaskWalk(t *testing.T) {
	shown := map[string]any{
		"aliases": []any{"5.6.7.x", "plain"},
		"streams": []any{
			map[string]any{"name": "one", "v": "***"},
			map[string]any{"name": "two", "v": "1.2.3.x"},
		},
		"note": "***",
	}
	raw := map[string]any{
		"aliases": []any{"5.6.7.8", "plain"},
		"streams": []any{
			map[string]any{"name": "one", "v": "secret1"},
			map[string]any{"name": "two", "v": "1.2.3.4"},
		},
		"note": "secret",
	}
	patch := map[string]any{
		"aliases": []any{"5.6.7.x", "plain", "9.9.9.x"},
		"streams": []any{
			map[string]any{"name": "two", "v": "1.2.3.x"},
			map[string]any{"name": "one", "v": "***"},
			map[string]any{"name": "fresh", "v": "***"},
		},
		"note": "*** changed",
	}
	got, _ := aiChgUnmask(patch, shown, raw).(map[string]any)
	want := `{"aliases":["5.6.7.8","plain","9.9.9.x"],"note":"*** changed","streams":[{"name":"two","v":"1.2.3.4"},{"name":"one","v":"secret1"},{"name":"fresh","v":"***"}]}`
	if have := string(aiRaw(got)); have != want {
		t.Fatalf("got  %s\nwant %s", have, want)
	}
	if patch["note"] != "*** changed" || patch["aliases"].([]any)[0] != "5.6.7.x" {
		t.Fatal("the patch was modified")
	}
}

// TestAssistantChangesLinksError checks a refused delete is an *aiLinksError whose card names the
// dependents, at the proposal and at the apply, for a catalog record and for a campaign.
func TestAssistantChangesLinksError(t *testing.T) {
	e := newChgEnv(t)
	e.groups.list[0].Streams[0].Out = config.Output{Offers: []config.ShareRef{{ID: "o1"}}}
	_, err := e.propose(t, "offers", aiOpDelete, `{"id":"o1"}`)
	var le *aiLinksError
	if !errors.As(err, &le) {
		t.Fatalf("error %T %v", err, err)
	}
	wantChgErr(t, "proposal", err, http.StatusConflict, aiErrHasLinks)
	v := le.View
	if v.Op != aiOpDelete || v.Resource != "offers" || v.EntityID != "o1" || v.Title != "Alpha" || v.Status != aiChangeRejected {
		t.Fatalf("card %+v", v)
	}
	if len(v.Links) != 1 || v.Links[0] != (aiLinkView{Resource: "campaigns", ID: "g1", Name: "Demo"}) {
		t.Fatalf("links %+v", v.Links)
	}
	if aiCodeOf(v.Error) != aiErrHasLinks || v.Error != le.Error() || len(v.Diff) == 0 {
		t.Fatalf("error %q diff %v", v.Error, v.Diff)
	}
	if recs, _ := e.s.aiChgRead(); len(recs) != 0 {
		t.Fatal("a refused delete was stored")
	}
	if b, _ := json.Marshal(v); strings.Contains(string(b), "CANARYQ") {
		t.Fatal("the card shows the token")
	}

	n := newChgEnv(t)
	n9 := n.mustPropose(t, "affiliate_networks", aiOpDelete, `{"id":"n9"}`)
	offers, _ := n.s.cfg.Offers.List(context.Background())
	offers[1].NetworkID = "n9"
	if err := n.s.cfg.Offers.Save(context.Background(), offers); err != nil {
		t.Fatal(err)
	}
	_, err = n.apply(n9.ID, aiApplyOpts{ConfirmDelete: true})
	if !errors.As(err, &le) || len(le.View.Links) != 1 || le.View.Links[0].ID != "o9" || le.View.ID != n9.ID {
		t.Fatalf("apply of a network: %v", err)
	}

	c := newChgEnv(t)
	del := c.mustPropose(t, "campaigns", aiOpDelete, `{"id":"g9"}`)
	c.groups.list = append(c.groups.list, config.Group{ID: "g5", Name: "Hands over", Status: true, Redirect: "group", Out: "g9", Streams: []config.Stream{}})
	_, err = c.apply(del.ID, aiApplyOpts{ConfirmDelete: true})
	if !errors.As(err, &le) || len(le.View.Links) != 1 || le.View.Links[0].ID != "g5" || le.View.Status != aiChangeRejected {
		t.Fatalf("apply of a campaign: %v", err)
	}
	wantChgErr(t, "campaign", err, http.StatusConflict, aiErrHasLinks)
}

// TestAssistantChangesSecretCanaries checks that a secret stored in a network, a traffic source or a
// landing reaches neither a card, nor changes.json, nor a text meant for the model.
func TestAssistantChangesSecretCanaries(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		resource, canary, update, del string
		plant                         func(e *chgEnv)
	}{
		{"affiliate_networks", "CANARYNET", `{"id":"n9","patch":{"name":"Net 2"}}`, `{"id":"n9"}`, func(e *chgEnv) {
			nets, _ := e.s.cfg.Networks.List(ctx)
			for i := range nets {
				nets[i].PostbackURL += "&key=CANARYNET"
			}
			if err := e.s.cfg.Networks.Save(ctx, nets); err != nil {
				t.Fatal(err)
			}
		}},
		{"traffic_sources", "CANARYSRC", `{"id":"s1","patch":{"name":"Src 2"}}`, `{"id":"s9"}`, func(e *chgEnv) {
			list, _ := e.s.cfg.Sources.List(ctx)
			for i := range list {
				if list[i].ID == "s9" {
					list[i].Parameters["token"] = "CANARYSRC"
				}
			}
			if err := e.s.cfg.Sources.Save(ctx, list); err != nil {
				t.Fatal(err)
			}
		}},
		{"landings", "CANARYLAND", `{"id":"l1","patch":{"name":"Land 2"}}`, `{"id":"l1"}`, func(e *chgEnv) {
			list, _ := e.s.cfg.Landings.List(ctx)
			for i := range list {
				if list[i].ID == "l1" {
					list[i].URL += "?key=CANARYLAND"
				}
			}
			if err := e.s.cfg.Landings.Save(ctx, list); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.resource, func(t *testing.T) {
			e := newChgEnv(t)
			c.plant(e)
			var texts []string
			upd := e.mustPropose(t, c.resource, aiOpUpdate, c.update)
			del := e.mustPropose(t, c.resource, aiOpDelete, c.del)
			for _, v := range []aiChangeView{upd, del} {
				b, _ := json.Marshal(v)
				texts = append(texts, string(b))
			}
			texts = append(texts, e.chgFile(t))
			_, err := e.propose(t, c.resource, aiOpUpdate, strings.Replace(c.update, `"name":"`, `"name":"*** `, 1))
			wantChgErr(t, "mark", err, http.StatusBadRequest, aiErrRedactedValue)
			texts = append(texts, err.Error())
			if c.resource == "affiliate_networks" {
				e.groups.list[0].Streams[0].Out = config.Output{Offers: []config.ShareRef{{ID: "o1"}}}
				_, err = e.propose(t, c.resource, aiOpDelete, `{"id":"n1"}`)
				wantChgErr(t, "links", err, http.StatusConflict, aiErrHasLinks)
				texts = append(texts, err.Error())
			}
			for _, text := range texts {
				if strings.Contains(text, c.canary) {
					t.Fatalf("%q leaks %s", text[:min(len(text), 80)], c.canary)
				}
			}
			if !strings.Contains(texts[1], aiRedactMark) {
				t.Fatalf("the delete card shows no mark: %s", texts[1])
			}
			e.mustApply(t, upd.ID)
		})
	}
}
