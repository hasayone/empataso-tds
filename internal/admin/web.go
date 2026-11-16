package admin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/vendor
var vendorFS embed.FS

// indexTag identifies the embedded SPA bytes for the life of the binary.
var indexTag = contentTag(indexHTML)

// contentTag returns the first 16 bytes of the SHA-256 of b as hex.
// It does not fail.
func contentTag(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// serveUI serves the embedded SPA at the root with an ETag per encoding.
// The page is the same bytes on every request, so the tag is computed once.
// The identity body carries the strong tag "<hash>". The body that
// gzipMiddleware compresses carries W/"<hash>-gzip": its bytes depend on the
// compressor, so only a weak tag is honest. Vary: Accept-Encoding keeps
// caches from mixing the two. A matching If-None-Match answers 304 without
// a body; Cache-Control no-cache from securityHeaders makes the browser ask.
func serveUI(w http.ResponseWriter, r *http.Request) {
	if answerETag(w, r, indexTag) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

// answerETag sets the ETag of the body whose content tag is tag and answers 304 when the
// request already holds it. w and r are the response and request. The tag is strong for the
// identity body and weak ("<tag>-gzip") when gzipMiddleware compresses the answer; Vary:
// Accept-Encoding keeps caches from mixing the two. It returns true when it wrote the 304,
// and the caller must then write nothing more. It does not fail.
func answerETag(w http.ResponseWriter, r *http.Request, tag string) bool {
	h := w.Header()
	addVary(h, "Accept-Encoding")
	etag := `"` + tag + `"`
	if gzipEligible(r) {
		etag = `W/"` + tag + `-gzip"`
	}
	h.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// etagMatches reports whether the If-None-Match value header lists etag.
// Comparison is weak, as RFC 9110 requires for If-None-Match: a W/ prefix
// is ignored on both sides. "*" matches any tag. An empty header does not match.
func etagMatches(header, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(header, ",") {
		tag := strings.TrimSpace(part)
		if tag == "*" || (tag != "" && strings.TrimPrefix(tag, "W/") == want) {
			return true
		}
	}
	return false
}

// serveAssets возвращает обработчик статики из web/vendor по префиксу /assets/.
// Каждый ответ несёт ETag по хэшу содержимого файла (считается один раз при создании
// обработчика), совпавший If-None-Match даёт 304. Cache-Control задаёт securityHeaders:
// private, max-age=86400. Каталоги и пути с завершающим / — 404.
// Параметры: нет.
// Возврат: http.Handler.
// Ошибки: если fs.Sub не может открыть web/vendor или файлы не читаются, возвращается
// http.NotFoundHandler (404 на любой путь).
func serveAssets() http.Handler {
	sub, err := fs.Sub(vendorFS, "web/vendor")
	if err != nil {
		return http.NotFoundHandler()
	}
	tags, err := vendorTags()
	if err != nil {
		return http.NotFoundHandler()
	}
	files := http.StripPrefix("/assets/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/assets/")
		if rel == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		clean := path.Clean(rel)
		if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
			http.NotFound(w, r)
			return
		}
		tag, ok := tags[clean]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if answerETag(w, r, tag) {
			return
		}
		files.ServeHTTP(w, r)
	})
}

// vendorTags returns the contentTag of every file of web/vendor, keyed by slash path below it.
// The files are embedded, so the tags are computed once per process on first use.
// The error is the one assetTags returned.
var vendorTags = sync.OnceValues(func() (map[string]string, error) {
	sub, err := fs.Sub(vendorFS, "web/vendor")
	if err != nil {
		return nil, err
	}
	return assetTags(sub)
})

// assetTags maps the slash path of every file in fsys to the contentTag of its bytes.
// fsys is the embedded asset tree. It returns an error when a file cannot be read.
func assetTags(fsys fs.FS) (map[string]string, error) {
	tags := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		tags[p] = contentTag(b)
		return nil
	})
	return tags, err
}
