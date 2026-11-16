package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/security"
)

// msgKeyExpired is the error text, code key_expired, of a request whose API key has expired.
const msgKeyExpired = "api key expired"

// keyUseFlushEvery is how often recorded key uses are written to the key file.
const keyUseFlushEvery = time.Minute

// apiKeyCreated is the body of a successful create or rotate.
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

// registerAPIKeys mounts list, create, rotate, update and delete for /api/api-keys.
//
// Each route has the cookie flag in routeAccess, so an Api-Key session cannot
// manage keys. s is the server and mux receives the routes. There is no error.
func registerAPIKeys(s *Server, mux *http.ServeMux) {
	s.handle(mux, "GET /api/api-keys", http.HandlerFunc(s.handleAPIKeysList))
	s.handle(mux, "POST /api/api-keys", http.HandlerFunc(s.handleAPIKeysCreate))
	s.handle(mux, "POST /api/api-keys/{id}/rotate", http.HandlerFunc(s.handleAPIKeyRotate))
	s.handle(mux, "PUT /api/api-keys/{id}", http.HandlerFunc(s.handleAPIKeyUpdate))
	s.handle(mux, "DELETE /api/api-keys/{id}", http.HandlerFunc(s.handleAPIKeyDelete))
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
// "apikey:" plus the stored prefix, API is true, and the key id, type, role,
// access and actions are copied in (see keySession). CSRF is not checked.
// A disabled set, an unknown secret, a revoked key, a key without
// ScopeAdmin, a USER key whose role is gone, or a USER key whose own access
// no longer passes normalizeAccess answers 401 and does not read the cookie.
// An expired key answers 401 with code key_expired. An accepted key has its
// use recorded in memory through Touch, at most once a minute, with the client
// address masked; WatchKeyUse writes it to the file later.
// The login limiter is not consulted. The header and the secret are not
// logged. There is no error return.
func (s *Server) authAPIKey(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if !s.apiKeysEnabled() {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	now := time.Now()
	k, err := s.keys.Check(r.Header.Get("Api-Key"), security.ScopeAdmin, now)
	if errors.Is(err, security.ErrAPIKeyExpired) {
		writeErr(w, http.StatusUnauthorized, msgKeyExpired)
		return
	}
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	sess, ok := s.keySession(k)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	s.keys.Touch(k.ID, maskKeyIP(s.clientIP(r)), now)
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
}

// maskKeyIP returns the stored form of the address a key was used from:
// a.b.c.x for IPv4 and the /48 prefix for IPv6. An unparsable addr returns "".
func maskKeyIP(addr string) string {
	ip, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil {
		return ""
	}
	return aiMaskIP(ip)
}

// WatchKeyUse writes recorded key uses to the key file every keyUseFlushEvery
// until ctx is done. A set with no new use is not written. There is no error
// return; FlushKeyUse logs a failed write.
func (s *Server) WatchKeyUse(ctx context.Context) {
	if !s.apiKeysEnabled() {
		return
	}
	t := time.NewTicker(keyUseFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.FlushKeyUse()
		}
	}
}

// FlushKeyUse saves the key file when a use was recorded since the last save.
//
// It holds keyMu so it never interleaves with a key change. A failed write is
// logged and kept for the next call; memory is not reloaded, so the recorded
// uses survive. There is no error return.
func (s *Server) FlushKeyUse() {
	if !s.apiKeysEnabled() || !s.keys.TakeDirty() {
		return
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if err := s.keys.Save(s.cfg.APIKeysFile); err != nil {
		s.logWarn("api key use save failed", "err", err)
		s.keys.MarkDirty()
	}
}

// keySession builds the API session for the verified key k.
//
// KeyType is ADMIN or USER. A USER key keeps its role, or its own access after
// normalizeAccess. It returns false when the role does not exist or the own
// access is refused, so the request is answered 401. An ADMIN key carries no
// role and no access.
func (s *Server) keySession(k security.APIKey) (security.Session, bool) {
	sess := security.Session{
		User: "apikey:" + k.Prefix, API: true,
		KeyID: k.ID, KeyType: k.EffectiveType(), KeyActions: k.Actions,
	}
	if sess.KeyType != security.KeyTypeUser {
		return sess, true
	}
	if k.RoleID != "" {
		if !s.roleExists(k.RoleID) {
			return security.Session{}, false
		}
		sess.KeyRole = k.RoleID
		return sess, true
	}
	var own security.Access
	if k.Access != nil {
		own = *k.Access
	}
	acc, err := normalizeAccess(own)
	if err != nil {
		return security.Session{}, false
	}
	sess.KeyAccess = &acc
	return sess, true
}

// roleExists reports whether roles.json holds a role with id. A server without
// roles storage has none. There is no error.
func (s *Server) roleExists(id string) bool {
	if s.roles == nil {
		return false
	}
	_, ok := s.roles.Get(id)
	return ok
}

// keyBody is the request body of key create and update.
//
// Name, Type and RoleID are pointers, and Scopes, Access and Actions are nil
// when absent, so an update can tell "not sent" from "empty": a field that is
// not sent keeps the stored value. TTLDays is the lifetime counted from now,
// one of security.KeyTTLDays; 0 makes the key never expire. On update it
// extends or shortens the key without changing its secret.
type keyBody struct {
	Name    *string          `json:"name"`
	Scopes  []string         `json:"scopes"`
	Type    *string          `json:"type"`
	RoleID  *string          `json:"role_id"`
	Access  *security.Access `json:"access"`
	Actions []string         `json:"actions"`
	TTLDays *int             `json:"ttl_days"`
}

// expiry returns whether b sets the lifetime and the expiry it asks for.
// A lifetime outside security.KeyTTLDays returns security.ErrAPIKeyTTL.
func (b keyBody) expiry(now time.Time) (bool, *time.Time, error) {
	if b.TTLDays == nil {
		return false, nil, nil
	}
	at, err := security.ExpiryAfter(*b.TTLDays, now)
	return err == nil, at, err
}

// accessOver returns the access fields of cur with every field sent in b laid over it.
//
// A role sent without access drops the stored access, and a non-empty access
// sent without a role drops the stored role, so switching between the two does
// not fail on the old one. cur is the stored key, or a zero key for create.
func (b keyBody) accessOver(cur security.APIKey) security.KeyAccess {
	ka := security.KeyAccess{Type: cur.Type, RoleID: cur.RoleID, Access: cur.Access, Actions: cur.Actions}
	if b.Type != nil {
		ka.Type = *b.Type
	}
	if b.RoleID != nil {
		ka.RoleID = *b.RoleID
	}
	if b.Access != nil {
		ka.Access = b.Access
	}
	if b.Actions != nil {
		ka.Actions = b.Actions
	}
	if b.RoleID != nil && strings.TrimSpace(*b.RoleID) != "" && b.Access == nil {
		ka.Access = nil
	}
	if b.Access != nil && !accessBlank(*b.Access) && b.RoleID == nil {
		ka.RoleID = ""
	}
	return ka
}

// checkKeyAccess validates the role and own access of a USER key before storage.
//
// For a USER key the role must exist (errRoleUnknown) and the own access goes
// through normalizeAccess (errors wrap errAccess*). Other types are passed on
// unchanged; security.ValidKeyAccess clears or refuses what does not fit. The
// result is the key access to store. Every error answers 400.
func (s *Server) checkKeyAccess(ka security.KeyAccess) (security.KeyAccess, error) {
	if ka.Type != security.KeyTypeUser {
		return ka, nil
	}
	if strings.TrimSpace(ka.RoleID) != "" && !s.roleExists(strings.TrimSpace(ka.RoleID)) {
		return security.KeyAccess{}, errRoleUnknown
	}
	if ka.Access != nil {
		acc, err := normalizeAccess(*ka.Access)
		if err != nil {
			return security.KeyAccess{}, err
		}
		ka.Access = &acc
	}
	return ka, nil
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
// The body is a keyBody: {"name","scopes","type","role_id","access","actions","ttl_days"},
// limited to userBodyLimit bytes. Without ttl_days the key never expires; a
// ttl_days outside security.KeyTTLDays answers 400. A bad body, an unknown role, an access
// refused by normalizeAccess, ErrAPIKeyName, ErrAPIKeyNameLen, ErrAPIKeyScope,
// ErrAPIKeyType, ErrAPIKeyAction, ErrAPIKeyAccess and ErrAPIKeyUserAction
// answer 400 with the error text. Any other Generate error answers 500
// "internal error". A disabled set answers 503 "api keys disabled". After
// Generate the set is saved; a save error answers 500 and the set is reloaded
// from disk so memory matches the file. The raw secret is not logged. r is the
// cookie session.
func (s *Server) handleAPIKeysCreate(w http.ResponseWriter, r *http.Request) {
	if !s.apiKeysEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "api keys disabled")
		return
	}
	var body keyBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, userBodyLimit)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	ka, err := s.checkKeyAccess(body.accessOver(security.APIKey{}))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_, expires, err := body.expiry(time.Now())
	if err != nil {
		writeAPIKeyErr(w, err)
		return
	}
	name := ""
	if body.Name != nil {
		name = *body.Name
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	raw, k, err := s.keys.GenerateWith(name, body.Scopes, ka)
	if err == nil && expires != nil {
		k, err = s.keys.SetExpiry(k.ID, expires)
	}
	if err != nil {
		writeAPIKeyErr(w, err)
		return
	}
	if err := s.persistKeys(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, apiKeyCreated{Key: raw, Item: k.Public()})
}

// handleAPIKeyRotate replaces the secret of one key and returns the new raw secret once.
//
// The id comes from the path. A disabled set answers 503 "api keys disabled".
// ErrAPIKeyNotFound answers 404 "not found". ErrAPIKeyRevoked answers 409 with
// the error text. Any other error, including a save failure, answers 500.
// Success answers 200 with the raw secret and the public record. After Rotate
// the set is saved; a save error reloads the set from disk. r is the cookie
// session. The secret is not logged.
func (s *Server) handleAPIKeyRotate(w http.ResponseWriter, r *http.Request) {
	if !s.apiKeysEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "api keys disabled")
		return
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	raw, k, err := s.keys.Rotate(r.PathValue("id"))
	if err != nil {
		writeAPIKeyErr(w, err)
		return
	}
	if err := s.persistKeys(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, apiKeyCreated{Key: raw, Item: k.Public()})
}

