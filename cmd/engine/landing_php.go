package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/render"
)

// errPHPOff is returned when a PHP file is requested and EMTDS_PHP_URL is empty.
var errPHPOff = errors.New("php url is empty")

// errLandingTooBig is returned when a landing body exceeds landingBodyLimit.
var errLandingTooBig = errors.New("landing body exceeds 8 MiB")

// phpResponse is one landing reply from disk or from PHP.
// Body is at most landingBodyLimit bytes. Status is the HTTP status.
// Location and ContentType are copied only when PHP sent those headers.
type phpResponse struct {
	Body        []byte
	Status      int
	Location    string
	ContentType string
}

// readEntry loads the landing file named name.
// r is the visitor request, used for the PHP query and headers.
// id is the landing id. deps supplies the client IP and the click id.
// A PHP name is fetched from the sidecar. Any other file is read from disk
// with status 200 and empty Location and ContentType.
// err is errPHPOff, a not-exist error, or a read failure.
func (s *landingServer) readEntry(r *http.Request, id, name string, deps render.MacroDeps) (phpResponse, error) {
	if phpEntry(name) {
		if s == nil || s.phpURL == "" {
			return phpResponse{}, errPHPOff
		}
		ip := deps.IP
		if ip == "" && r != nil {
			ip = landingClientIP(r)
		}
		return s.proxyPHP(r, id, name, ip, deps.CID, deps.Origin)
	}
	full, ok := s.fileInLanding(id, name)
	if !ok {
		return phpResponse{}, os.ErrNotExist
	}
	f, err := os.Open(full)
	if err != nil {
		return phpResponse{}, err
	}
	defer f.Close()
	body, err := readLimited(f)
	if err != nil {
		return phpResponse{}, err
	}
	return phpResponse{Body: body, Status: http.StatusOK}, nil
}

