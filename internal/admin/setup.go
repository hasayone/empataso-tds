package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hasayone/empataso-tds/internal/security"
)

// adminProfile is the environment administrator identity stored beside the password hash.
// The password itself stays in admin.hash. This file holds only the login and the email.
type adminProfile struct {
	Login string `json:"login"`
	Email string `json:"email"`
	// Lang is the panel language of the environment administrator: ru, en or uk.
	Lang string `json:"lang,omitempty"`
	// TZ is the statistics time zone of the environment administrator, an IANA name.
	TZ string `json:"tz,omitempty"`
}

// setupView is the body of GET /api/setup.
// Needed is true until the first-run form has written the password.
// PublicIP is Config.PublicIP and may be empty. TokenRequired is true only while the form is
// open and Config.SetupToken is set, so the panel knows to ask for it. The token is never included.
type setupView struct {
	Needed        bool   `json:"needed"`
	PublicIP      string `json:"public_ip"`
	TokenRequired bool   `json:"token_required"`
}

// adminProfilePath is admin.json in the same directory as the password file.
// passwordFile is EMTDS_ADMIN_PASSWORD_FILE. An empty path is an error:
// there is nowhere to keep the identity. The returned path is not created.
func adminProfilePath(passwordFile string) (string, error) {
	if strings.TrimSpace(passwordFile) == "" {
		return "", errors.New("admin: password file is not set")
	}
	return filepath.Join(filepath.Dir(passwordFile), "admin.json"), nil
}

// readAdminProfile loads path. A missing file returns a zero profile and a nil error.
// path is admin.json. A broken file is an error so a restart does not forget the login.
func readAdminProfile(path string) (adminProfile, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return adminProfile{}, nil
	}
	if err != nil {
		return adminProfile{}, err
	}
	var prof adminProfile
	if err := json.Unmarshal(b, &prof); err != nil {
		return adminProfile{}, err
	}
	return prof, nil
}

// applyAdminProfile copies a saved login and email onto the server.
// A missing file leaves the environment login in place. A broken file is returned
// and the server does not start. It returns nil when there is no password file.
func (s *Server) applyAdminProfile() error {
	path, err := adminProfilePath(s.cfg.PasswordFile)
	if err != nil {
		return nil
	}
	prof, err := readAdminProfile(path)
	if err != nil {
		return err
	}
	if prof.Login != "" {
		s.cfg.AdminUser = prof.Login
	}
	s.adminEmail.Store(prof.Email)
	return nil
}

// setupLoginOK reports whether login can be the environment administrator name.
// login is the raw field. It must be 2 to 64 characters, without spaces or controls.
func setupLoginOK(login string) bool {
	n := utf8.RuneCountInString(login)
	if n < 2 || n > 64 {
		return false
	}
	for _, r := range login {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// setupEmailOK reports whether email is a bare address.
// The parsed address must equal the field, so a display name is rejected.
func setupEmailOK(email string) bool {
	addr, err := mail.ParseAddress(email)
	return err == nil && addr.Address == email
}

// adminLang is the panel language stored on the environment administrator profile.
// ru, en and uk are returned. A missing file, a broken file and any other
// language are empty. It does not fail.
func (s *Server) adminLang() string {
	path, err := adminProfilePath(s.cfg.PasswordFile)
	if err != nil {
		return ""
	}
	prof, err := readAdminProfile(path)
	if err != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(prof.Lang)) {
	case "ru", "en", "uk":
		return strings.ToLower(strings.TrimSpace(prof.Lang))
	default:
		return ""
	}
}

// writeAdminLang stores lang on the environment administrator profile.
// lang is ru, en or uk. Login and email already in the file stay.
// A missing password file and a write error are returned.
func (s *Server) writeAdminLang(lang string) error {
	path, err := adminProfilePath(s.cfg.PasswordFile)
	if err != nil {
		return err
	}
	prof, err := readAdminProfile(path)
	if err != nil {
		return err
	}
	prof.Lang = lang
	raw, err := json.Marshal(prof)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(raw, '\n'), 0o600)
}

