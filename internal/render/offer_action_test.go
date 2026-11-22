package render

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOfferActionHTML checks that an HTML action is the typed page with status 200
// and text/html, not a wrapped document and not a redirect.
func TestOfferActionHTML(t *testing.T) {
	res := Do("show_html", "<p>hi</p>", Options{})
	if res.Status != http.StatusOK || !strings.HasPrefix(res.ContentType, "text/html") {
		t.Fatalf("status %d type %q", res.Status, res.ContentType)
	}
	if string(res.Body) != "<p>hi</p>" || strings.Contains(string(res.Body), "<!DOCTYPE") {
		t.Fatalf("body %q", res.Body)
	}
	w := httptest.NewRecorder()
	res.Write(w)
	if w.Code != http.StatusOK || w.Header().Get("Location") != "" || w.Body.String() != "<p>hi</p>" {
		t.Fatalf("write %d %q %q", w.Code, w.Header().Get("Location"), w.Body.String())
	}
}
