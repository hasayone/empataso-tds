package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAPIKeyHeaderPrefixes requires the Api-Key header to accept a stored em_ key
// and to refuse a stored key under any other prefix, including the retired
// two-letter one (assembled from parts), with 401.
func TestAPIKeyHeaderPrefixes(t *testing.T) {
	raws := map[string]int{
		"em_" + strings.Repeat("a", 40):      http.StatusOK,
		"k" + "z_" + strings.Repeat("b", 40): http.StatusUnauthorized,
		"xx_" + strings.Repeat("c", 40):      http.StatusUnauthorized,
	}
	var items []string
	i := 0
	for raw := range raws {
		sum := sha256.Sum256([]byte(raw))
		i++
		items = append(items, `{"id":"aaaaaaaaaaa`+string(rune('0'+i))+`","name":"k`+string(rune('0'+i))+
			`","prefix":"12345678","hash":"`+hex.EncodeToString(sum[:])+
			`","scopes":["admin"],"created_at":"2026-01-01T00:00:00Z"}`)
	}
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte("["+strings.Join(items, ",")+"]"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := startKeyServer(t, path, nil)
	for raw, want := range raws {
		if code, _ := doReq(t, nil, http.MethodGet, srv.URL+"/api/openapi.json", "", raw, ""); code != want {
			t.Errorf("%s key: status %d, want %d", raw[:3], code, want)
		}
	}
}
