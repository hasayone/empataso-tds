package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/toon-format/toon-go"
)

const (
	// toonContentType is the Content-Type of an answer in TOON (Token-Oriented Object Notation).
	toonContentType = "text/toon; charset=utf-8"

	// toonMedia is the media type a client lists in Accept to get TOON.
	toonMedia = "text/toon"

	// formatParam is the query parameter that picks the answer format: json or toon.
	formatParam = "format"
)

// toonWriter marks a response whose JSON answers writeJSON sends as TOON. It changes nothing
// else: every other write passes through to the wrapped writer.
type toonWriter struct {
	http.ResponseWriter
}

// Flush flushes the wrapped writer when it supports it.
func (w *toonWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (w *toonWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// toonFormat marks the responses of API requests that ask for TOON, so writeJSON answers in it.
// next is the rest of the chain. A request outside /api passes unchanged.
func toonFormat(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) && wantsTOON(r) {
			w = &toonWriter{ResponseWriter: w}
		}
		next.ServeHTTP(w, r)
	})
}

// wantsTOON reports whether r asks for TOON. format=toon in the query asks for it and
// format=json refuses it whatever Accept says; any other value of format is ignored. Without
// either, an Accept header that lists text/toon with a non-zero quality asks for it.
func wantsTOON(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(formatParam))) {
	case "toon":
		return true
	case "json":
		return false
	}
	return acceptsTOON(r.Header.Get("Accept"))
}

// acceptsTOON reports whether the Accept header value accept lists text/toon without q=0.
func acceptsTOON(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || !strings.EqualFold(media, toonMedia) {
			continue
		}
		if q, err := strconv.ParseFloat(params["q"], 64); err == nil && q <= 0 {
			continue
		}
		return true
	}
	return false
}

// toonRequested reports whether w, or a writer it wraps, was marked by toonFormat.
func toonRequested(w http.ResponseWriter) bool {
	for w != nil {
		if _, ok := w.(*toonWriter); ok {
			return true
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = u.Unwrap()
	}
	return false
}

// toonFromJSON converts one JSON document to TOON by the toon-format specification.
//
// Object keys keep their order in data. An integer that does not fit int64, or lies beyond
// ±(2^53−1), becomes a quoted decimal string so no digit is lost; other numbers keep their value.
// It fails when data is not exactly one JSON value.
func toonFromJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := toonValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("toon: more than one JSON value")
	}
	return toon.Marshal(v)
}

// toonValue reads the next JSON value of dec as a value toon.Marshal encodes in the same order:
// toon.Object for an object, []any for an array and the primitive otherwise.
func toonValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			return toonObject(dec)
		}
		if t == '[' {
			return toonArray(dec)
		}
		return nil, errors.New("toon: unexpected " + t.String())
	case json.Number:
		return toonNumber(t), nil
	}
	return tok, nil
}

// toonObject reads the members of an object whose opening brace dec has already read.
func toonObject(dec *json.Decoder) (toon.Object, error) {
	obj := toon.Object{Fields: []toon.Field{}}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return obj, err
		}
		key, _ := tok.(string)
		val, err := toonValue(dec)
		if err != nil {
			return obj, err
		}
		obj.Fields = append(obj.Fields, toon.Field{Key: key, Value: val})
	}
	_, err := dec.Token()
	return obj, err
}

// toonArray reads the elements of an array whose opening bracket dec has already read.
func toonArray(dec *json.Decoder) ([]any, error) {
	arr := []any{}
	for dec.More() {
		val, err := toonValue(dec)
		if err != nil {
			return arr, err
		}
		arr = append(arr, val)
	}
	_, err := dec.Token()
	return toonTable(arr), err
}

// toonTable makes an array of flat records fit the TOON table form name[N]{a,b}:. When every
// element of arr is an object whose values are primitives or lists of primitives, each record gets
// the union of all keys in first-seen order, a missing key becomes null and a list of primitives
// becomes its compact JSON text, so ["ru","kz"] is the cell "[\"ru\",\"kz\"]". Any other array,
// including records with a nested object or a list of objects, is returned unchanged.
func toonTable(arr []any) []any {
	var keys []string
	seen := map[string]bool{}
	for _, el := range arr {
		obj, ok := el.(toon.Object)
		if !ok {
			return arr
		}
		for _, f := range obj.Fields {
			switch v := f.Value.(type) {
			case toon.Object:
				return arr
			case []any:
				if !toonScalars(v) {
					return arr
				}
			}
			if !seen[f.Key] {
				seen[f.Key] = true
				keys = append(keys, f.Key)
			}
		}
	}
	if len(keys) == 0 {
		return arr
	}
	out := make([]any, len(arr))
	for i, el := range arr {
		vals := map[string]any{}
		for _, f := range el.(toon.Object).Fields {
			vals[f.Key] = f.Value
		}
		row := toon.Object{Fields: make([]toon.Field, len(keys))}
		for j, k := range keys {
			v := vals[k]
			if list, ok := v.([]any); ok {
				text, _ := json.Marshal(list)
				v = string(text)
			}
			row.Fields[j] = toon.Field{Key: k, Value: v}
		}
		out[i] = row
	}
	return out
}

