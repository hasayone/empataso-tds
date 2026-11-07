package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
)

const recoveryTTL = 15 * time.Minute

const recoveryFileName = "recovery.json"

// errRecoveryRejected means the code does not reset this login. The handler
// answers the same way as a wrong code, so the response does not say whether
// the login exists.
var errRecoveryRejected = errors.New("admin: recovery rejected")

type recoveryRecord struct {
	Login    string    `json:"login"`
	CodeHash string    `json:"code_hash"`
	Expires  time.Time `json:"expires"`
}

// recoveryPath is the one-time code file next to the password hash.
// passwordFile is KUZTDS_ADMIN_PASSWORD_FILE. An empty path or a path with no
// directory returns an error and no location.
func recoveryPath(passwordFile string) (string, error) {
	if strings.TrimSpace(passwordFile) == "" {
		return "", errors.New("admin: password file is not configured")
	}
	dir := filepath.Dir(passwordFile)
	if dir == "" || dir == "." {
		return "", errors.New("admin: password file is not configured")
	}
	return filepath.Join(dir, recoveryFileName), nil
}

// IssueRecoveryCode prints a one-time recovery code to w and stores only its
// hash, the login and the expiry beside the password file.
// login is the account the code resets. passwordFile is the hash file whose
// directory receives recovery.json, mode 0600. now is the issue time. The code
// itself is not written to disk. A later call replaces the previous file.
// Nothing is printed when the file cannot be written.
func IssueRecoveryCode(w io.Writer, login, passwordFile string, now time.Time) error {
	login = strings.TrimSpace(login)
	if login == "" {
		return errors.New("admin: recovery login is empty")
	}
	path, err := recoveryPath(passwordFile)
	if err != nil {
		return err
	}
	code, err := security.RandomToken()
	if err != nil {
		return err
	}
	hash, err := security.HashPassword(code)
	if err != nil {
		return err
	}
	rec := recoveryRecord{Login: login, CodeHash: hash, Expires: now.UTC().Add(recoveryTTL)}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, code); err != nil {
		return err
	}
	return nil
}

// loginClosed reports that this host is an admin.* name and the shared
// login limiter is not connected, so sign-in and recovery stay closed.
func (s *Server) loginClosed(r *http.Request) bool {
	return strings.HasPrefix(config.NormalizeHTTPHost(r.Host), "admin.") && !s.cfg.LoginRateLimitShared
}

// readRecovery loads the current code file. A missing, unreadable or
// incomplete file returns false. The code itself is not in the file.
func (s *Server) readRecovery() (recoveryRecord, bool) {
	path, err := recoveryPath(s.cfg.PasswordFile)
	if err != nil {
		return recoveryRecord{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return recoveryRecord{}, false
	}
	var rec recoveryRecord
	if json.Unmarshal(raw, &rec) != nil || rec.Login == "" || rec.CodeHash == "" {
		return recoveryRecord{}, false
	}
	return rec, true
}

// recoveryLoginOK reports whether given is the account stored in the code file.
// stored and given are the logins. adminUser is the root login, compared with
// case sensitivity; any other login is compared ignoring case.
func recoveryLoginOK(stored, given, adminUser string) bool {
	if security.EqualTokens(stored, adminUser) {
		return security.EqualTokens(given, adminUser)
	}
	return strings.EqualFold(strings.TrimSpace(stored), strings.TrimSpace(given))
}

// handleRecover sets a new password when the one-time code matches.
// The body is login, code and the new password. A wrong code, an expired code
// and an unknown login are one 401. The code file is removed only after the
// new hash is stored. The password and the code are not logged.
func (s *Server) handleRecover(w http.ResponseWriter, r *http.Request) {
	if s.loginClosed(r) {
		writeErr(w, http.StatusServiceUnavailable, "login unavailable")
		return
	}
	ip := s.clientIP(r)
	if s.cfg.Limiter != nil {
		allowed, err := s.cfg.Limiter.Allow(r.Context(), ip)
		if err != nil {
			s.logWarn("recovery limiter unavailable, refusing before the code check", "ip", ip, "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !allowed {
			writeErr(w, http.StatusTooManyRequests, "too many attempts")
			return
		}
	}
	var body struct {
		Login    string `json:"login"`
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if len(body.Password) < 6 {
		writeErr(w, http.StatusBadRequest, "password too short (min. 6)")
		return
	}
	if !s.hashes.enter() {
		if n := s.hashes.report(); n > 0 {
			s.logWarn("recovery verifications refused: hashing slots exhausted", "since_last", n)
		}
		writeErr(w, http.StatusTooManyRequests, "too many attempts")
		return
	}
	rec, ok := s.readRecovery()
	hashToCheck := security.DummyHash
	loginMatch := false
	if ok {
		hashToCheck = rec.CodeHash
		if rec.Expires.After(time.Now()) && recoveryLoginOK(rec.Login, body.Login, s.cfg.AdminUser) {
			loginMatch = true
		}
	}
	codeOK := security.VerifyPassword(body.Code, hashToCheck)
	s.hashes.leave()
	if !loginMatch || !codeOK {
		s.logWarn("password recovery failed", "ip", ip, "login_fp", s.loginFP(body.Login))
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := s.applyRecoveredPassword(rec.Login, body.Password); err != nil {
		if errors.Is(err, errRecoveryRejected) {
			s.logWarn("password recovery failed", "ip", ip, "login_fp", s.loginFP(body.Login))
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		s.logWarn("password recovery save failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.consumeRecovery()
	writeJSON(w, http.StatusOK, map[string]string{"status": "changed"})
}

func (s *Server) applyRecoveredPassword(login, password string) error {
	if security.EqualTokens(login, s.cfg.AdminUser) {
		return s.replaceRootPassword(password)
	}
	if s.users == nil {
		return errRecoveryRejected
	}
	u, ok := s.users.ByLogin(login)
	if !ok || !u.Enabled || u.Hash == "" {
		return errRecoveryRejected
	}
	return s.setUserPassword(u.ID, password)
}

// replaceRootPassword writes a new administrator hash to the password file and
// then publishes it. password is the new password. The file is written first:
// a failed write leaves the previous hash in memory. An empty password file
// path returns an error and does not change memory.
func (s *Server) replaceRootPassword(password string) error {
	if s.cfg.PasswordFile == "" {
		return errors.New("admin: password file is not configured")
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	if err := security.ValidateHash(hash); err != nil {
		return err
	}
	s.pwWrite.Lock()
	defer s.pwWrite.Unlock()
	if err := writeFileAtomic(s.cfg.PasswordFile, []byte(hash), 0o600); err != nil {
		return err
	}
	return s.publish(hash)
}

// consumeRecovery removes the code file so the same code cannot be used again.
// A failed remove is overwritten with an already expired record.
func (s *Server) consumeRecovery() {
	path, err := recoveryPath(s.cfg.PasswordFile)
	if err != nil {
		return
	}
	if err := os.Remove(path); err == nil || os.IsNotExist(err) {
		return
	}
	dead := recoveryRecord{Expires: time.Unix(0, 0).UTC(), CodeHash: security.DummyHash, Login: "consumed"}
	raw, mErr := json.Marshal(dead)
	if mErr != nil || writeFileAtomic(path, raw, 0o600) != nil {
		s.logWarn("recovery code file was not removed", "err", err)
	}
}
