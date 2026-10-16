package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/render"
	"github.com/egerkuzma/kuztds/internal/server"
)

// landingBodyLimit is the maximum landing body read from disk or from PHP.
const landingBodyLimit = 8 << 20

// landingPHPTimeout bounds one PHP request, including the body.
const landingPHPTimeout = 10 * time.Second

// landingServer serves local landing files and proxies PHP to a sidecar.
// dir is the landings root. phpURL empty means PHP is disabled.
// lookup reads Entry from the catalog and may be nil.
// log receives a missing-file warning once per landing and may be nil.
type landingServer struct {
	dir    string
	phpURL string
	lookup func(id string) (config.Landing, bool)
	log    *slog.Logger
	client *http.Client

	mu      sync.Mutex
	warned  map[string]struct{}
	phpWarn time.Time
}

// newLandingServer returns a server for dir.
// phpURL is the PHP origin, for example http://php:80. An empty value
// disables PHP. lookup reads a landing by id and may be nil.
// log is warned once per missing landing; a nil log drops the warning.
// There is no error: a missing directory is reported later, per landing.
func newLandingServer(dir, phpURL string, lookup func(string) (config.Landing, bool), log *slog.Logger) *landingServer {
	return &landingServer{
		dir:    dir,
		phpURL: strings.TrimRight(strings.TrimSpace(phpURL), "/"),
		lookup: lookup,
		log:    log,
		client: &http.Client{
			Timeout: landingPHPTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		warned: map[string]struct{}{},
	}
}

// landingsDir returns the directory local landings are read from.
// explicit is KUZTDS_LANDINGS_DIR. groupsFile is KUZTDS_GROUPS_FILE.
// An explicit path wins. Otherwise, when groupsFile is set, the directory
// is "landings" next to that file. Both empty returns "" and nothing is served.
func landingsDir(explicit, groupsFile string) string {
	if explicit != "" {
		return explicit
	}
	if groupsFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(groupsFile), "landings")
}

// serveLocalLanding writes the landing chosen for this click.
// id is the catalog id. deps carries macros, the click id and the offer URL.
// A nil server answers 503. serveEntry reports a missing file as 404.
func (d *engineDeps) serveLocalLanding(w http.ResponseWriter, r *http.Request, id string, deps render.MacroDeps) {
	if d == nil || d.landServe == nil {
		http.Error(w, "landing unavailable", http.StatusServiceUnavailable)
		return
	}
	d.landServe.serveEntry(w, r, id, deps)
}

// serveLandingAsset is the /l/{id}/{path} route.
// A nil server answers 404. The response is the file or the PHP body.
func (d *engineDeps) serveLandingAsset(w http.ResponseWriter, r *http.Request) {
	if d == nil || d.landServe == nil {
		http.NotFound(w, r)
		return
	}
	d.landServe.serveAsset(w, r)
}