// handleMeLang saves the panel language of the signed-in account.
// The body is lang: ru, en or uk. The environment administrator writes it on
// the admin profile. A users.json account writes that account's prefs and keeps
// the time zone. A bad language is 400. A missing password file for the
// environment administrator is 503. User-store errors use the users statuses.
func (s *Server) handleMeLang(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lang string `json:"lang"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	lang := strings.ToLower(strings.TrimSpace(body.Lang))
	switch lang {
	case "ru", "en", "uk":
	default:
		writeErr(w, http.StatusBadRequest, "invalid language")
		return
	}
	sess := sessionFrom(r.Context())
	if sess.UID != "" {
		if _, err := s.updateUser(sess.UID, func(u *security.User) error {
			merged := mergePrefs(u.Prefs, security.Prefs{Lang: lang})
			if err := validatePrefs(merged); err != nil {
				return err
			}
			u.Prefs = merged
			return nil
		}); err != nil {
			writeUserUpdateErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"lang": lang})
		return
	}
	if err := s.writeAdminLang(lang); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "profile is not stored")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"lang": lang})
}

// adminTZ is the statistics time zone stored on the environment administrator profile.
// A missing file, a broken file and a zone time.LoadLocation rejects are empty.
// It does not fail.
func (s *Server) adminTZ() string {
	path, err := adminProfilePath(s.cfg.PasswordFile)
	if err != nil {
		return ""
	}
	prof, err := readAdminProfile(path)
	if err != nil {
		return ""
	}
	tz := strings.TrimSpace(prof.TZ)
	if tz == "" {
		return ""
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return ""
	}
	return tz
}

// writeAdminTZ stores tz on the environment administrator profile.
// tz is an IANA name the caller already checked. Login, email and language stay.
// A missing password file and a write error are returned.
func (s *Server) writeAdminTZ(tz string) error {
	path, err := adminProfilePath(s.cfg.PasswordFile)
	if err != nil {
		return err
	}
	prof, err := readAdminProfile(path)
	if err != nil {
		return err
	}
	prof.TZ = tz
	raw, err := json.Marshal(prof)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(raw, '\n'), 0o600)
}

// handleMeTZ saves the statistics time zone of the signed-in account.
// The body is tz, an IANA name. The environment administrator writes it on
// the admin profile. A users.json account writes that account's prefs and keeps
// the language. A bad zone is 400. A missing password file for the environment
// administrator is 503. User-store errors use the users statuses.
func (s *Server) handleMeTZ(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TZ string `json:"tz"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	tz := strings.TrimSpace(body.TZ)
	if _, err := time.LoadLocation(tz); err != nil || tz == "" {
		writeErr(w, http.StatusBadRequest, "invalid timezone")
		return
	}
	sess := sessionFrom(r.Context())
	if sess.UID != "" {
		if _, err := s.updateUser(sess.UID, func(u *security.User) error {
			merged := mergePrefs(u.Prefs, security.Prefs{TZ: tz})
			if err := validatePrefs(merged); err != nil {
				return err
			}
			u.Prefs = merged
			return nil
		}); err != nil {
			writeUserUpdateErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"tz": tz})
		return
	}
	if err := s.writeAdminTZ(tz); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "profile is not stored")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"tz": tz})
}

// handleSetupGet tells the panel whether the first-run form is still open.
// The body is setupView. It is public and does not create a session.
func (s *Server) handleSetupGet(w http.ResponseWriter, r *http.Request) {
	open := s.setup.Load()
	writeJSON(w, http.StatusOK, setupView{Needed: open, PublicIP: s.cfg.PublicIP,
		TokenRequired: open && s.cfg.SetupToken != ""})
}

// setupBody is the JSON of POST /api/setup. The password is never stored in admin.json.
// Token is the first-run token; it is read only when Config.SetupToken is set.
type setupBody struct {
	Login    string `json:"login"`
	Password string `json:"password"`
	Email    string `json:"email"`
	Token    string `json:"token"`
}

// setupTokenError is the 403 text for the first-run token: "setup token is required" for an
// empty value and "setup token is wrong" for any other mismatch. The panel tells them apart by
// the substring "setup token".
func setupTokenError(given string) string {
	if given == "" {
		return "setup token is required"
	}
	return "setup token is wrong"
}

// setupTokenOK reports whether given may open the first-run form.
// It is true when Config.SetupToken is empty, and otherwise only for an equal value. The
// SHA-256 digests are compared in constant time, so the length of the token does not leak.
// Neither value is logged.
func (s *Server) setupTokenOK(given string) bool {
	want := s.cfg.SetupToken
	if want == "" {
		return true
	}
	givenSum := sha256.Sum256([]byte(given))
	wantSum := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(givenSum[:], wantSum[:]) == 1
}