// handleAPIKeyUpdate changes the name, scopes, type, role, access and actions of one key.
//
// The body is a keyBody limited to userBodyLimit bytes. A field that is not
// sent keeps the stored value, so a client that sends only name and scopes
// leaves the access and actions as they were. A disabled set answers 503. A
// bad body answers 400 "bad request". ErrAPIKeyNotFound answers 404.
// ErrAPIKeyRevoked answers 409 with the error text. An unknown role, an
// access refused by normalizeAccess and every validation error of create
// answer 400 with the error text. The access fields are validated before
// anything is changed, so a refused request leaves the key as it was. Any
// other error, including a save failure, answers 500. Success answers 200
// with the public record. r is the cookie session. The secret is not logged.
func (s *Server) handleAPIKeyUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.apiKeysEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "api keys disabled")
		return
	}
	var body keyBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, userBodyLimit)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	id := r.PathValue("id")
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	cur, ok := s.keys.Get(id)
	if !ok {
		writeAPIKeyErr(w, security.ErrAPIKeyNotFound)
		return
	}
	if !cur.Active() {
		writeAPIKeyErr(w, security.ErrAPIKeyRevoked)
		return
	}
	ka, err := s.checkKeyAccess(body.accessOver(cur))
	if err == nil {
		ka, err = security.ValidKeyAccess(ka)
	}
	if err != nil {
		writeAPIKeyErr(w, err)
		return
	}
	setExpiry, expires, err := body.expiry(time.Now())
	if err != nil {
		writeAPIKeyErr(w, err)
		return
	}
	name, scopes := cur.Name, cur.Scopes
	if body.Name != nil {
		name = *body.Name
	}
	if body.Scopes != nil {
		scopes = body.Scopes
	}
	if _, err := s.keys.Update(id, name, scopes); err != nil {
		writeAPIKeyErr(w, err)
		return
	}
	k, err := s.keys.SetAccess(id, ka)
	if err == nil && setExpiry {
		k, err = s.keys.SetExpiry(id, expires)
	}
	if err != nil {
		writeAPIKeyErr(w, err)
		return
	}
	if err := s.persistKeys(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, k.Public())
}

