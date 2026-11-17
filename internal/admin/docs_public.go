package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
)

const (
	// docsCacheControl lets browsers and proxies keep a public docs answer for five minutes.
	docsCacheControl = "public, max-age=300"
	// docsLimitPrefix namespaces the per-address entries of the public docs in keyLim.
	docsLimitPrefix = "docs:"
)

// docsMu serialises writes of the docs settings file.
var docsMu sync.Mutex

// docsSettings is the docs settings file: Public opens the API reference without sign-in.
type docsSettings struct {
	Public bool `json:"public"`
}

// publicDocs holds the public reference built once per process: the full OpenAPI document,
// the Postman collection of the same routes, their content tags and the Markdown text in each panel language.
type publicDocs struct {
	openAPI    []byte
	tag        string
	postman    []byte
	postmanTag string
	md         map[string][]byte
	mdTag      map[string]string
}

// docsLangs lists the languages of the Markdown reference; the first is the default.
var docsLangs = []string{"en", "ru", "uk"}

// docsIntro is the head of the Markdown reference per language.
var docsIntro = map[string]string{
	"en": "# emtds admin API\n\nThe base address is the address this text was served from. Every call sends the key in the " +
		"`Api-Key: em_...` header. A key reads " +
		"the sections the administrator opened for it and writes only where write access is ticked; dangerous " +
		"actions need their own tick. Answers are JSON; errors are `{\"error\", \"code\"}`. The machine-readable " +
		"document is `/api/docs/openapi.json`. The Postman collection of the same routes is `/api/docs/postman.json`.\n",
	"ru": "# API админки emtds\n\nБазовый адрес — адрес, с которого отдан этот текст. Каждый вызов передаёт ключ в заголовке " +
		"`Api-Key: em_...`. Ключ " +
		"читает разделы, которые ему открыл администратор, и пишет только там, где отмечена запись; опасные действия " +
		"требуют своей галочки. Ответы в JSON, ошибки — `{\"error\", \"code\"}`. Машиночитаемый документ — " +
		"`/api/docs/openapi.json`. Коллекция Postman тех же маршрутов — `/api/docs/postman.json`.\n",
	"uk": "# API адмінки emtds\n\nБазова адреса — адреса, з якої віддано цей текст. Кожен виклик передає ключ у заголовку " +
		"`Api-Key: em_...`. Ключ " +
		"читає розділи, які йому відкрив адміністратор, і пише лише там, де позначено запис; небезпечні дії " +
		"потребують своєї галочки. Відповіді в JSON, помилки — `{\"error\", \"code\"}`. Машинночитаний документ — " +
		"`/api/docs/openapi.json`. Колекція Postman тих самих маршрутів — `/api/docs/postman.json`.\n",
}

// docsWords holds the labels of the Markdown reference per language.
var docsWords = map[string]map[string]string{
	"en": {"query": "Query parameters", "required": "required", "body": "Request body", "response": "Response",
		"empty": "Empty body.", "action": "Key tick", "cookie": "Browser session only: a key gets 403.",
		"public": "No key needed.", "admin": "Administrators only."},
	"ru": {"query": "Параметры запроса", "required": "обязательный", "body": "Тело запроса", "response": "Ответ",
		"empty": "Пустое тело.", "action": "Галочка ключа", "cookie": "Только сессия браузера: ключ получает 403.",
		"public": "Ключ не нужен.", "admin": "Только администраторы."},
	"uk": {"query": "Параметри запиту", "required": "обов'язковий", "body": "Тіло запиту", "response": "Відповідь",
		"empty": "Порожнє тіло.", "action": "Галочка ключа", "cookie": "Лише сесія браузера: ключ отримує 403.",
		"public": "Ключ не потрібен.", "admin": "Лише адміністратори."},
}