// readSetupBody reads and checks the first-run form.
// w receives 403 when the first-run token is missing or wrong, checked before any other field,
// and 400 when the body or a field is rejected. It returns false after writing that status.
// A taken login is also 400. The password and the token are not logged.
func (s *Server) readSetupBody(w http.ResponseWriter, r *http.Request) (setupBody, bool) {
	var body setupBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return setupBody{}, false
	}
	if !s.setupTokenOK(body.Token) {
		writeErr(w, http.StatusForbidden, setupTokenError(body.Token))
		return setupBody{}, false
	}
	if !setupLoginOK(body.Login) {
		writeErr(w, http.StatusBadRequest, "login must be 2 to 64 characters without spaces")
		return setupBody{}, false
	}
	if body.Password == "admin" {
		writeErr(w, http.StatusBadRequest, "password must not be admin")
		return setupBody{}, false
	}
	if len(body.Password) < 6 {
		writeErr(w, http.StatusBadRequest, "password too short (min. 6)")
		return setupBody{}, false
	}
	if !setupEmailOK(body.Email) {
		writeErr(w, http.StatusBadRequest, "email is not valid")
		return setupBody{}, false
	}
	if s.users != nil {
		if _, ok := s.users.ByLogin(body.Login); ok {
			writeErr(w, http.StatusBadRequest, "login is taken")
			return setupBody{}, false
		}
	}
	return body, true
}

// writeSetupFiles stores the profile and the password hash, both mode 0600.
// login and email go to admin.json. hash is the argon2id string, never the password.
// A failed hash write removes the profile just written. It returns the write error.
func (s *Server) writeSetupFiles(login, email, hash string) error {
	profilePath, err := adminProfilePath(s.cfg.PasswordFile)
	if err != nil || s.cfg.PasswordFile == "" {
		return errors.New("admin: password file is not set")
	}
	raw, err := json.Marshal(adminProfile{Login: login, Email: email})
	if err != nil {
		return err
	}
	if err := writeFileAtomic(profilePath, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(s.cfg.PasswordFile, []byte(hash), 0o600); err != nil {
		_ = os.Remove(profilePath)
		return err
	}
	return nil
}

// handleSetupPost stores the first administrator and opens a session.
// The body is login, password, email and, when Config.SetupToken is set, token.
// A short password, the password "admin", a bad login or a bad email is 400.
// A finished setup is 409 whatever the token is, so that answer reveals nothing about it.
// While the form is open, a missing or wrong token is 403 ("setup token is required" or
// "setup token is wrong"), checked after the limiter and before the password is hashed.
// The limiter is the same one as sign-in. Neither the password nor the token is written to the log.
// Success is 200 with the CSRF token and the session cookie.
func (s *Server) handleSetupPost(w http.ResponseWriter, r *http.Request) {
	if s.loginClosed(r) {
		writeErr(w, http.StatusServiceUnavailable, "login unavailable")
		return
	}
	if s.cfg.Limiter != nil {
		allowed, err := s.cfg.Limiter.Allow(r.Context(), s.clientIP(r))
		if err != nil {
			s.logWarn("setup limiter unavailable", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !allowed {
			writeRetryLater(w, msgTooManyAttempts, LoginLimitWindow)
			return
		}
	}
	s.pwWrite.Lock()
	open := s.setup.Load()
	s.pwWrite.Unlock()
	if !open {
		writeErr(w, http.StatusConflict, "setup already finished")
		return
	}
	body, ok := s.readSetupBody(w, r)
	if !ok {
		return
	}
	if !s.hashes.enter() {
		writeRetryLater(w, msgTooManyAttempts, time.Second)
		return
	}
	hash, err := security.HashPassword(body.Password)
	s.hashes.leave()
	if err != nil || security.ValidateHash(hash) != nil {
		s.logWarn("setup hash was not stored", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.pwWrite.Lock()
	defer s.pwWrite.Unlock()
	if !s.setup.Load() {
		writeErr(w, http.StatusConflict, "setup already finished")
		return
	}
	if err := s.writeSetupFiles(body.Login, body.Email, hash); err != nil {
		s.logWarn("setup files were not written", "err", err)
		writeErr(w, http.StatusInternalServerError, "failed to save (check EMTDS_ADMIN_PASSWORD_FILE)")
		return
	}
	if err := s.publish(hash); err != nil {
		s.logWarn("setup hash was not published", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.cfg.AdminUser = body.Login
	s.adminEmail.Store(body.Email)
	s.setup.Store(false)
	csrf, err := s.issueSession(w, r, "")
	if err != nil {
		s.logWarn("session after setup failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"csrf": csrf})
}
