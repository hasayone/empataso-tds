package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hasayone/empataso-tds/internal/security"
)

// aiDocsBody returns the body of a settings PUT that saves the given documents.
func aiDocsBody(t *testing.T, docs map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"docs": docs})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// aiDocsFile returns the path of the file of a document in the test directory.
func (e *aiTestEnv) aiDocsFile(name string) string {
	return filepath.Join(e.dir, "assistant", aiDocsDir, name)
}

func TestAIDocsDefaultsAreShownAndNotWritten(t *testing.T) {
	e := newAITestEnv(t, nil)
	v := e.view(t)
	if v.Docs.Soul != aiDocLF(aiDocSoulDefault) || v.Docs.Memory != aiDocLF(aiDocMemoryDefault) || v.Docs.Expertise != aiDocLF(aiDocExpertiseDefault) {
		t.Fatalf("the view does not carry the built-in texts: %+v", v.Docs)
	}
	if v.Docs.MaxBytes != aiDocMaxBytes {
		t.Fatalf("max_bytes %d", v.Docs.MaxBytes)
	}
	e.mustPut(t, `{"enabled":false}`)
	if _, err := os.Stat(filepath.Join(e.dir, "assistant", aiDocsDir)); err == nil {
		t.Fatal("a save without documents wrote the defaults to the volume")
	}
}

func TestAIDocsDefaultsHoldNoSecretAndFit(t *testing.T) {
	for _, d := range aiDocSpecs {
		if d.def == "" || len(d.def) > aiDocMaxBytes {
			t.Errorf("%s: default is %d bytes", d.key, len(d.def))
		}
		if aiRedactText(d.def, []string{aiCanary}) != d.def {
			t.Errorf("%s: the redactor changes the default, so it holds a secret-like text", d.key)
		}
	}
}

func TestAIDocsSaveSurvivesRereadAndRestart(t *testing.T) {
	e := newAITestEnv(t, nil)
	const mine = "# Моя экспертиза\nКампании `#/flows/{slug}`.\n"
	v := e.mustPut(t, aiDocsBody(t, map[string]string{"expertise": mine}))
	if v.Docs.Expertise != mine {
		t.Fatalf("the answer of the save holds %q", v.Docs.Expertise)
	}
	if got := e.view(t).Docs; got.Expertise != mine || got.Soul != aiDocLF(aiDocSoulDefault) || got.Memory != aiDocLF(aiDocMemoryDefault) {
		t.Fatalf("reread: %+v", got)
	}
	if raw, err := os.ReadFile(e.aiDocsFile("expertise.md")); err != nil || string(raw) != mine {
		t.Fatalf("file %q %v", raw, err)
	}
	for _, other := range []string{"soul.md", "memory.md"} {
		if _, err := os.Stat(e.aiDocsFile(other)); err == nil {
			t.Errorf("%s was written although it was not saved", other)
		}
	}
	e.mustPut(t, `{"enabled":false}`)
	restarted := newAITestEnv(t, func(c *Config) {
		c.AIFile = filepath.Join(e.dir, "ai.json")
		c.AIDir = filepath.Join(e.dir, "assistant")
	})
	if got := restarted.view(t).Docs.Expertise; got != mine {
		t.Fatalf("after a restart the document is %q", got)
	}
	if raw, _ := os.ReadFile(e.aiDocsFile("expertise.md")); string(raw) != mine {
		t.Fatalf("the start rewrote the saved document: %q", raw)
	}
}

