package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

const (
	// keitaroSourceFile, keitaroSourceEnv and keitaroSourceNone name where the
	// current tracker address and key came from.
	keitaroSourceFile = "file"
	keitaroSourceEnv  = "env"
	keitaroSourceNone = "none"

	keitaroKeyMax        = 512
	keitaroRedirectLimit = 10
	keitaroSettingsBody  = 1 << 16
)

// ktCreds is a copy of the tracker origin and the Admin API key.
// A job takes one copy at its start and uses it until it ends, so a later
// settings change cannot mix two trackers inside one run.
type ktCreds struct {
	Base string
	Key  string
}

// configured reports whether origin and key are both non-empty. Blank space does not count.
func (c ktCreds) configured() bool {
	return strings.TrimSpace(c.Base) != "" && strings.TrimSpace(c.Key) != ""
}

// host returns the hostname of the origin, or an empty string when there is none.
func (c ktCreds) host() string {
	return keitaroHost(c.Base)
}

// keitaroFileBody is the JSON stored in keitaro.json.
type keitaroFileBody struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// keitaroSettingsView is the body of GET and PUT /api/keitaro/settings.
// The key is never a field. Source is file, env or none.
type keitaroSettingsView struct {
	BaseURL string `json:"base_url"`
	Host    string `json:"host"`
	HasKey  bool   `json:"has_key"`
	Source  string `json:"source"`
}

// keitaroSettingsPut is the body of PUT /api/keitaro/settings.
// An empty APIKey or a mask keeps the stored key.
type keitaroSettingsPut struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// keitaroCreds returns a copy of the current tracker origin and key.
// It is the only reader of the stored values.
func (s *Server) keitaroCreds() ktCreds {
	c, _ := s.keitaroState()
	return c
}

// keitaroState returns the current origin and key together with their source.
func (s *Server) keitaroState() (ktCreds, string) {
	s.ktMu.RLock()
	defer s.ktMu.RUnlock()
	return ktCreds{Base: s.ktBase, Key: s.ktKey}, s.ktSource
}

// setKeitaroState replaces the stored origin, key and source.
func (s *Server) setKeitaroState(c ktCreds, source string) {
	s.ktMu.Lock()
	s.ktBase, s.ktKey, s.ktSource = c.Base, c.Key, source
	s.ktMu.Unlock()
}

// loadKeitaro sets the initial origin and key.
// The file wins when it exists. Otherwise the start values of the Config are used.
// It returns an error when the file exists but cannot be read or is not valid.
// Falling back to the environment then would silently undo a saved change.
func (s *Server) loadKeitaro() error {
	return s.reloadKeitaroConns()
}

// keitaroEnv returns the tracker origin and key given by the Config and their source.
// The origin goes through normalizeKeitaroBase. An origin that is not valid, for example one with
// userinfo or a query, makes the whole environment pair count as not configured; the log
// line says so and does not repeat the address.
func (s *Server) keitaroEnv() (ktCreds, string) {
	env := ktCreds{Base: strings.TrimSpace(s.cfg.KeitaroBase), Key: s.cfg.KeitaroKey}
	if env.Base != "" {
		base, err := normalizeKeitaroBase(env.Base)
		if err != nil {
			s.logWarn("keitaro address from the environment ignored", "reason", err.Error())
			return ktCreds{}, keitaroSourceNone
		}
		env.Base = base
	}
	if env.Base == "" && env.Key == "" {
		return env, keitaroSourceNone
	}
	return env, keitaroSourceEnv
}

// normalizeKeitaroBase validates a tracker origin typed by the operator or given by the environment.
// raw is the URL. The scheme must be http or https and a host is required;
// userinfo, query and fragment are refused. The host is lower-cased and a trailing slash is removed.
// It returns the cleaned URL, or an error naming the defect.
func normalizeKeitaroBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "?#") {
		return "", errors.New("base_url must be an http or https address without query or fragment")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil {
		return "", errors.New("base_url is not a valid address")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("base_url must use http or https")
	}
	u.Host = strings.ToLower(u.Host)
	return strings.TrimRight(u.String(), "/"), nil
}

