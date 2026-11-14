package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egerkuzma/kuztds/internal/security"
)

// adminProfile is the environment administrator identity stored beside the password hash.
// The password itself stays in admin.hash. This file holds only the login and the email.
type adminProfile struct {
	Login string `json:"login"`
	Email string `json:"email"`
}

// setupView is the body of GET /api/setup.
// Needed is true until the first-run form has written the password.
// PublicIP is Config.PublicIP and may be empty. No secret is included.
type setupView struct {
	Needed   bool   `json:"needed"`
	PublicIP string `json:"public_ip"`
}

// adminProfilePath is admin.json in the same directory as the password file.
// passwordFile is KUZTDS_ADMIN_PASSWORD_FILE. An empty path is an error:
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

// handleSetupGet tells the panel whether the first-run form is still open.
// The body is setupView. It is public and does not create a session.
func (s *Server) handleSetupGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, setupView{Needed: s.setup.Load(), PublicIP: s.cfg.PublicIP})
}

// setupBody is the JSON of POST /api/setup. The password is never stored in admin.json.
type setupBody struct {
	Login    string `json:"login"`
	Password string `json:"password"`
	Email    string `json:"email"`
}

// readSetupBody reads and checks the first-run form.
// w receives 400 when the body or a field is rejected. It returns false after writing that status.
// A taken login is also 400. The password is not logged.
func (s *Server) readSetupBody(w http.ResponseWriter, r *http.Request) (setupBody, bool) {
	var body setupBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
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
// The body is login, password and email. A short password, the password "admin",
// a bad login or a bad email is 400. A second call after success is 409.
// The limiter is the same one as sign-in. The password is not written to the log.
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
			writeErr(w, http.StatusTooManyRequests, "too many attempts")
			return
		}
	}
	body, ok := s.readSetupBody(w, r)
	if !ok {
		return
	}
	s.pwWrite.Lock()
	open := s.setup.Load()
	s.pwWrite.Unlock()
	if !open {
		writeErr(w, http.StatusConflict, "setup already finished")
		return
	}
	if !s.hashes.enter() {
		writeErr(w, http.StatusTooManyRequests, "too many attempts")
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
		writeErr(w, http.StatusInternalServerError, "failed to save (check KUZTDS_ADMIN_PASSWORD_FILE)")
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
