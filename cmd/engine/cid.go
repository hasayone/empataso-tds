package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egerkuzma/kuztds/internal/atomicfile"
)

const (
	// cidAlphabet is the base36 alphabet of a click id, as in imported Keitaro ids.
	cidAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	// cidRandLen is the number of random characters of a click id.
	cidRandLen = 9
	// cidSigLen is the number of signature characters: a foreign id passes with
	// probability 1/36^3 = 1/46656.
	cidSigLen = 3
	// cidLen is the full click id length: random part plus signature.
	cidLen = cidRandLen + cidSigLen
	// legacyCIDLen is the length of the legacy 10-hex click id: ten lowercase hex characters.
	legacyCIDLen = 10
	// cidKeyFile is the name of the generated signing key inside the state directory.
	cidKeyFile = "cid.key"
	// cidKeyBytes is the size of a generated signing key and the least size of KUZTDS_CID_KEY.
	cidKeyBytes = 32
	// originSigBytes is the MAC length kept in an origin token.
	originSigBytes = 16
	// originMACPrefix separates the origin MAC from other uses of the click id key.
	originMACPrefix = "origin:"
)

// cidSigner issues and verifies click ids: nine random base36 characters and
// three characters of an HMAC over them. A nil *cidSigner works with a key that
// lives only as long as the process.
type cidSigner struct {
	key []byte
	// sticky is the key of the remembered-stream tokens, derived from key once
	// when the signer is built.
	sticky []byte
}

// keyedSigner builds a signer over key and derives its sticky key.
// key is the signing key. It returns the signer; it does not fail.
func keyedSigner(key []byte) *cidSigner {
	s := &cidSigner{key: key}
	s.sticky = s.derive(stickyKeyLabel)
	return s
}

// stickyKey returns the key of the remembered-stream tokens.
// A nil signer uses the process key. A signer built without keyedSigner derives
// the key on each call.
func (s *cidSigner) stickyKey() []byte {
	if s == nil {
		return processKeySigner().stickyKey()
	}
	if len(s.sticky) == 0 {
		return s.derive(stickyKeyLabel)
	}
	return s.sticky
}

var (
	processOnce   sync.Once
	processSigner *cidSigner
)

// processKeySigner returns the signer with a random key that is never stored.
func processKeySigner() *cidSigner {
	processOnce.Do(func() {
		k := make([]byte, cidKeyBytes)
		_, _ = rand.Read(k)
		processSigner = keyedSigner(k)
	})
	return processSigner
}

// cidKeyPath returns where the signing key file lives.
//
// override is KUZTDS_CID_KEY_FILE; groupsFile is KUZTDS_GROUPS_FILE. The file
// sits in the state directory next to the groups file, the volume the engine
// already writes. It returns "" when neither is set: the key then lives only in
// the process.
func cidKeyPath(override, groupsFile string) string {
	if override = strings.TrimSpace(override); override != "" {
		return override
	}
	if groupsFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(groupsFile), cidKeyFile)
}

// newCIDSigner builds the signer.
//
// env is the KUZTDS_CID_KEY value: it must be at least 32 bytes, a shorter one
// is ignored with a warning. path is the key file (see cidKeyPath). A missing
// file is published atomically with mode 0600 and a fresh random key (never visible
// half-written); a file that appears meanwhile is read instead. Any other read error, and a file that is
// empty or not 32 hex-encoded bytes, leaves the file alone and gives a
// process-lifetime key with a warning: ids issued before a restart then fail
// verification, which only makes the offer step issue a new id. log may be nil.
func newCIDSigner(env, path string, log *slog.Logger) *cidSigner {
	warn := func(msg string, args ...any) {
		if log != nil {
			log.Warn(msg, args...)
		}
	}
	if env = strings.TrimSpace(env); env != "" {
		if len(env) >= cidKeyBytes {
			return keyedSigner([]byte(env))
		}
		warn("cid key: KUZTDS_CID_KEY is shorter than 32 bytes, ignored", "len", len(env))
	}
	if path == "" {
		return processKeySigner()
	}
	key, err := loadCIDKey(path)
	if err != nil {
		warn("cid key: not usable, using a process key", "path", path, "err", err)
		return processKeySigner()
	}
	return keyedSigner(key)
}