// readDocsSettings loads the docs settings from path.
// An empty path, a missing file or an empty file yields the default: public docs on.
// It returns an error when the file cannot be read or holds invalid JSON.
func readDocsSettings(path string) (docsSettings, error) {
	def := docsSettings{Public: true}
	if path == "" {
		return def, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return def, nil
	}
	if err != nil {
		return docsSettings{}, err
	}
	if strings.TrimSpace(string(b)) == "" {
		return def, nil
	}
	var v docsSettings
	if err := json.Unmarshal(b, &v); err != nil {
		return docsSettings{}, err
	}
	return v, nil
}

// docsOpen reports whether the public docs are on. A settings file that cannot be read
// closes them and is logged.
func (s *Server) docsOpen() bool {
	v, err := readDocsSettings(s.cfg.DocsFile)
	if err != nil {
		s.logWarn("docs settings", "err", err)
		return false
	}
	return v.Public
}

// handleDocsSettingsGet answers {"public": bool}. A file that cannot be read is 500.
func (s *Server) handleDocsSettingsGet(w http.ResponseWriter, r *http.Request) {
	v, err := readDocsSettings(s.cfg.DocsFile)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleDocsSettingsPut saves the body {"public": bool} and answers it back.
// It answers 400 for a bad body, 503 without a settings file and 500 when saving fails.
func (s *Server) handleDocsSettingsPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DocsFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "docs file not configured")
		return
	}
	var v docsSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	docsMu.Lock()
	err := writeFileAtomic(s.cfg.DocsFile, append(b, '\n'), 0o644)
	docsMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// docsGate wraps a public docs handler: it answers 404 while the public docs are off and
// 429 with Retry-After when the client address is over the per-address rate of keyLim.
// next runs otherwise. It does not return an error.
func (s *Server) docsGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.docsOpen() {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if s.keyLim != nil {
			release, den := s.keyLim.acquire(docsLimitPrefix+s.clientIP(r), false)
			if den != nil {
				w.Header().Set("Retry-After", strconv.Itoa(den.retry))
				writeErr(w, http.StatusTooManyRequests, den.msg)
				return
			}
			defer release()
		}
		next(w, r)
	}
}

// publicDocsBuilt returns the public reference, building it on first use. The routes are
// fixed once Handler has run, so one build serves the process.
func (s *Server) publicDocsBuilt() *publicDocs {
	s.docsOnce.Do(func() {
		doc := s.openAPIDocumentOf(func(routeRule) bool { return true })
		body, _ := json.Marshal(doc)
		pm, _ := json.Marshal(postmanCollection(doc))
		d := &publicDocs{openAPI: body, tag: contentTag(body), postman: pm, postmanTag: contentTag(pm), md: map[string][]byte{}, mdTag: map[string]string{}}
		patterns := append(slices.Clone(s.routes), s.public...)
		for _, lang := range docsLangs {
			md := []byte(docsMarkdown(patterns, lang))
			d.md[lang] = md
			d.mdTag[lang] = contentTag(md)
		}
		s.docs = d
	})
	return s.docs
}

// handleDocsPage serves the SPA, which paints the public reference at /docs.
func (s *Server) handleDocsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	serveUI(w, r)
}

// handleDocsOpenAPI answers the OpenAPI document of every route, with no session filter.
func (s *Server) handleDocsOpenAPI(w http.ResponseWriter, r *http.Request) {
	d := s.publicDocsBuilt()
	writeDocsBody(w, r, d.tag, "application/json; charset=utf-8", d.openAPI)
}

// handleDocsMarkdown answers the whole reference as one Markdown text for people and AI
// agents. The query lang (en, ru or uk) picks the language; anything else is English.
func (s *Server) handleDocsMarkdown(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	if !slices.Contains(docsLangs, lang) {
		lang = docsLangs[0]
	}
	d := s.publicDocsBuilt()
	writeDocsBody(w, r, d.mdTag[lang], "text/markdown; charset=utf-8", d.md[lang])
}

