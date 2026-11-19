package admin

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// aiAnthCut is an Anthropic answer cut by the output limit: text and the stop reason max_tokens.
func aiAnthCut(text string) string {
	return strings.Replace(aiAnthText(text, 40, 11), `"end_turn"`, `"max_tokens"`, 1)
}

// nudgeEnv runs one turn against a stub whose n-th answer is script(n). settings replace the
// steps of the default test settings with 20 so a turn has room for the extra calls, and the
// preset of the permissions with preset when it is not empty. It returns the stub, the raw stream
// and the last event.
func nudgeEnv(t *testing.T, preset, body string, script func(n int) string) (*aiStub, string, sseEvent) {
	t.Helper()
	stub := newAIStub(t, func(w http.ResponseWriter, r *http.Request, n int) {
		aiJSONReply(w, http.StatusOK, script(n))
	})
	settings := strings.Replace(aiTurnSettings, `"max_steps":3,`, `"max_steps":20,`, 1)
	if preset != "" {
		settings = strings.Replace(settings, `"preset":"full"`, `"preset":"`+preset+`"`, 1)
	}
	e := newTurnEnv(t, stub, settings)
	chat := e.newChat(t)
	resp := e.send(t, chat, body)
	defer resp.Body.Close()
	events, raw := readSSE(t, resp.Body)
	return stub, raw, lastEvent(t, events)
}

// aiToolStep is the answer of the n-th model call that calls the tool name with args.
func aiToolStep(n int, name, args string) string {
	return aiAnthTool("", fmt.Sprintf("tu%d", n), name, args)
}

func TestAIPromisesMore(t *testing.T) {
	for text, want := range map[string]bool{
		"Всего 10344 кампании. Загружу ещё страницы.":   true,
		"Продолжаю собирать данные.":                    true,
		"Зараз завантажу наступну сторінку.":            true,
		"Let me load the next page.":                    true,
		"I'll check the remaining campaigns.":           true,
		"Готово: найдено 12 кампаний с эмодзи.":         false,
		"Продолжить поиск по следующим эмодзи?":         false,
		"":                                              false,
		"All 12 campaigns are listed above, 3 are off.": false,
	} {
		if got := aiPromisesMore(text); got != want {
			t.Errorf("aiPromisesMore(%q) = %v, want %v", text, got, want)
		}
	}
}

// TestAssistantTurnPromiseWithoutCallGoesOn is the report: after tool work the model writes
// "Загружу ещё страницы" and no tool call. The turn must answer with a nudge and make the call.
func TestAssistantTurnPromiseWithoutCallGoesOn(t *testing.T) {
	stub, _, last := nudgeEnv(t, "", `{"text":"collect them"}`, func(n int) string {
		switch n {
		case 1, 3:
			return aiToolStep(n, "list_campaigns", `{"limit":200}`)
		case 2:
			return aiAnthText("Всего 10344 кампании. Загружу ещё страницы.", 40, 11)
		}
		return aiAnthText("all done", 30, 5)
	})
	if last.name != "done" {
		t.Fatalf("the last event is %s: %s", last.name, last.data)
	}
	if stub.calls() != 4 {
		t.Fatalf("model calls %d, want 4: the promise must lead to a second tool call", stub.calls())
	}
	if body := string(stub.req(2).Body); !strings.Contains(body, aiNudgeText) {
		t.Errorf("the call after the promise carries no nudge: %.300s", body)
	}
}

func TestAssistantTurnPromiseBeforeAnyToolIsAnAnswer(t *testing.T) {
	stub, _, last := nudgeEnv(t, "", `{"text":"hello"}`, func(int) string {
		return aiAnthText("Let me know if you need more.", 40, 11)
	})
	if last.name != "done" || stub.calls() != 1 {
		t.Fatalf("last %s, calls %d", last.name, stub.calls())
	}
}

func TestAssistantTurnCutReplyGoesOn(t *testing.T) {
	stub, _, last := nudgeEnv(t, "", `{"text":"go"}`, func(n int) string {
		if n == 1 {
			return aiAnthCut("The campaigns with the first emoji are")
		}
		return aiAnthText("final", 30, 5)
	})
	if last.name != "done" || stub.calls() != 2 {
		t.Fatalf("last %s, calls %d, want a call after the cut", last.name, stub.calls())
	}
	if !strings.Contains(string(stub.req(1).Body), aiNudgeText) {
		t.Error("the call after the cut carries no nudge")
	}
}

// TestAssistantTurnNudgesAreBounded gives a model that promises forever: the turn ends after
// aiMaxNudges nudges instead of looping to the step ceiling.
func TestAssistantTurnNudgesAreBounded(t *testing.T) {
	stub, _, last := nudgeEnv(t, "", `{"text":"go"}`, func(n int) string {
		if n == 1 {
			return aiToolStep(n, "list_campaigns", `{}`)
		}
		return aiAnthText("Загружу ещё страницы.", 40, 11)
	})
	if last.name != "done" {
		t.Fatalf("the last event is %s: %s", last.name, last.data)
	}
	if want := 1 + 1 + aiMaxNudges; stub.calls() != want {
		t.Fatalf("model calls %d, want %d", stub.calls(), want)
	}
}