// handleAPIKeyDelete revokes the key named by the id path segment, or removes it when hard=1.
//
// A disabled set answers 503. When the query hard is not "1", Revoke answers
// 404 for an unknown or already revoked id and 204 after the save. When hard
// is "1", Remove answers 404 for an unknown id, 409 when the key is still
// active, and 204 after the revoked record is dropped and saved. A save error
// answers 500 and the set is reloaded from disk. After the save the rate-limit
// entry of the key is dropped. r is the cookie session. The secret is not logged.
func (s *Server) handleAPIKeyDelete(w http.ResponseWriter, r *http.Request) {
	if !s.apiKeysEnabled() {
		writeErr(w, http.StatusServiceUnavailable, "api keys disabled")
		return
	}
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	id := r.PathValue("id")
	if r.URL.Query().Get("hard") == "1" {
		if err := s.keys.Remove(id); err != nil {
			writeAPIKeyErr(w, err)
			return
		}
	} else if !s.keys.Revoke(id) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.persistKeys(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.keyLim.forget(id)
	w.WriteHeader(http.StatusNoContent)
}

// writeAPIKeyErr maps a KeySet error onto the HTTP status for key routes.
//
// err is the error from Generate, Rotate, Update, SetAccess, Remove or
// checkKeyAccess. ErrAPIKeyNotFound answers 404 "not found". ErrAPIKeyRevoked
// and ErrAPIKeyActive answer 409 with the error text. ErrAPIKeyName,
// ErrAPIKeyNameLen, ErrAPIKeyScope, ErrAPIKeyType, ErrAPIKeyAction,
// ErrAPIKeyAccess, ErrAPIKeyUserAction, an unknown role and an access refused
// by normalizeAccess answer 400 with the error text. Any other error answers
// 500 "internal error". There is no return value.
func writeAPIKeyErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, security.ErrAPIKeyNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, security.ErrAPIKeyRevoked), errors.Is(err, security.ErrAPIKeyActive):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, security.ErrAPIKeyName), errors.Is(err, security.ErrAPIKeyNameLen), errors.Is(err, security.ErrAPIKeyScope),
		errors.Is(err, security.ErrAPIKeyType), errors.Is(err, security.ErrAPIKeyAction),
		errors.Is(err, security.ErrAPIKeyAccess), errors.Is(err, security.ErrAPIKeyUserAction),
		errors.Is(err, security.ErrAPIKeyTTL), errors.Is(err, errRoleUnknown), isAccessErr(err):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
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
