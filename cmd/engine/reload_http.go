package main

import (
	"encoding/json"
	"net/http"
)

// reloadHandler serves POST /internal/reload: the admin calls it right after it saves the
// groups file, so a renamed or replaced campaign is live on the next click instead of at the
// next tick of EMTDS_RELOAD_INTERVAL.
//
// groups is the watcher of the live campaign set; nil means the engine runs on the built-in
// demo group and there is nothing to reload. books are the catalogs reloaded together with
// the groups; a nil book is skipped.
// The body is JSON {"groups":bool}, true when the campaign set was swapped. Any method other
// than POST is 405. A groups file that cannot be read or parsed is 500 {"error":"reload failed"}:
// the previous campaign set keeps serving and the ticker retries. The path of the file is
// not written into the answer. The route sits under /internal/, which nginx does not publish.
func reloadHandler(groups *groupsWatcher, books ...interface{ Reload() error }) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		for _, b := range books {
			if b != nil {
				_ = b.Reload()
			}
		}
		swapped := false
		if groups != nil {
			var err error
			if swapped, err = groups.reload(); err != nil {
				groups.log.Warn("groups reload on request failed, keeping the previous config", "err", err)
				writeReloadJSON(w, http.StatusInternalServerError, map[string]any{"error": "reload failed"})
				return
			}
		}
		writeReloadJSON(w, http.StatusOK, map[string]any{"groups": swapped})
	}
}

// writeReloadJSON writes body as JSON with the given status and no-store caching.
// It does not return an error: a write failure is the caller's closed connection.
func writeReloadJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
