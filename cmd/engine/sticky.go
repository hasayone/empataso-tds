package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/netip"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/router"
)

const (
	// stickyCookiePrefix starts the name of the cookie that holds a remembered stream.
	stickyCookiePrefix = "zts_"
	// stickyRedisTimeout bounds one Redis call of the remembered-stream store,
	// so a slow Redis never holds a click for long: a visit makes one read and
	// at most one write.
	stickyRedisTimeout = 150 * time.Millisecond
	// stickyTokenBytes is the size of the MAC kept in a token.
	stickyTokenBytes = 16
	// stickyTokenLen is the length of a token as text (base64url, no padding).
	stickyTokenLen = (stickyTokenBytes*8 + 5) / 6
	// stickyKeyLabel separates the sticky key from the click id key it derives from.
	stickyKeyLabel = "sticky-stream"
)

// derive returns a key for one purpose, derived from the signing key of s with
// HMAC-SHA256 over label. A nil signer derives from the process key.
func (s *cidSigner) derive(label string) []byte {
	m := hmac.New(sha256.New, s.signKey())
	m.Write([]byte(label))
	return m.Sum(nil)
}

// stickyMAC returns the opaque token of a stream.
//
// key is the derived sticky key, groupID the serving group and stream the
// stream name. The token is the first 16 bytes of HMAC-SHA256 over the group and
// the name, in base64url. It does not reveal the name and cannot be made for
// another stream or group without the key.
func stickyMAC(key []byte, groupID, stream string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(groupID))
	m.Write([]byte{0})
	m.Write([]byte(stream))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:stickyTokenBytes])
}

// stickyOn reports whether the group remembers the stream of a returning visitor.
//
// grp is the serving group; apiMode marks a request of the api client, which
// sends its own uniqueness and gets no cookie. The mode needs group weights, a
// uniqueness window to live for, and a store: the cookie, or Redis for the ip
// method.
func (d *engineDeps) stickyOn(grp *config.Group, apiMode bool) bool {
	if apiMode || grp == nil || !grp.StickyStream || !grp.Weight || grp.UniqSeconds <= 0 {
		return false
	}
	return grp.UniqMethod == "cookie" || d.counters != nil
}

// selectStream chooses the stream of grp for the visitor.
//
// w receives the cookie, r identifies the visitor together with v.IP, v and deps
// feed the router, apiMode marks an api request. A sticky group first tries the stream
// remembered for the visitor and otherwise draws one and remembers it; the
// answer then gets Cache-Control private, no-store, since it depends on the
// visitor's cookie or address. Other groups just call router.Select.
// It returns the stream and true, or nil and false when none matches.
func (d *engineDeps) selectStream(w http.ResponseWriter, r *http.Request, grp *config.Group, v router.Visitor, deps router.Deps, apiMode bool) (*config.Stream, bool) {
	if !d.stickyOn(grp, apiMode) {
		return router.Select(grp, v, deps)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if s := d.stickyPick(r, grp, v, deps); s != nil {
		return s, true
	}
	s, ok := router.Select(grp, v, deps)
	if ok {
		d.stickyRemember(w, r, grp, v.IP, s.Name)
	}
	return s, ok
}

// stickyPick returns the stream remembered for the visitor when it still fits.
//
// r and v.IP identify the visitor; grp is the serving group; v and deps feed
// router.Why. deps carries the limiter, so a stream served again spends its
// limit once, exactly as a drawn stream does.
// It returns nil when nothing is remembered, the token matches no stream of the
// group, the stream is switched off, weighs nothing, fails a filter or has used
// up its limit. The caller then draws a stream as for a new visitor.
func (d *engineDeps) stickyPick(r *http.Request, grp *config.Group, v router.Visitor, deps router.Deps) *config.Stream {
	token := d.stickyRecall(r, grp, v.IP)
	if len(token) != stickyTokenLen {
		return nil
	}
	key := d.cids.stickyKey()
	for i := range grp.Streams {
		s := &grp.Streams[i]
		if !hmac.Equal([]byte(token), []byte(stickyMAC(key, grp.ID, s.Name))) {
			continue
		}
		if !s.Status || s.Weight <= 0 || router.Why(s, v, deps) != "" {
			return nil
		}
		return s
	}
	return nil
}

// stickyRecall reads the remembered token of the visitor.
//
// r supplies the cookie; grp picks the store (cookie or Redis by uniq_method);
// ip is the Redis key part. It returns "" when nothing is stored or Redis fails.
func (d *engineDeps) stickyRecall(r *http.Request, grp *config.Group, ip netip.Addr) string {
	if grp.UniqMethod == "cookie" {
		c, err := r.Cookie(stickyCookiePrefix + grp.ID)
		if err != nil {
			return ""
		}
		return c.Value
	}
	if d.counters == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(r.Context(), stickyRedisTimeout)
	defer cancel()
	token, err := d.counters.StickyGet(ctx, grp.ID, ip)
	if err != nil {
		return ""
	}
	return token
}

// stickyRemember stores the token of the stream that was drawn for the visitor.
//
// w receives the cookie, r gives the request context, grp is the serving group,
// ip is the visitor and stream the drawn stream name. The record lives for
// UniqSeconds. A Redis failure is ignored: the visitor is drawn again next time.
func (d *engineDeps) stickyRemember(w http.ResponseWriter, r *http.Request, grp *config.Group, ip netip.Addr, stream string) {
	token := stickyMAC(d.cids.stickyKey(), grp.ID, stream)
	if grp.UniqMethod == "cookie" {
		http.SetCookie(w, &http.Cookie{
			Name: stickyCookiePrefix + grp.ID, Value: token,
			Path: "/", MaxAge: grp.UniqSeconds, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		return
	}
	if d.counters == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), stickyRedisTimeout)
	defer cancel()
	_ = d.counters.StickySet(ctx, grp.ID, ip, token, secs(grp.UniqSeconds))
}
