package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

func TestWriteErrCodeTable(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusBadRequest:            "bad_request",
		http.StatusUnauthorized:          "unauthorized",
		http.StatusForbidden:             "forbidden",
		http.StatusNotFound:              "not_found",
		http.StatusConflict:              "conflict",
		http.StatusRequestEntityTooLarge: "too_large",
		http.StatusTooManyRequests:       "too_many_requests",
		http.StatusBadGateway:            "bad_gateway",
		http.StatusServiceUnavailable:    "unavailable",
		http.StatusInternalServerError:   "internal",
		http.StatusGatewayTimeout:        "internal",
		http.StatusUnprocessableEntity:   "bad_request",
		http.StatusMethodNotAllowed:      "method_not_allowed",
	} {
		rec := httptest.NewRecorder()
		writeErr(rec, status, "boom")
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != status || body["code"] != want || body["error"] != "boom" {
			t.Errorf("status %d: code %d body %v, want code %q", status, rec.Code, body, want)
		}
	}
}

// TestWriteRetryLater checks that a 429 answer carries the wait rounded up to whole seconds, at least one,
// in Retry-After and in retry_after of the body and of params, with the stable code too_many_requests.
func TestWriteRetryLater(t *testing.T) {
	for _, c := range []struct {
		wait time.Duration
		want int
	}{{0, 1}, {300 * time.Millisecond, 1}, {time.Minute, 60}, {179*time.Second + time.Millisecond, 180}} {
		rec := httptest.NewRecorder()
		writeRetryLater(rec, "rate_limited", c.wait)
		var body struct {
			Error      string         `json:"error"`
			Code       string         `json:"code"`
			RetryAfter int            `json:"retry_after"`
			Params     map[string]int `json:"params"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != strconv.Itoa(c.want) ||
			body.Error != "rate_limited" || body.Code != "too_many_requests" || body.RetryAfter != c.want || body.Params["retry_after"] != c.want {
			t.Errorf("wait %v: %d %q %+v, want %d s", c.wait, rec.Code, rec.Header().Get("Retry-After"), body, c.want)
		}
	}
}

func TestWriteErrSpecialMessages(t *testing.T) {
	for _, c := range []struct{ msg, want string }{
		{"action required: logs_delete", "action_required"},
		{"cookie session required", "cookie_required"},
		{"forbidden", "forbidden"},
	} {
		rec := httptest.NewRecorder()
		writeErr(rec, http.StatusForbidden, c.msg)
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != c.want || body["error"] != c.msg {
			t.Errorf("%q: %v, want code %q", c.msg, body, c.want)
		}
	}
}

func TestWriteErrWriteNCKeepsCode(t *testing.T) {
	rec := httptest.NewRecorder()
	writeNC(rec, http.StatusConflict, "pending", map[string]any{"code": "evil", "pending": true})
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "conflict" || body["error"] != "pending" || body["pending"] != true {
		t.Fatalf("body %v", body)
	}
}

func TestWriteErrNoExtraNamedCode(t *testing.T) {
	src := []string{"namecheap_register.go", "namecheap_pending.go"}
	for _, name := range src {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"code":`) {
			t.Errorf("%s passes an extra field named code to writeNC", name)
		}
	}
}

func TestWriteErrOnKeyRoutes(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	_, plain := rig.newKey(t, security.KeyAccess{})
	code, body := rig.call(t, plain, http.MethodGet, "/api/me", nil)
	if code != http.StatusForbidden || !strings.Contains(body, `"code":"cookie_required"`) {
		t.Fatalf("cookie route: %d %s", code, body)
	}
	code, body = rig.call(t, plain, http.MethodDelete, "/api/logs?group=g1", nil)
	if code != http.StatusForbidden || !strings.Contains(body, `"code":"action_required"`) {
		t.Fatalf("action route: %d %s", code, body)
	}
	code, body = rig.call(t, spoilKey(plain), http.MethodGet, "/api/offers", nil)
	if code != http.StatusUnauthorized || !strings.Contains(body, `"code":"unauthorized"`) ||
		!strings.Contains(body, `"error":"unauthorized"`) {
		t.Fatalf("bad key: %d %s", code, body)
	}
}

func TestAPIUnknownRouteAnswersJSON(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	for _, c := range []struct {
		method, path string
		status       int
		code, allow  string
	}{
		{http.MethodGet, "/api", http.StatusNotFound, "not_found", ""},
		{http.MethodPost, "/api", http.StatusNotFound, "not_found", ""},
		{http.MethodGet, "/api/", http.StatusNotFound, "not_found", ""},
		{http.MethodGet, "/api/nope", http.StatusNotFound, "not_found", ""},
		{http.MethodGet, "/api/offers/1/nope/deeper", http.StatusNotFound, "not_found", ""},
		{http.MethodPatch, "/api/health", http.StatusMethodNotAllowed, "method_not_allowed", "GET"},
		{http.MethodDelete, "/api/me", http.StatusMethodNotAllowed, "method_not_allowed", "GET"},
	} {
		req, err := http.NewRequest(c.method, rig.srv.URL+c.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rig.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]string
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		if resp.StatusCode != c.status || body["code"] != c.code || body["error"] == "" {
			t.Errorf("%s %s: %d %v", c.method, c.path, resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s %s: content type %q", c.method, c.path, ct)
		}
		if allow := resp.Header.Get("Allow"); c.allow != "" && !strings.Contains(allow, c.allow) {
			t.Errorf("%s %s: Allow %q, want %q", c.method, c.path, allow, c.allow)
		}
	}
}

func TestNonAPIUnknownPathKeepsPlainAnswer(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	resp, err := rig.srv.Client().Get(rig.srv.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestAPIKnownRoutesAreNotRewritten(t *testing.T) {
	rig := newKeyRig(t, richStats{})
	resp, err := rig.srv.Client().Get(rig.srv.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health %d", resp.StatusCode)
	}
	if code, _ := rig.asAdmin(t, http.MethodGet, "/api/offers/none", nil); code != http.StatusNotFound {
		t.Fatalf("handler 404 became %d", code)
	}
}

func TestWriteErrKeepsAssistantCodes(t *testing.T) {
	for status, code := range map[int]string{
		http.StatusConflict:              aiErrBusy,
		http.StatusTooManyRequests:       aiErrQuota,
		http.StatusInsufficientStorage:   aiErrDiskFull,
		http.StatusBadRequest:            aiErrBadModel,
		http.StatusServiceUnavailable:    aiErrDisabled,
		http.StatusRequestEntityTooLarge: aiErrChatFull,
		http.StatusNotAcceptable:         "ai_not_acceptable",
		http.StatusBadGateway:            aiErrStepsLimit,
		http.StatusGatewayTimeout:        aiErrTimeout,
		http.StatusUnprocessableEntity:   aiErrInputTooLarge,
	} {
		rec := httptest.NewRecorder()
		writeErr(rec, status, newAIError(status, code, "text").msg)
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != status || body["code"] != code {
			t.Errorf("status %d: code %d body %v, want code %q", status, rec.Code, body, code)
		}
	}
}
