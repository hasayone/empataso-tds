package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

const (
	// uniqCookieLabel separates the uniqueness-cookie key from the click id key.
	uniqCookieLabel = "uniq-cookie"
	// uniqCookieMACBytes is the size of the MAC kept in a uniqueness cookie.
	uniqCookieMACBytes = 16
)

// uniqKey returns the key of signed uniqueness cookies.
// A nil signer uses the process key.
func (s *cidSigner) uniqKey() []byte {
	if s == nil {
		return processKeySigner().derive(uniqCookieLabel)
	}
	return s.derive(uniqCookieLabel)
}

// uniqCookieValue builds the cookie value for one campaign and one cookie name.
//
// key is the derived uniqueness key. groupID and name are bound into the MAC,
// so a cookie of another campaign or of the offer step does not pass. exp is
// the unix expiry. The value is the decimal expiry, a dot and the MAC. It holds
// neither the address nor the user-agent.
func uniqCookieValue(key []byte, groupID, name string, exp int64) string {
	return strconv.FormatInt(exp, 10) + "." + uniqCookieMAC(key, groupID, name, exp)
}

// uniqCookieMAC returns the base64url MAC of groupID, name and exp.
func uniqCookieMAC(key []byte, groupID, name string, exp int64) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(groupID))
	m.Write([]byte{0})
	m.Write([]byte(name))
	m.Write([]byte{0})
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(exp))
	m.Write(buf[:])
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:uniqCookieMACBytes])
}

// uniqCookieValid reports whether value is a signature this key would issue
// for groupID and name, and whether exp is still ahead of now.
// A plain legacy value ("1") and a truncated or altered MAC are not valid.
func uniqCookieValid(key []byte, groupID, name, value string, now time.Time) bool {
	expText, mac, ok := strings.Cut(value, ".")
	if !ok || expText == "" || mac == "" {
		return false
	}
	exp, err := strconv.ParseInt(expText, 10, 64)
	if err != nil || exp <= now.Unix() {
		return false
	}
	want := uniqCookieMAC(key, groupID, name, exp)
	return hmac.Equal([]byte(mac), []byte(want))
}

// readUniqCookie reports whether the request carries a live signed cookie.
//
// r supplies the cookie. signer is the engine key (nil uses the process key).
// groupID and name select the campaign and the step. now is the clock.
// It returns false when the cookie is missing, expired or signed for something else.
func readUniqCookie(r *http.Request, signer *cidSigner, groupID, name string, now time.Time) bool {
	c, err := r.Cookie(name)
	if err != nil {
		return false
	}
	return uniqCookieValid(signer.uniqKey(), groupID, name, c.Value, now)
}

// setUniqCookie sends a signed uniqueness cookie for the campaign window.
//
// w receives the cookie. r decides the Secure flag. signer is the engine key.
// grp supplies the id and UniqSeconds (capped at 8760 hours, a day when unset).
// name is the cookie, different for the landing step and the offer step.
// now is the clock the expiry is counted from. The value has no visitor data.
func setUniqCookie(w http.ResponseWriter, r *http.Request, signer *cidSigner, grp *config.Group, name string, now time.Time) {
	ttl := config.UniqCookieSeconds(grp.UniqSeconds)
	exp := now.Add(time.Duration(ttl) * time.Second).Unix()
	setEngineCookie(w, r, &http.Cookie{
		Name: name, Value: uniqCookieValue(signer.uniqKey(), grp.ID, name, exp),
		Path: "/", MaxAge: ttl, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}
