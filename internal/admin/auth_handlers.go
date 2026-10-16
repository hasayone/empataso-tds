package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/security"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)

	// The limiter goes first, and its failure answers 500 before any password
	// is looked at. That ordering is the whole point.
	//
	// A wrong password answers 401. A right password with the session store
	// down answers 500, because issueSession cannot write the session. Two
	// different answers to "was that the password" is an oracle: with the
	// limiter previously swallowing the storage error into a fail-open true, an
	// unlimited stream of guesses could be run against a dead Redis and be told
	// which one was correct. Nobody could log in during that window — but the
	// password was learned, and Redis comes back.
	//
	// Refusing before the comparison removes the answer instead of hiding it.
	// It does not remove the window entirely: Redis can still die between this
	// check and issueSession, with an argon2 verification in between. That
	// residue is the width of one hash, and every probe of it costs the
	// attacker a hash and a slot in the limiter. Closing it completely would
	// take making both branches do the same work — writing and discarding a
	// session on every failure — which puts a scanner's traffic into the same
	// Redis the engine keeps its counters in. Not worth it. Do not read this as
	// "the oracle is gone": it is narrowed to the gap between here and
	// issueSession, and moving the limiter would widen it back.
	if s.cfg.Limiter != nil {
		allowed, err := s.cfg.Limiter.Allow(r.Context(), ip)
		if err != nil {
			s.logWarn("login limiter unavailable, refusing before the password check", "ip", ip, "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !allowed {
			writeErr(w, http.StatusTooManyRequests, "too many attempts")
			return
		}
	}

	var body struct{ Login, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}

	if !s.hashes.enter() {
		if n := s.hashes.report(); n > 0 {
			s.logWarn("login verifications refused: hashing slots exhausted", "since_last", n)
		}
		writeErr(w, http.StatusTooManyRequests, "too many attempts")
		return
	}
	userOK := security.EqualTokens(body.Login, s.cfg.AdminUser)
	// Both halves are evaluated. With "!userOK || !VerifyPassword(...)" Go
	// short-circuits: a wrong user name returns in microseconds while the right
	// one costs argon2's hundred milliseconds, and the administrator's login
	// name reads off the clock. Constant-time EqualTokens right next to it buys
	// nothing while the operator above it leaks the answer.
	passOK := security.VerifyPassword(body.Password, s.currentHash())
	s.hashes.leave()

	if !userOK || !passOK {
		// The submitted login is not logged. People type their password into
		// the user name box, and this line goes to a collector that outlives
		// the password. The fingerprint is keyed with a value generated at
		// startup, so a scanner still reads as one repeated login and a spray
		// as many different ones, while a dictionary run against the log
		// finds nothing.
		s.logWarn("admin login failed", "ip", ip, "login_fp", s.loginFP(body.Login))
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	csrf, err := s.issueSession(w, r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"csrf": csrf})
}

// loginFP is a short keyed fingerprint of a submitted login name, for telling a
// scanner (one value, many attempts) from a spray (many values) without putting
// the value itself into a log.
//
// Keyed, not plain: eight bytes of bare SHA-256 are sixty-four bits, which is
// plenty to confirm a guess, so a plain digest of a mistyped password is a
// crackable artefact sitting in the log. The key is generated per process, so
// the fingerprints are comparable within one run — the window anyone actually
// looks at — and useless for linking activity across restarts or for testing a
// dictionary against.
func (s *Server) loginFP(login string) string {
	m := hmac.New(sha256.New, s.loginFPKey)
	m.Write([]byte(login))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:8])
}

// issueSession creates a fresh session bound to the current password, sets the
// cookie and returns the new CSRF token.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request) (string, error) {
	token, err1 := security.RandomToken()
	csrf, err2 := security.RandomToken()
	if err1 != nil {
		return "", err1
	}
	if err2 != nil {
		return "", err2
	}
	sess := security.Session{
		User:    s.cfg.AdminUser,
		CSRF:    csrf,
		Created: time.Now(),
		PwFP:    s.current().fp,
	}
	if err := s.cfg.Sessions.Create(r.Context(), token, sess, s.cfg.SessionTTL); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/",
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
		Expires: time.Now().Add(s.cfg.SessionTTL),
	})
	return csrf, nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.cfg.Sessions.Delete(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"user": sess.User, "csrf": sess.CSRF, "engine": s.linkBase()})
}

// linkBase is the engine address shown in campaign links and postbacks.
// PublicURL is used when set, so a Docker-internal EngineURL is not opened in the browser.
// An empty PublicURL falls back to EngineURL. The result has no trailing slash.
func (s *Server) linkBase() string {
	if u := strings.TrimRight(strings.TrimSpace(s.cfg.PublicURL), "/"); u != "" {
		return u
	}
	return strings.TrimRight(strings.TrimSpace(s.cfg.EngineURL), "/")
}

// handlePassword changes the administrator password (argon2id), saving the hash to a file.
func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var body struct{ Old, New string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if !security.VerifyPassword(body.Old, s.currentHash()) {
		writeErr(w, http.StatusUnauthorized, "incorrect current password")
		return
	}
	if len(body.New) < 6 {
		writeErr(w, http.StatusBadRequest, "password too short (min. 6)")
		return
	}
	hash, err := security.HashPassword(body.New)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// One lock around the whole change. The write and the switch have to land
	// in the same order for everyone: with two changes in flight, the disk
	// keeps whichever renamed last while memory keeps whichever locked last,
	// and the process then runs on a password the file does not hold. Nothing
	// reveals that until the next restart.
	s.pwWrite.Lock()
	defer s.pwWrite.Unlock()

	// Validate before the file, not after. A hash that cannot be decoded must
	// never reach disk: the file outlives this process, and the next start
	// would refuse to boot with no way left to log in and fix it.
	if err := security.ValidateHash(hash); err != nil {
		s.logWarn("refusing to store an unusable password hash", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Persist first, switch in memory second: a failed write must not leave the
	// process running on a password that survives no restart.
	if s.cfg.PasswordFile != "" {
		if err := writeFileAtomic(s.cfg.PasswordFile, []byte(hash), 0o600); err != nil {
			s.logWarn("password file write failed", "err", err)
			writeErr(w, http.StatusInternalServerError, "failed to save (check KUZTDS_ADMIN_PASSWORD_FILE)")
			return
		}
	}
	if err := s.publish(hash); err != nil {
		s.logWarn("password hash published after the write failed validation", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Every session issued under the old password is now invalid, including
	// this one — hand the caller a fresh cookie and CSRF token so the panel
	// keeps working, and drop the old token from the store.
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.cfg.Sessions.Delete(r.Context(), c.Value)
	}
	csrf, err := s.issueSession(w, r)
	if err != nil {
		s.logWarn("session reissue after password change failed", "err", err)
		writeJSON(w, http.StatusOK, map[string]string{"status": "changed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "changed", "csrf": csrf})
}