// writeDocsBody writes a cacheable public docs body with its ETag; a matching
// If-None-Match answers 304. w and r are the response and request, tag the content tag.
func writeDocsBody(w http.ResponseWriter, r *http.Request, tag, contentType string, body []byte) {
	w.Header().Set("Cache-Control", docsCacheControl)
	if answerETag(w, r, tag) {
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(body)
}

// docsMarkdown renders patterns as the Markdown reference in lang, grouped by docTags.
// Every route gets its summary, description, query parameters, body and answer samples
// and its access notes. It does not fail.
func docsMarkdown(patterns []string, lang string) string {
	bySection := map[string][]string{}
	for _, p := range patterns {
		_, muxPath, _ := strings.Cut(p, " ")
		tag := routeTag(muxPath)
		bySection[tag] = append(bySection[tag], p)
	}
	var b strings.Builder
	b.WriteString(docsIntro[lang])
	b.WriteString(toonDocsIntro[lang])
	for _, tag := range docTags {
		list := bySection[tag.Name]
		if len(list) == 0 {
			continue
		}
		slices.Sort(list)
		b.WriteString("\n## " + docsTagName(tag, lang) + "\n")
		for _, p := range list {
			writeDocsRoute(&b, p, lang)
		}
	}
	return b.String()
}

// docsTagName returns the section name of tag in lang.
func docsTagName(tag docTag, lang string) string {
	switch lang {
	case "ru":
		return tag.RU
	case "uk":
		return tag.UK
	}
	return tag.EN
}

// docsText returns the summary and description of doc in lang; an empty translation
// falls back to English.
func docsText(doc routeDoc, lang string) docText {
	t := docText{Summary: doc.Summary, Description: doc.Description}
	loc := map[string]docText{"ru": doc.RU, "uk": doc.UK}[lang]
	if loc.Summary != "" {
		t.Summary = loc.Summary
	}
	if loc.Description != "" {
		t.Description = loc.Description
	}
	return t
}

// writeDocsRoute appends the Markdown section of pattern in lang to b.
func writeDocsRoute(b *strings.Builder, pattern, lang string) {
	words := docsWords[lang]
	doc := routeDocs[pattern]
	rule := ruleOf(pattern)
	text := docsText(doc, lang)
	b.WriteString("\n### `" + convertedKey(pattern) + "`\n\n" + text.Summary + "\n")
	if text.Description != "" {
		b.WriteString("\n" + text.Description + "\n")
	}
	var notes []string
	if _, public := publicRoutes[pattern]; public {
		notes = append(notes, words["public"])
	}
	if rule.cookie {
		notes = append(notes, words["cookie"])
	}
	if rule.admin {
		notes = append(notes, words["admin"])
	}
	if rule.action != "" {
		notes = append(notes, words["action"]+": `"+rule.action+"`.")
	}
	if len(notes) > 0 {
		b.WriteString("\n" + strings.Join(notes, " ") + "\n")
	}
	if len(doc.Query) > 0 {
		b.WriteString("\n" + words["query"] + ":\n\n")
		for _, q := range doc.Query {
			desc := map[string]string{"ru": q.RU, "uk": q.UK}[lang]
			if desc == "" {
				desc = q.Description
			}
			kind := q.Type
			if q.Array {
				kind += "[]"
			}
			if q.Required {
				kind += ", " + words["required"]
			}
			b.WriteString("- `" + q.Name + "` (" + kind + ") — " + desc + "\n")
		}
	}
	if doc.Example != nil {
		b.WriteString("\n" + words["body"] + ":\n\n" + docsSample(doc.Example))
	}
	b.WriteString("\n" + words["response"] + " " + strconv.Itoa(doc.successStatus()) + ":\n\n")
	if doc.Response == nil {
		b.WriteString(words["empty"] + "\n")
	} else {
		b.WriteString(docsSample(doc.Response))
	}
}

// convertedKey returns "METHOD /path" for a mux pattern, with {name...} written {name}.
func convertedKey(pattern string) string {
	verb, path, _ := strings.Cut(pattern, " ")
	return verb + " " + pathParamPattern.ReplaceAllString(path, "{$1}")
}

// docsSample renders v as a fenced block: a string as plain text, anything else as indented JSON.
func docsSample(v any) string {
	if s, ok := v.(string); ok {
		return "```text\n" + strings.TrimRight(s, "\n") + "\n```\n"
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return "```json\n" + string(out) + "\n```\n"
}