// serveEntry writes the landing HTML for landingID.
// w is the visitor response. r is the click. landingID is the catalog id.
// deps supplies macros, including the offer path and the click id.
// A 2xx body is expanded and gets a base href. A 3xx with Location is
// returned as that redirect, without macros. Other statuses pass the body
// through. A missing entry is 404. PHP disabled is 503. Other failures are 502.
func (s *landingServer) serveEntry(w http.ResponseWriter, r *http.Request, landingID string, deps render.MacroDeps) {
	if s == nil || !safeLandingID(landingID) {
		http.NotFound(w, r)
		return
	}
	name, ok := s.resolveEntry(landingID)
	if !ok {
		s.warnMissing(landingID, os.ErrNotExist)
		http.NotFound(w, r)
		return
	}
	resp, err := s.readEntry(r, landingID, name, deps)
	if err != nil {
		if err == errPHPOff {
			s.warnPHP()
			http.Error(w, "php unavailable", http.StatusServiceUnavailable)
			return
		}
		if os.IsNotExist(err) {
			s.warnMissing(landingID, err)
			http.NotFound(w, r)
			return
		}
		if s.log != nil {
			s.log.Warn("engine: landing read failed", "landing", landingID, "err", err)
		}
		http.Error(w, "landing unavailable", http.StatusBadGateway)
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		if resp.Status >= 300 && resp.Status < 400 && resp.Location != "" {
			w.Header().Set("Location", resp.Location)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)
		return
	}
	html := injectBase(render.Expand(string(resp.Body), deps), landingID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

// serveAsset writes one file under /l/{id}/{path}.
// A directory, a hidden name, or a path that escapes the landing is 404.
// A .php file or a name without an extension is proxied to PHP.
// Any other file is static, with Cache-Control public for five minutes.
func (s *landingServer) serveAsset(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.NotFound(w, r)
		return
	}
	id, rel, ok := landingAssetPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if phpEntry(rel) {
		s.servePHPAsset(w, r, id, rel)
		return
	}
	full, ok := s.fileInLanding(id, rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFile(w, r, full)
}

// resolveEntry picks the file name for landingID.
// The catalog Entry wins. Otherwise index.php is used when it exists,
// and index.html after that. ok is false when nothing can be named.
func (s *landingServer) resolveEntry(landingID string) (string, bool) {
	if s.lookup != nil {
		if item, ok := s.lookup(landingID); ok {
			if name, good := safeRel(strings.TrimSpace(item.Entry)); good {
				return name, true
			}
		}
	}
	if s.fileExists(landingID, "index.php") {
		return "index.php", true
	}
	if s.fileExists(landingID, "index.html") {
		return "index.html", true
	}
	return "", false
}

// fileExists reports whether rel is a regular file inside the landing.
// landingID and rel are the directory and the relative name.
func (s *landingServer) fileExists(landingID, rel string) bool {
	full, ok := s.fileInLanding(landingID, rel)
	if !ok {
		return false
	}
	fi, err := os.Stat(full)
	return err == nil && !fi.IsDir()
}

// fileInLanding joins rel onto dir/landingID and refuses an escape.
// The result is the absolute path. ok is false when the name is unsafe
// or the landings directory is unset.
func (s *landingServer) fileInLanding(landingID, rel string) (string, bool) {
	if s == nil || s.dir == "" || !safeLandingID(landingID) {
		return "", false
	}
	clean, ok := safeRel(rel)
	if !ok {
		return "", false
	}
	base := filepath.Join(s.dir, landingID)
	full := filepath.Join(base, filepath.FromSlash(clean))
	back, err := filepath.Rel(base, full)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}

// warnMissing logs one missing landing.
// landingID is the catalog id. err is the stat or read error.
// Later requests for the same id stay quiet. There is no return.
func (s *landingServer) warnMissing(landingID string, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.warned == nil {
		s.warned = map[string]struct{}{}
	}
	_, already := s.warned[landingID]
	if !already {
		s.warned[landingID] = struct{}{}
	}
	s.mu.Unlock()
	if already || s.log == nil {
		return
	}
	s.log.Warn("engine: landing entry missing", "landing", landingID, "err", err)
}

// landingAssetPath splits /l/{id}/{file}.
// p is the URL path. id and rel are the landing and the file inside it.
// ok is false for a directory, a hidden name, or a path that escapes.
func landingAssetPath(p string) (id, rel string, ok bool) {
	if !strings.HasPrefix(p, "/l/") {
		return "", "", false
	}
	rest := strings.TrimPrefix(p, "/l/")
	id, rel, found := strings.Cut(rest, "/")
	if !found || rel == "" || strings.HasSuffix(rel, "/") {
		return "", "", false
	}
	if !safeLandingID(id) {
		return "", "", false
	}
	clean, good := safeRel(rel)
	if !good {
		return "", "", false
	}
	return id, clean, true
}

// safeLandingID reports whether id can be a landing directory name.
// Empty, hidden, absolute and slash-containing ids are rejected.
func safeLandingID(id string) bool {
	if id == "" || len(id) > 128 || strings.HasPrefix(id, ".") {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.':
		default:
			return false
		}
	}
	return true
}

// safeRel cleans rel and rejects escapes and hidden segments.
// rel is a URL path inside one landing. The result is the cleaned relative
// path. ok is false for "..", an absolute path, or a segment starting with '.'.
func safeRel(rel string) (string, bool) {
	if rel == "" || strings.Contains(rel, "\\") || strings.ContainsRune(rel, 0) {
		return "", false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || path.IsAbs(cleaned) || strings.HasPrefix(cleaned, "/") || strings.Contains(cleaned, "..") {
		return "", false
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	return cleaned, true
}

// phpEntry reports whether name must be executed by PHP.
// A .php suffix and a name with no extension are both proxied.
func phpEntry(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".php" || ext == ""
}

// injectBase inserts the landing base href just after the first head tag.
// body is the HTML. id is the landing id, already checked by safeLandingID.
// When the document has no head tag the tag is placed at the start.
func injectBase(body, id string) string {
	tag := `<base href="/l/` + id + `/">`
	at := headTagEnd(body)
	if at < 0 {
		return tag + body
	}
	return body[:at] + tag + body[at:]
}

// headTagEnd returns the index just after the first opening head tag.
// body is the HTML. The result is -1 when there is no head tag.
// A header element is not a head tag. There is no error return.
func headTagEnd(body string) int {
	lower := strings.ToLower(body)
	from := 0
	for from < len(lower) {
		i := strings.Index(lower[from:], "<head")
		if i < 0 {
			return -1
		}
		i += from
		if i+5 < len(body) {
			c := body[i+5]
			if c == '>' || c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '/' {
				end := strings.IndexByte(body[i:], '>')
				if end < 0 {
					return -1
				}
				return i + end + 1
			}
		}
		from = i + 5
	}
	return -1
}

// offerStepURL is the address of the offer step for this click.
// alias is the campaign path segment. cid is the click id. rawQuery is the
// visitor query, kept except for cid, which is replaced.
// The result starts with /. There is no error return.
func offerStepURL(alias, cid, rawQuery string) string {
	alias = strings.Trim(alias, "/")
	u := "/" + alias + "/offer?cid=" + cid
	if alias == "" {
		u = "/offer?cid=" + cid
	}
	if rest := queryWithout(rawQuery, "cid"); rest != "" {
		u += "&" + rest
	}
	return u
}

// queryWithout drops every pair whose key equals name.
// raw is the request RawQuery. The remaining pairs keep their original
// encoding and order. An empty raw returns empty. There is no error return.
func queryWithout(raw, name string) string {
	if raw == "" || name == "" {
		return raw
	}
	var kept []string
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		k, _, _ := strings.Cut(pair, "=")
		if k == name {
			continue
		}
		kept = append(kept, pair)
	}
	return strings.Join(kept, "&")
}

// reusedCID reports whether s is a click id the offer step may keep.
// s must be exactly ten lowercase hex characters. Anything else is a new id.
func reusedCID(s string) bool {
	if len(s) != 10 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// landingClientIP is the visitor address already stored on the request.
// The realip middleware wins. Otherwise the host of RemoteAddr is used.
// An address that cannot be parsed returns "".
func landingClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if ip := server.ClientIP(r.Context()); ip.IsValid() {
		return ip.String()
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
