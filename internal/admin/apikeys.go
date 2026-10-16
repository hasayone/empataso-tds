package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/egerkuzma/kuztds/internal/security"
)

// apiKeyCreated is the body of a successful POST /api/api-keys.
//
// Key is the raw secret and is returned only here. Item is the public record:
// its hash is cleared. There is no error value; failures use the HTTP status.
type apiKeyCreated struct {
	Key  string          `json:"key"`
	Item security.APIKey `json:"item"`
}

// loadAPIKeys reads Config.APIKeysFile into s.keys.
//
// An empty path leaves s.keys nil, which disables the key routes and the
// Api-Key header. A missing file becomes an empty set and is not an error.
// Any other read or JSON error is logged and replaced with an empty set so
// the process still starts. The secret is not logged. There is no error
// return: New has already accepted the password.
func (s *Server) loadAPIKeys() {
	if s.cfg.APIKeysFile == "" {
		return
	}
	ks, err := security.LoadKeySet(s.cfg.APIKeysFile)
	if err != nil {
		s.logWarn("api keys file not loaded", "err", err)
		s.keys = security.NewKeySet()
		return
	}
	s.keys = ks
}

// apiKeysEnabled reports whether key storage is configured.
//
// An empty APIKeysFile or a nil set means the feature is off. There is no error.
func (s *Server) apiKeysEnabled() bool {
	return s.cfg.APIKeysFile != "" && s.keys != nil
}

// registerAPIKeys mounts list, create and delete for /api/api-keys.
//
// Each route is behind auth and cookieOnly, so an Api-Key session cannot
// manage keys. s is the server, mux receives the three routes. There is no error.
func registerAPIKeys(s *Server, mux *http.ServeMux) {
	mux.Handle("GET /api/api-keys", s.auth(cookieOnly(http.HandlerFunc(s.handleAPIKeysList))))
	mux.Handle("POST /api/api-keys", s.auth(cookieOnly(http.HandlerFunc(s.handleAPIKeysCreate))))
	mux.Handle("DELETE /api/api-keys/{id}", s.auth(cookieOnly(http.HandlerFunc(s.handleAPIKeyDelete))))
}

// cookieOnly rejects a session opened with the Api-Key header.
//
// next runs for a cookie session. An API session gets 403 "cookie session
// required" and next is not called. A missing session has API false, so next
// still runs; auth is what rejects that request. There is no error return.
func cookieOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessionFrom(r.Context()).API {
			writeErr(w, http.StatusForbidden, "cookie session required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authAPIKey authenticates r by the Api-Key header and calls next.
//
// The header is already non-empty. When keys are enabled and Verify accepts
// the secret for ScopeAdmin, next runs with a context session: User is
// "apikey:" plus the stored prefix, and API is true. CSRF is not checked.
// A disabled set, an unknown secret, a revoked key, or a key without
// ScopeAdmin answers 401 and does not read the cookie. The login limiter is
// not consulted. The header and the secret are not logged. There is no error return.
func (s *Server) authAPIKey(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if !s.apiKeysEnabled() {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	k, ok := s.keys.Verify(r.Header.Get("Api-Key"), security.ScopeAdmin)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	sess := security.Session{User: "apikey:" + k.Prefix, API: true}
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
}

// handleAPIKeysList writes every key with the hash removed.
//
// When keys are disabled the body is an empty array, not null. The request
// is already a cookie session. There is no error status: List does not fail.
func (s *Server) handleAPIKeysList(w http.ResponseWriter, _ *http.Request) {
	if !s.apiKeysEnabled() {
		writeJSON(w, http.StatusOK, []security.APIKey{})
		return
	}
	writeJSON(w, http.StatusOK, s.keys.List())
}

// handleAPIKeysCreate adds a key and returns the raw secret once.
//
// The body is {"name","scopes"}. Generate's errors answer 400 with the error
// text. A disabled set answers 503 "api keys disabled". After Generate the set
// is saved; a save error answers 500 and the set is reloaded from disk so
// memory matches the file. The raw secret is not logged. r is the cookie session.
func (s *Server) handleAPIKeysCreate(w http.ResponseWriter, r *http.Request) {
	if !s.apiKeysEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "api keys disabled")
		return
	}
	var body struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	raw, k, err := s.keys.Generate(body.Name, body.Scopes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.persistKeys(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, apiKeyCreated{Key: raw, Item: k.Public()})
}

// handleAPIKeyDelete revokes the key named by the id path segment.
//
// A disabled set answers 503. An unknown or already revoked id answers 404.
// After Revoke the set is saved; a save error answers 500 and the set is
// reloaded from disk. Success answers 204 with an empty body. r is the cookie
// session. The secret is not logged.
func (s *Server) handleAPIKeyDelete(w http.ResponseWriter, r *http.Request) {
	if !s.apiKeysEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "api keys disabled")
		return
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if !s.keys.Revoke(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.persistKeys(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// persistKeys writes the set to APIKeysFile.
//
// The caller holds s.keyMu. A write error is returned to the handler and the
// set is reloaded from the same path so memory matches disk. A reload error
// is logged and not returned. The secret is not logged.
func (s *Server) persistKeys() error {
	if err := s.keys.Save(s.cfg.APIKeysFile); err != nil {
		s.logWarn("api key file save failed", "err", err)
		if rerr := s.keys.Reload(s.cfg.APIKeysFile); rerr != nil {
			s.logWarn("api key file reload failed", "err", rerr)
		}
		return err
	}
	return nil
}
