package main

import (
	"encoding/json"
	"net/http"

	"github.com/egerkuzma/emtds/internal/config"
)

// versionHandler serves GET /internal/version with the binary's build
// metadata as JSON. Any other method returns 405. The response always
// carries Cache-Control: no-store to prevent proxy or CDN caching.
func versionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		b := config.Build()
		data, err := json.Marshal(b)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	}
}