// servePHPAsset proxies one PHP or extensionless asset.
// id and rel name the landing and the file. The upstream status and body
// are passed through. Content-Type is the upstream value, or text/html when
// that header is empty. Location is copied when PHP sent it.
// PHP disabled is 503. A failed fetch is 502.
func (s *landingServer) servePHPAsset(w http.ResponseWriter, r *http.Request, id, rel string) {
	if s == nil || s.phpURL == "" {
		s.warnPHP()
		http.Error(w, "php unavailable", http.StatusServiceUnavailable)
		return
	}
	cid := ""
	if r != nil {
		cid = r.URL.Query().Get("cid")
	}
	resp, err := s.proxyPHP(r, id, rel, landingClientIP(r), cid, "")
	if err != nil {
		if s.log != nil {
			s.log.Warn("engine: landing php fetch failed", "landing", id, "err", err)
		}
		http.Error(w, "landing unavailable", http.StatusBadGateway)
		return
	}
	if resp.Status < 100 {
		resp.Status = http.StatusBadGateway
	}
	ct := resp.ContentType
	if ct == "" {
		ct = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	if resp.Location != "" {
		w.Header().Set("Location", resp.Location)
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// proxyPHP fetches id/name from the PHP origin.
// r supplies the query and the visitor headers that are copied.
// ip is X-Forwarded-For. cid is sent as X-Emtds-Cid.
// origin is sent as X-Emtds-Origin and may be empty.
// The result carries the body, the status, Location and Content-Type.
// err is set on transport failure, a timeout, or an oversized body.
// A non-2xx status is returned with its body and a nil error.
// Every request that is sent is counted in the landing metrics with its time
// and its failure, if any.
func (s *landingServer) proxyPHP(r *http.Request, id, name, ip, cid, origin string) (phpResponse, error) {
	if s == nil || s.phpURL == "" {
		return phpResponse{}, errPHPOff
	}
	start := time.Now()
	resp, err := s.fetchPHP(r, id, name, ip, cid, origin)
	s.stats.observe(time.Since(start), resp.Status, err)
	return resp, err
}

// fetchPHP is the request to the PHP origin that proxyPHP counts. Arguments and
// results are those of proxyPHP; the origin is known to be set.
func (s *landingServer) fetchPHP(r *http.Request, id, name, ip, cid, origin string) (phpResponse, error) {
	parent := context.Background()
	if r != nil {
		parent = r.Context()
	}
	ctx, cancel := context.WithTimeout(parent, landingPHPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.phpTarget(id, name, rawQuery(r)), nil)
	if err != nil {
		return phpResponse{}, err
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	req.Header.Set("X-Emtds-Cid", cid)
	req.Header.Set("X-Emtds-Origin", origin)
	if r != nil {
		if ua := r.Header.Get("User-Agent"); ua != "" {
			req.Header.Set("User-Agent", ua)
		}
		if lang := r.Header.Get("Accept-Language"); lang != "" {
			req.Header.Set("Accept-Language", lang)
		}
		if ref := r.Header.Get("Referer"); ref != "" {
			req.Header.Set("Referer", ref)
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return phpResponse{}, err
	}
	defer resp.Body.Close()
	body, err := readLimited(resp.Body)
	if err != nil {
		return phpResponse{}, err
	}
	return phpResponse{
		Body:        body,
		Status:      resp.StatusCode,
		Location:    rewritePHPLocation(resp.Header.Get("Location"), s.phpURL, id),
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// phpTarget is the URL PHP is asked for.
// id and name are the landing and the file. rawQuery is appended as-is.
// The result is phpURL/id/name with an optional query. There is no error.
func (s *landingServer) phpTarget(id, name, rawQuery string) string {
	u := s.phpURL + "/" + url.PathEscape(id) + "/" + escapeRel(name)
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

// escapeRel percent-encodes each segment of a relative path.
// name uses slashes. The result is safe to append to the PHP origin.
func escapeRel(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// rawQuery returns the visitor query without a leading question mark.
// A nil request returns "". There is no error.
func rawQuery(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.RawQuery
}

// warnPHP logs that PHP is disabled, at most once a minute.
// There is no return. A nil log drops the warning.
func (s *landingServer) warnPHP() {
	if s == nil {
		return
	}
	s.mu.Lock()
	now := time.Now()
	if !s.phpWarn.IsZero() && now.Sub(s.phpWarn) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.phpWarn = now
	s.mu.Unlock()
	if s.log != nil {
		s.log.Warn("engine: php landings disabled")
	}
}

// rewritePHPLocation translates a Location header from the PHP sidecar into an
// engine-relative path. phpURL is EMTDS_PHP_URL (e.g. http://php:80). id is the
// landing id passed to proxyPHP. Two cases are rewritten: (1) an absolute URL
// whose host equals the host or hostname of phpURL and whose path starts with
// /<id>/; (2) a relative path that starts with /<id>/. Both become /l/<id>/...
// with the original query preserved. External URLs and anything else are returned
// unchanged. An empty loc or a parse error returns loc unmodified.
func rewritePHPLocation(loc, phpURL, id string) string {
	if loc == "" || id == "" {
		return loc
	}
	u, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	prefix := "/" + id + "/"
	var uPath, uQuery string
	if u.Host != "" {
		// Absolute URL: check whether the host belongs to the PHP sidecar.
		p, err := url.Parse(phpURL)
		if err != nil || (u.Host != p.Host && u.Hostname() != p.Hostname()) {
			return loc
		}
		uPath = u.Path
		uQuery = u.RawQuery
	} else if strings.HasPrefix(u.Path, "/") {
		// Relative path starting with /.
		uPath = u.Path
		uQuery = u.RawQuery
	} else {
		return loc
	}
	if !strings.HasPrefix(uPath, prefix) {
		return loc
	}
	result := "/l" + uPath
	if uQuery != "" {
		result += "?" + uQuery
	}
	return result
}

// readLimited reads at most landingBodyLimit bytes.
// errLandingTooBig is returned when the reader still has more.
// A short read returns the bytes and the reader error.
func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, landingBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > landingBodyLimit {
		return nil, errLandingTooBig
	}
	return b, nil
}