// isKeitaroKeyMask reports whether v means "keep the stored key".
// v is the api_key field of a PUT. An empty value and a value made only of
// mask characters count.
func isKeitaroKeyMask(v string) bool {
	v = strings.TrimSpace(v)
	return strings.Trim(v, "*•●") == ""
}

// keitaroSettingsSnapshot builds the view for the current state.
// The address is shown only in its normalized form, so userinfo and a query never leave the server.
func (s *Server) keitaroSettingsSnapshot() keitaroSettingsView {
	c, source := s.keitaroState()
	base, err := normalizeKeitaroBase(c.Base)
	if err != nil {
		base = ""
	}
	return keitaroSettingsView{
		BaseURL: base,
		Host:    keitaroHost(base),
		HasKey:  strings.TrimSpace(c.Key) != "",
		Source:  source,
	}
}

// handleKeitaroSettingsGet returns the tracker address, host, key presence and source.
// The key is not part of the body.
func (s *Server) handleKeitaroSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.keitaroSettingsSnapshot())
}

// handleKeitaroSettingsPut saves the tracker address and key to keitaro.json.
// Answers: 400 for bad JSON, a bad address or a missing key, and 400 "api_key required
// for new host" when the host changes and no new key is sent; 503 when no file
// path is configured; 409 while an import or purge runs, and 409 manifest_host
// when the host changes while the import manifest names another host or none; 500
// "save error" when the file or manifest cannot be handled. Success is 200
// with the same body as GET. The key is never echoed or logged.
func (s *Server) handleKeitaroSettingsPut(w http.ResponseWriter, r *http.Request) {
	var body keitaroSettingsPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, keitaroSettingsBody)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if s.cfg.KeitaroFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "keitaro settings unavailable")
		return
	}
	base, err := normalizeKeitaroBase(body.BaseURL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := s.ktJob.beginPurge(); msg != "" {
		writeErr(w, http.StatusConflict, msg)
		return
	}
	defer s.ktJob.endPurge()
	cur, _ := s.keitaroState()
	hostChanged := keitaroHost(base) != cur.host()
	key := cur.Key
	if !isKeitaroKeyMask(body.APIKey) {
		key = strings.TrimSpace(body.APIKey)
		if len(key) > keitaroKeyMax || strings.ContainsAny(key, "\r\n\x00") {
			writeErr(w, http.StatusBadRequest, "api_key is not valid")
			return
		}
	} else if hostChanged {
		// The stored key belongs to the old tracker and is never sent to another host.
		writeErr(w, http.StatusBadRequest, "api_key required for new host")
		return
	}
	if strings.TrimSpace(key) == "" {
		writeErr(w, http.StatusBadRequest, "api_key is required")
		return
	}
	next := ktCreds{Base: base, Key: key}
	if hostChanged {
		manifest, err := s.loadManifest()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "save error")
			return
		}
		if !manifest.Empty() && !strings.EqualFold(manifest.Host, next.host()) {
			writeErr(w, http.StatusConflict, "manifest_host")
			return
		}
	}
	raw, err := json.Marshal(keitaroFileBody{BaseURL: base, APIKey: key})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if err := writeFileAtomic(s.cfg.KeitaroFile, append(raw, '\n'), 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if err := s.reloadKeitaroConns(); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	s.logInfo("keitaro settings saved", "host", next.host())
	writeJSON(w, http.StatusOK, s.keitaroSettingsSnapshot())
}

// keitaroHTTP is the client for every tracker call.
// It follows a redirect only inside the same host and never from https to http,
// so the Api-Key header cannot reach another server.
var keitaroHTTP = &http.Client{CheckRedirect: keitaroRedirect}

// keitaroRedirect decides whether a tracker redirect is followed.
// req is the next request and via the requests already made, oldest first.
// It returns nil to follow, or http.ErrUseLastResponse to hand the redirect
// response to the caller, which treats it as a non-200 status.
func keitaroRedirect(req *http.Request, via []*http.Request) error {
	first := via[0].URL
	if len(via) >= keitaroRedirectLimit || req.URL.Host != first.Host {
		return http.ErrUseLastResponse
	}
	if first.Scheme == "https" && req.URL.Scheme != "https" {
		return http.ErrUseLastResponse
	}
	return nil
}