// TestAssistantTurnSamePageErrorDoesNotLoop makes the tool fail on one call again and again: after
// the same failure twice the call is refused without running, and the turn ends with a plain line.
func TestAssistantTurnSamePageErrorDoesNotLoop(t *testing.T) {
	stub, raw, last := nudgeEnv(t, "", `{"text":"go","lang":"ru"}`, func(n int) string {
		return aiToolStep(n, "get_offer", `{"id":"missing"}`)
	})
	if last.name != "done" {
		t.Fatalf("the last event is %s: %s", last.name, last.data)
	}
	if want := aiSameFailMax + aiMaxRefusals; stub.calls() != want {
		t.Fatalf("model calls %d, want %d", stub.calls(), want)
	}
	if !strings.Contains(raw, "один и тот же вызов инструмента") {
		t.Errorf("the turn did not say why it stopped: %s", raw)
	}
	if !strings.Contains(string(stub.req(stub.calls()-1).Body), "repeated_failure") {
		t.Error("the model was not told about the repeated failure")
	}
}

func TestAIDeniedText(t *testing.T) {
	for _, c := range []struct{ lang, res, op, level, want string }{
		{"ru", "campaigns", aiOpUpdate, aiPermRead, "Раздел «Кампании» менять нельзя"},
		{"ru", "campaigns", aiOpUpdate, aiPermRead, "Читать можно."},
		{"en", "offers", aiOpCreate, aiPermRead, "I cannot change the section «Offers»"},
		{"en", "offers", aiOpCreate, aiPermRead, "I can read it."},
		{"uk", "landings", aiOpUpdate, aiPermRead, "Розділ «Лендінги» змінювати не можна"},
		{"uk", "landings", aiOpUpdate, aiPermRead, "Читати можна."},
		{"ru", "campaigns", aiOpDelete, aiPermEdit, "Удалять записи раздела «Кампании» нельзя"},
		{"ru", "campaigns", aiOpDelete, aiPermEdit, "Читать и править можно."},
		{"ru", "campaigns", aiOpDelete, aiPermRead, "менять нельзя"},
		{"xx", "domains", aiOpUpdate, aiPermRead, "Domains"},
	} {
		if got := aiDeniedText(c.lang, c.res, c.op, c.level); !strings.Contains(got, c.want) {
			t.Errorf("aiDeniedText(%s,%s,%s,%s) = %q, want %q", c.lang, c.res, c.op, c.level, got, c.want)
		}
	}
	if aiDeniedText("ru", "campaigns", "", aiPermRead) != "" {
		t.Error("a read is not a refusal")
	}
}

func TestAIReadOnlyNote(t *testing.T) {
	if got := aiReadOnlyNote("ru", aiPerms{Preset: aiPresetRead}); !strings.Contains(got, "Кампании") || strings.Contains(got, "Домены") {
		t.Errorf("read preset note: %q", got)
	}
	if got := aiReadOnlyNote("en", aiPerms{Preset: aiPresetEdit}); got != "" {
		t.Errorf("edit preset has no read-only resource except domains: %q", got)
	}
	custom := aiPerms{Preset: aiPresetCustom, Resources: map[string]string{"campaigns": aiPermRead, "offers": aiPermEdit}}
	if got := aiReadOnlyNote("en", aiNormalizePerms(custom)); !strings.Contains(got, "Campaigns") || strings.Contains(got, "Offers") {
		t.Errorf("custom note: %q", got)
	}
}

// TestAssistantTurnWriteRefusalSaysReadIsAllowed calls a change tool the preset does not give.
// The reply must tell the user, in the language of the panel, that the section cannot be changed and
// can be read, and the model must be told the user already knows.
func TestAssistantTurnWriteRefusalSaysReadIsAllowed(t *testing.T) {
	for _, c := range []struct {
		preset, tool, args, lang string
		want                     []string
	}{
		{"read", "update_campaign", `{"id":"g1","patch":{"name":"x"}}`, "ru", []string{"Раздел «Кампании» менять нельзя", "Читать можно."}},
		{"read", "create_offer", `{"patch":{"name":"x"}}`, "en", []string{"I cannot change the section «Offers»", "I can read it."}},
		{"read", "update_landing", `{"id":"l1","patch":{}}`, "uk", []string{"Розділ «Лендінги» змінювати не можна", "Читати можна."}},
		{"edit", "delete_campaign", `{"id":"g1"}`, "ru", []string{"Удалять записи раздела «Кампании» нельзя", "Читать и править можно."}},
	} {
		stub, raw, last := nudgeEnv(t, c.preset, fmt.Sprintf(`{"text":"change it","lang":%q}`, c.lang), func(n int) string {
			if n == 1 {
				return aiToolStep(n, c.tool, c.args)
			}
			return aiAnthText("ok", 30, 5)
		})
		if last.name != "done" {
			t.Fatalf("%s: the last event is %s: %s", c.tool, last.name, last.data)
		}
		for _, want := range c.want {
			if !strings.Contains(raw, want) {
				t.Errorf("%s: the reply lacks %q: %s", c.tool, want, raw)
			}
		}
		if !strings.Contains(string(stub.req(1).Body), "already been told") {
			t.Errorf("%s: the model was not told the user knows", c.tool)
		}
	}
}

// TestAssistantTurnReadOnlySystemNote checks the model is told which sections it may only read,
// and that a read tool still works there.
func TestAssistantTurnReadOnlySystemNote(t *testing.T) {
	stub, _, last := nudgeEnv(t, "read", `{"text":"read","lang":"ru"}`, func(n int) string {
		if n == 1 {
			return aiToolStep(n, "list_campaigns", `{}`)
		}
		return aiAnthText("done reading", 30, 5)
	})
	if last.name != "done" {
		t.Fatalf("the last event is %s: %s", last.name, last.data)
	}
	if !strings.Contains(string(stub.req(0).Body), "Ассистенту разрешено только читать") {
		t.Error("the system prompt lacks the read-only note")
	}
	if strings.Contains(string(stub.req(1).Body), "ai_forbidden") {
		t.Error("a read of a read-only section was refused")
	}
}