// loadCIDKey reads the key file, creating it when it does not exist.
//
// path is the key file. It returns the key, or an error for an unreadable,
// malformed or uncreatable file; it never overwrites an existing file.
func loadCIDKey(path string) ([]byte, error) {
	key, err := readCIDKey(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	fresh := make([]byte, cidKeyBytes)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	created, err := atomicfile.CreateIfAbsent(path, []byte(hex.EncodeToString(fresh)+"\n"), 0o600)
	if err != nil {
		return nil, err
	}
	if !created {
		// Another process published the file first; it is complete by construction.
		return readCIDKey(path)
	}
	return fresh, nil
}

// readCIDKey reads and decodes the key file at path.
// It returns fs.ErrNotExist for a missing file and an error for any file that
// is not 32 hex-encoded bytes.
func readCIDKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, err
	}
	if len(key) != cidKeyBytes {
		return nil, errors.New("key file does not hold 32 bytes")
	}
	return key, nil
}

// signKey returns the signing key of s, or the process key when s is nil.
func (s *cidSigner) signKey() []byte {
	if s == nil || len(s.key) == 0 {
		return processKeySigner().key
	}
	return s.key
}

// sign returns the signature characters for the random part body.
func (s *cidSigner) sign(body string) string {
	m := hmac.New(sha256.New, s.signKey())
	m.Write([]byte(body))
	base := uint32(len(cidAlphabet))
	n := binary.BigEndian.Uint32(m.Sum(nil)[:4]) % (base * base * base)
	return string([]byte{cidAlphabet[n/(base*base)], cidAlphabet[n/base%base], cidAlphabet[n%base]})
}

// newCID returns a new twelve character click id: nine random characters and
// their signature.
func (s *cidSigner) newCID() string {
	body := make([]byte, 0, cidRandLen)
	// 252 is the largest multiple of 36 below 256: bytes above it are skipped,
	// so every alphabet character is equally likely.
	const limit = 256 - 256%len(cidAlphabet)
	buf := make([]byte, 32)
	for len(body) < cidRandLen {
		_, _ = rand.Read(buf)
		for _, b := range buf {
			if int(b) < limit && len(body) < cidRandLen {
				body = append(body, cidAlphabet[int(b)%len(cidAlphabet)])
			}
		}
	}
	return string(body) + s.sign(string(body))
}

// reusedCID reports whether c is a click id the offer step may keep: ten
// lowercase hex characters (the legacy 10-hex format, accepted without an
// expiry) or twelve base36 characters whose last three are a valid signature.
// Any other value, imported ids included, is not reusable, so a visitor cannot
// attach a click to a foreign id.
// signOrigin returns a signed origin token for group gid.
//
// gid is the first-entry campaign id. The token is gid plus a dot plus a
// base64url MAC. An empty gid returns "".
func (s *cidSigner) signOrigin(gid string) string {
	gid = strings.TrimSpace(gid)
	if gid == "" {
		return ""
	}
	m := hmac.New(sha256.New, s.signKey())
	m.Write([]byte(originMACPrefix))
	m.Write([]byte(gid))
	sig := base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:originSigBytes])
	return gid + "." + sig
}

// verifyOrigin checks an origin query token.
//
// tok is the value of ?origin=. It returns the group id and true when the
// signature matches; a missing dot, a bad encoding or a wrong MAC yield "", false.
func (s *cidSigner) verifyOrigin(tok string) (string, bool) {
	tok = strings.TrimSpace(tok)
	dot := strings.LastIndex(tok, ".")
	if dot <= 0 || dot == len(tok)-1 {
		return "", false
	}
	gid, sig := tok[:dot], tok[dot+1:]
	if gid == "" || sig == "" {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(raw) != originSigBytes {
		return "", false
	}
	m := hmac.New(sha256.New, s.signKey())
	m.Write([]byte(originMACPrefix))
	m.Write([]byte(gid))
	want := m.Sum(nil)[:originSigBytes]
	if !hmac.Equal(raw, want) {
		return "", false
	}
	return gid, true
}

func (s *cidSigner) reusedCID(c string) bool {
	switch len(c) {
	case legacyCIDLen:
		for i := 0; i < len(c); i++ {
			if (c[i] < '0' || c[i] > '9') && (c[i] < 'a' || c[i] > 'f') {
				return false
			}
		}
		return true
	case cidLen:
		for i := 0; i < len(c); i++ {
			if strings.IndexByte(cidAlphabet, c[i]) < 0 {
				return false
			}
		}
		return hmac.Equal([]byte(c[cidRandLen:]), []byte(s.sign(c[:cidRandLen])))
	}
	return false
}
