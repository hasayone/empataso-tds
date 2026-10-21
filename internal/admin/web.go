package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/vendor
var vendorFS embed.FS

// serveUI serves the embedded SPA at the root.
func serveUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

// serveAssets возвращает обработчик статики из web/vendor по префиксу /assets/.
// Cache-Control задаёт securityHeaders (private, no-cache). Каталоги и пути с завершающим / — 404.
// Параметры: нет.
// Возврат: http.Handler.
// Ошибки: если fs.Sub не может открыть web/vendor, возвращается http.NotFoundHandler (404 на любой путь).
func serveAssets() http.Handler {
	sub, err := fs.Sub(vendorFS, "web/vendor")
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
		info, err := fs.Stat(sub, clean)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}