// toonScalars reports whether list holds only primitives: no objects and no nested lists.
func toonScalars(list []any) bool {
	for _, v := range list {
		switch v.(type) {
		case toon.Object, []any:
			return false
		}
	}
	return true
}

// toonAPIDescription is the part of the OpenAPI info.description that explains the TOON format.
const toonAPIDescription = "Every JSON answer, errors included, can be sent as TOON (Token-Oriented Object Notation, " +
	"media type text/toon) to save LLM context tokens: pass ?format=toon in the query (POST, PUT, PATCH and DELETE too) " +
	"or the header Accept: text/toon. ?format=json forces JSON whatever Accept says; any other format value is ignored. " +
	"JSON stays the default. A TOON answer has Content-Type: text/toon; charset=utf-8 and carries the same fields, " +
	"rights and hidden secrets as the JSON one; an array of records becomes a table " +
	"(name[N]{field1,field2}: and one row per line). In a table a field the JSON record omits is null, and a list " +
	"of plain values is one cell holding its JSON text (\"[\\\"ru\\\",\\\"kz\\\"]\"); records with nested objects are " +
	"not changed and can be longer than JSON. File downloads, CSV exports and event streams are not converted."

// toonDocsIntro is the TOON section of the Markdown reference (/api/docs.md, /llms.txt) per language.
var toonDocsIntro = map[string]string{
	"en": "\n## TOON format for AI\n\nFor AI you can get the data as `text/toon` by passing `?format=toon` (or the header " +
		"`Accept: text/toon`); `?format=json` always answers JSON, JSON is the default. Errors come in TOON too " +
		"(`code: not_found`, `error: not found`). An array of records becomes a table `name[N]{a,b}:` with one row " +
		"per line: a field the JSON record omits is `null`, and a list of plain values is one cell holding its JSON " +
		"text (`\"[\\\"ru\\\",\\\"kz\\\"]\"`). Catalogs, journals and postbacks get 31–53% shorter; records with nested " +
		"objects (campaigns, reports) are not changed and can be longer than JSON. CSV exports, downloads and event " +
		"streams do not change.\n",
	"ru": "\n## Формат TOON для AI\n\nДля AI можно выводить информацию в формате `text/toon`, передав `?format=toon` (или " +
		"заголовок `Accept: text/toon`); `?format=json` всегда отвечает JSON, JSON — по умолчанию. Ошибки тоже в TOON " +
		"(`code: not_found`, `error: not found`). Массив записей становится таблицей `name[N]{a,b}:` по строке на " +
		"запись: поле, которого нет в JSON-записи, — `null`, а список простых значений — одна ячейка с его JSON-текстом " +
		"(`\"[\\\"ru\\\",\\\"kz\\\"]\"`). Каталоги, журналы и постбеки короче на 31–53%; записи с вложенными объектами " +
		"(кампании, отчёты) не меняются и могут быть длиннее JSON. Выгрузки CSV, скачивание файлов и потоки событий " +
		"не меняются.\n",
	"uk": "\n## Формат TOON для AI\n\nДля AI можна виводити інформацію у форматі `text/toon`, передавши `?format=toon` (або " +
		"заголовок `Accept: text/toon`); `?format=json` завжди відповідає JSON, JSON — за замовчуванням. Помилки теж у TOON " +
		"(`code: not_found`, `error: not found`). Масив записів стає таблицею `name[N]{a,b}:` по рядку на запис: " +
		"поле, якого немає в JSON-записі, — `null`, а список простих значень — одна клітинка з його JSON-текстом " +
		"(`\"[\\\"ru\\\",\\\"kz\\\"]\"`). Каталоги, журнали й постбеки коротші на 31–53%; записи з вкладеними об'єктами " +
		"(кампанії, звіти) не змінюються і можуть бути довшими за JSON. Вивантаження CSV, завантаження файлів і потоки " +
		"подій не змінюються.\n",
}

// toonOpenAPIParam returns the shared OpenAPI parameter object of the format query parameter.
func toonOpenAPIParam() map[string]any {
	return map[string]any{
		"name": formatParam, "in": "query", "required": false,
		"description": "Answer format: json (default) or toon (text/toon, fewer tokens for an LLM). " +
			"Accept: text/toon does the same.",
		"schema": map[string]any{"type": "string", "enum": []any{"json", "toon"}, "default": "json"},
	}
}

// toonExample returns the TOON form of the JSON sample v of an OpenAPI response. ok is false when
// v cannot be encoded.
func toonExample(v any) (text string, ok bool) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	out, err := toonFromJSON(data)
	if err != nil {
		return "", false
	}
	return string(out), true
}

// aiToolText returns the text of a read tool result raw (compact JSON) for the model: its TOON
// form when that is shorter, raw itself otherwise. Tables of uniform records shrink the most; a
// small or deeply nested object usually stays JSON.
func aiToolText(raw []byte) string {
	if out, err := toonFromJSON(raw); err == nil && len(out) < len(raw) {
		return string(out)
	}
	return string(raw)
}

// toonNumber returns an integer literal n as int64, or as its text when it overflows int64, and
// any other number unchanged.
func toonNumber(n json.Number) any {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		return n
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	return s
}
