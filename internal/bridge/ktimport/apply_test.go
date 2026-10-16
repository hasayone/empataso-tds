package ktimport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplyMergeAndSimulate(t *testing.T) {
	var saved []Group
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/login":
			http.SetCookie(w, &http.Cookie{Name: "kuztds_admin", Value: "sess"})
			_ = json.NewEncoder(w).Encode(map[string]string{"csrf": "tok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/groups":
			_ = json.NewEncoder(w).Encode([]Group{{ID: "mine", Name: "Mine", Status: true}})
		case r.Method == http.MethodPut && r.URL.Path == "/api/lists/kt-demo.dat":
			if r.Header.Get("X-CSRF-Token") != "tok" {
				http.Error(w, "csrf", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"saved"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/groups":
			if r.Header.Get("X-CSRF-Token") != "tok" {
				http.Error(w, "csrf", http.StatusForbidden)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &saved); err != nil {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"status":"saved"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/simulate":
			var req struct {
				Group string `json:"group"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			selected, out := "-", ""
			if req.Group == "offer-es" {
				selected, out = "kt-11", "https://offer.example/p?subid=[CID]&c=[COUNTRY]"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"path": []map[string]string{{"selected": selected, "out": out}},
			})
		case strings.HasPrefix(r.URL.Path, "/api/affiliate-networks") || strings.HasPrefix(r.URL.Path, "/api/traffic-sources") || strings.HasPrefix(r.URL.Path, "/api/domains") || strings.HasPrefix(r.URL.Path, "/api/landings") || strings.HasPrefix(r.URL.Path, "/api/offers"):
			if r.Method == http.MethodGet {
				if r.URL.Path == "/api/offers" {
					_ = json.NewEncoder(w).Encode([]CatOffer{{ID: "101", Name: "old", URL: "https://offer.example/old", Status: true}})
					return
				}
				_ = json.NewEncoder(w).Encode([]any{})
				return
			}
			if r.Header.Get("X-CSRF-Token") != "tok" {
				http.Error(w, "csrf", http.StatusForbidden)
				return
			}
			calls = append(calls, r.Method+" "+r.URL.Path)
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	admin := NewAdmin(srv.URL, "admin", "local-secret")
	if err := admin.login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := admin.PutList(context.Background(), "kt-demo.dat", "1.1.1.1\n"); err != nil {
		t.Fatal(err)
	}
	incoming := MapAll(loadFixture(t))
	got, losses, err := admin.Apply(context.Background(), incoming)
	merged := got.Groups
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findGroup(merged, "mine"); !ok {
		t.Fatal("local group missing after apply")
	}
	if len(saved) != len(merged) {
		t.Fatalf("saved %d merged %d", len(saved), len(merged))
	}
	selected, out, err := admin.Simulate(context.Background(), merged, "offer-es", "ES")
	if err != nil {
		t.Fatal(err)
	}
	if selected != "kt-11" || !strings.Contains(out, "[CID]") {
		t.Fatalf("simulate %s %s", selected, out)
	}
	selected, _, err = admin.Simulate(context.Background(), merged, "weights", "ES")
	if err != nil {
		t.Fatal(err)
	}
	if selected != "-" {
		t.Fatalf("weight selected %s", selected)
	}
	if strings.Contains(lossesText(losses), "secret") {
		t.Fatal("secret in losses")
	}
	wantCalls := []string{
		"POST /api/affiliate-networks",
		"POST /api/affiliate-networks",
		"POST /api/traffic-sources",
		"POST /api/domains",
		"POST /api/landings",
		"PUT /api/offers/101",
		"POST /api/offers",
		"POST /api/offers",
		"POST /api/offers",
		"POST /api/offers",
		"POST /api/offers",
		"POST /api/offers",
	}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("catalog calls:\n%s", strings.Join(calls, "\n"))
	}
	weights, _ := findGroup(merged, "weights")
	if !weights.Status || !weights.Weight {
		t.Fatalf("weight group %+v", weights)
	}
}