func TestAIDocsLineBreaksAreLF(t *testing.T) {
	e := newAITestEnv(t, nil)
	if err := os.MkdirAll(filepath.Dir(e.aiDocsFile("memory.md")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.aiDocsFile("memory.md"), []byte("one\r\ntwo\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.view(t).Docs.Memory; got != "one\ntwo\n" {
		t.Fatalf("a hand-edited file reads as %q", got)
	}
	e.mustPut(t, aiDocsBody(t, map[string]string{"soul": "a\r\nb"}))
	if raw, _ := os.ReadFile(e.aiDocsFile("soul.md")); string(raw) != "a\nb" {
		t.Fatalf("a save keeps CRLF: %q", raw)
	}
	if strings.Contains(aiDocLF(aiDocExpertiseDefault), "\r") {
		t.Fatal("a built-in text keeps CR")
	}
}

func TestAIDocsEmptyStaysEmpty(t *testing.T) {
	e := newAITestEnv(t, nil)
	e.mustPut(t, aiDocsBody(t, map[string]string{"memory": ""}))
	if got := e.view(t).Docs.Memory; got != "" {
		t.Fatalf("an empty document reads as %q", got)
	}
	block := e.s.aiPersonaBlock(nil)
	if strings.Contains(block, aiDocLF(aiDocMemoryDefault)) {
		t.Fatal("an empty document was replaced by the default in the context")
	}
	if !strings.Contains(block, "## Memory\n"+aiDocEmptyNote) {
		t.Fatalf("an empty document has no mark:\n%s", block)
	}
}

func TestAIDocsRefusals(t *testing.T) {
	e := newAITestEnv(t, nil)
	plain, err := e.s.createUser(userDraft{Login: "bob", Password: "secret1", Type: security.UserRegular, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	body := aiDocsBody(t, map[string]string{"soul": "I am hijacked"})
	for who, sess := range map[string]security.Session{
		"user":      {UID: plain.ID, User: "bob"},
		"admin key": {API: true, KeyType: security.KeyTypeAdmin, KeyID: "k1"},
		"user key":  {API: true, KeyType: security.KeyTypeUser, KeyID: "k2"},
	} {
		if code, out := e.call(e.s.handleAISettingsPut, http.MethodPut, body, sess); code != http.StatusForbidden {
			t.Errorf("%s: %d %s", who, code, out)
		}
	}
	if _, err := os.Stat(e.aiDocsFile("soul.md")); err == nil {
		t.Fatal("a refused save wrote a document")
	}
	if code, out := e.call(e.s.handleAISettingsGet, http.MethodGet, "", security.Session{UID: plain.ID, User: "bob"}); code != http.StatusForbidden || strings.Contains(out, "встроенный ассистент") {
		t.Fatalf("a user reads the documents through the settings: %d", code)
	}

	long := aiDocsBody(t, map[string]string{"memory": "fine", "soul": strings.Repeat("я", aiDocMaxBytes)})
	code, out := e.put(long)
	if code != http.StatusBadRequest || aiBodyCode(t, out) != aiErrBadRequest {
		t.Fatalf("too long: %d %s", code, out)
	}
	var e2 struct {
		Params map[string]string `json:"params"`
	}
	if err := json.Unmarshal([]byte(out), &e2); err != nil || e2.Params["field"] != "docs.soul" || e2.Params["reason"] != aiReasonTooLong {
		t.Fatalf("params %v %v", e2.Params, err)
	}
	if _, err := os.Stat(e.aiDocsFile("memory.md")); err == nil {
		t.Fatal("a refused save wrote the documents that were fine")
	}
	if code, _ := e.put(aiDocsBody(t, map[string]string{"soul": strings.Repeat("a", aiDocMaxBytes)})); code != http.StatusOK {
		t.Fatalf("a document of exactly the limit was refused: %d", code)
	}

	off := newAITestEnv(t, func(c *Config) { c.AIDir = "" })
	if code, _ := off.put(body); code != http.StatusServiceUnavailable {
		t.Fatalf("without the assistant directory: %d", code)
	}
	if _, err := os.Stat(filepath.Join(off.dir, "ai.json")); err == nil {
		t.Fatal("a refused save wrote ai.json")
	}
	if got := off.view(t).Docs.Soul; got != aiDocLF(aiDocSoulDefault) {
		t.Fatalf("without the directory the view holds %q", got)
	}
}

func TestAIDocsLongDocumentIsCut(t *testing.T) {
	e := newAITestEnv(t, nil)
	if err := os.MkdirAll(filepath.Dir(e.aiDocsFile("soul.md")), 0o700); err != nil {
		t.Fatal(err)
	}
	// One ASCII letter then two-byte letters: the limit falls inside a character.
	text := "a" + strings.Repeat("я", aiDocMaxBytes)
	if err := os.WriteFile(e.aiDocsFile("soul.md"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	block := e.s.aiPersonaBlock(nil)
	start := strings.Index(block, "## Soul\n") + len("## Soul\n")
	end := strings.Index(block, "\n\n## Memory")
	section := block[start:end]
	if !strings.HasSuffix(section, aiDocCutNote) {
		t.Fatalf("no note of the cut at the end of the section: ...%q", section[len(section)-80:])
	}
	body := strings.TrimSuffix(section, aiDocCutNote)
	if len(body) > aiDocMaxBytes || !utf8.ValidString(body) || len(body) < aiDocMaxBytes-utf8.UTFMax {
		t.Fatalf("the cut text is %d bytes, valid %v", len(body), utf8.ValidString(body))
	}
	if !strings.Contains(block, "## Memory\n") || !strings.Contains(block, "## Expertise\n") {
		t.Fatal("a long document pushed out the next headings")
	}
	if got := aiDocCut(strings.Repeat("a", aiDocMaxBytes)); strings.Contains(got, aiDocCutNote) {
		t.Fatal("a document of exactly the limit was marked as cut")
	}
}

func TestAIDocsUnreadableFileIsNotReplacedByTheDefault(t *testing.T) {
	e := newAITestEnv(t, nil)
	// A directory in place of the file: ReadFile fails with something other than "not exist".
	if err := os.MkdirAll(e.aiDocsFile("soul.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	block := e.s.aiPersonaBlock(nil)
	if strings.Contains(block, aiDocLF(aiDocSoulDefault)) || !strings.Contains(block, "## Soul\n"+aiDocEmptyNote) {
		t.Fatalf("a damaged document was replaced:\n%s", block)
	}
	if !strings.Contains(e.log.text(), "assistant document cannot be read") {
		t.Fatal("the unreadable document was not logged")
	}
}

func TestAIDocsPersonaBlockCleansSecrets(t *testing.T) {
	e := newAITestEnv(t, nil)
	e.mustPut(t, aiDocsBody(t, map[string]string{"memory": "key is " + aiCanary + " and " + aiCanary2}))
	block := e.s.aiPersonaBlock([]string{aiCanary2})
	if strings.Contains(block, aiCanary) || strings.Contains(block, aiCanary2) {
		t.Fatalf("a secret reached the context:\n%s", block)
	}
}

func TestAssistantTurnCarriesThreeDocuments(t *testing.T) {
	stub := aiScriptStub(t, aiAnthText("ok", 5, 5))
	e := newTurnEnv(t, stub, aiTurnSettings)
	const note = "Партнёрская сеть Alpha платит по субботам"
	if code, body := e.do(t, http.MethodPut, "/api/ai/settings", aiDocsBody(t, map[string]string{
		"soul": "Отвечай как штурман.", "memory": note, "expertise": "Кампания — это верх, поток — внутри.",
	})); code != http.StatusOK {
		t.Fatalf("save %d %s", code, body)
	}
	chat := e.newChat(t)
	resp := e.send(t, chat, `{"text":"what happens?","lang":"ru"}`)
	defer resp.Body.Close()
	events, _ := readSSE(t, resp.Body)
	if lastEvent(t, events).name != "done" {
		t.Fatalf("events %v", eventNames(events))
	}
	system := aiSystemOf(t, stub)
	for _, want := range []string{"## Soul\nОтвечай как штурман.", "## Memory\n" + note, "## Expertise\nКампания — это верх, поток — внутри."} {
		if !strings.Contains(system, want) {
			t.Errorf("the system prompt lacks %q", want)
		}
	}
	if strings.Index(system, "## Soul") > strings.Index(system, "## Memory") || strings.Index(system, "## Memory") > strings.Index(system, "## Expertise") {
		t.Error("the documents are out of order")
	}
	if !strings.HasSuffix(system, aiLangPrompt["ru"]) {
		t.Error("the language instruction is no longer the last of the prompt")
	}
}

func TestAssistantTurnReadsDocumentsAgainEveryTurn(t *testing.T) {
	stub := aiScriptStub(t, aiAnthText("ok", 5, 5), aiAnthText("ok", 5, 5))
	e := newTurnEnv(t, stub, aiTurnSettings)
	chat := e.newChat(t)
	for i, text := range []string{"first note", "second note"} {
		if code, body := e.do(t, http.MethodPut, "/api/ai/settings", aiDocsBody(t, map[string]string{"memory": text})); code != http.StatusOK {
			t.Fatalf("save %d %s", code, body)
		}
		resp := e.send(t, chat, `{"text":"hello"}`)
		readSSE(t, resp.Body)
		resp.Body.Close()
		var req map[string]any
		_ = json.Unmarshal(stub.req(i).Body, &req)
		if system, _ := req["system"].(string); !strings.Contains(system, "## Memory\n"+text) {
			t.Errorf("turn %d does not carry %q", i+1, text)
		}
	}
}
