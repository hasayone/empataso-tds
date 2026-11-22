package cron

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/atomicfile"
	"github.com/oschwald/maxminddb-golang"
)

const (
	// maxGeoBytes caps a MaxMind download and the database unpacked from it.
	maxGeoBytes = 1 << 30
	// dbipMaxBytes caps a DB-IP ASN download and the database unpacked from it.
	dbipMaxBytes = 256 << 20
	// dbipTimeout bounds one DB-IP ASN download together with unpacking it.
	dbipTimeout = 3 * time.Minute
	// dbipCityMaxBytes caps a DB-IP City Lite download and the database unpacked
	// from it; the file is about ten times the size of the ASN one.
	dbipCityMaxBytes = 768 << 20
	// dbipCityTimeout bounds one DB-IP City Lite download together with unpacking it.
	dbipCityTimeout = 15 * time.Minute
	// maxMindHost is the host that serves MaxMind databases and needs a license key.
	maxMindHost = "download.maxmind.com"
)

// geoTarget returns the file a source of the given kind writes and the
// environment variable that names it, for the error text.
func (r *Runner) geoTarget(kind string) (path, env string) {
	switch kind {
	case KindASN:
		return r.paths.ASNDB, "EMTDS_ASN_DB"
	case KindDBIPASN:
		return r.paths.DBIPASN, "EMTDS_DBIP_ASN_DB, EMTDS_ASN_DB or EMTDS_GEO_DB"
	}
	return r.paths.CityDB, "EMTDS_GEO_DB"
}

// isDBIP reports whether src downloads from DB-IP: every dbip_asn source and
// any other source on the DB-IP host, such as City Lite. Those are held to the
// DB-IP host, a size cap and a deadline.
func isDBIP(src GeoSource) bool {
	return src.Kind == KindDBIPASN || hostOf(cleanURL(src.URL)) == DBIPASNHost
}

// withCityFallback returns the sources of a geo_db run. A MaxMind city source
// without a key cannot download, so when no other city source can, the city
// sources become one DB-IP City Lite source, at the place of the first of them.
// It returns sources itself when the runner has no city database path, there is
// no city source, or one of them can download. The argument is not modified.
func (r *Runner) withCityFallback(sources []GeoSource) []GeoSource {
	if r.paths.CityDB == "" {
		return sources
	}
	hasCity := false
	for _, s := range sources {
		if s.Kind != KindCity {
			continue
		}
		if geoSourceReady(s) {
			return sources
		}
		hasCity = true
	}
	if !hasCity {
		return sources
	}
	out := make([]GeoSource, 0, len(sources))
	placed := false
	for _, s := range sources {
		switch {
		case s.Kind != KindCity:
			out = append(out, s)
		case !placed:
			out, placed = append(out, r.dbipCitySource()), true
		}
	}
	return out
}

// runGeoDB refreshes every source in cfg. ctx cancels the downloads.
// The string is one "kind: result" per source that finished or was skipped,
// joined with "; ". A source that fails is skipped, its error is joined into
// the returned error, and later sources still run. A MaxMind source with no
// account id and no license key is skipped with a message, not an error:
// the default config lists them before anyone has a key. The city one is the
// exception: DB-IP City Lite is downloaded in its place, see withCityFallback.
func (r *Runner) runGeoDB(ctx context.Context, cfg GeoDB) (string, error) {
	sources := r.withCityFallback(cfg.Sources)
	if len(sources) == 0 {
		return "no sources configured", nil
	}
	var done []string
	var errs []error
	for _, src := range sources {
		if src.User == "" && src.Password == "" && hostOf(cleanURL(src.URL)) == maxMindHost {
			done = append(done, src.Kind+": skipped, no MaxMind license key")
			continue
		}
		target, env := r.geoTarget(src.Kind)
		if target == "" {
			errs = append(errs, fmt.Errorf("%s: %s is not set for the cron service, nowhere to put the database", src.Kind, env))
			continue
		}
		msg, err := r.updateGeo(ctx, src, target)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Kind, err))
			continue
		}
		done = append(done, src.Kind+": "+msg)
	}
	return strings.Join(done, "; "), errors.Join(errs...)
}

// updateGeo downloads one database next to its target, checks that it is a
// working database of the expected kind, and only then renames it into place.
// The engine notices the new file on its reload interval.
//
// A URL with {YYYY-MM} is tried with the current month first and, on a 404,
// with the previous one. A DB-IP source (see isDBIP) is held to the DB-IP host
// (also for redirects), a size cap and a deadline, both larger for City Lite.
// Any failure leaves the file at target as it was.
// ctx cancels the download, src is the source and target the installed file.
// It returns a short result text, or an error that names the host or the
// problem with the file, never a URL with a key.
func (r *Runner) updateGeo(ctx context.Context, src GeoSource, target string) (string, error) {
	limit := int64(maxGeoBytes)
	if isDBIP(src) {
		timeout := dbipTimeout
		limit = r.dbipLimit
		if src.Kind == KindCity {
			limit, timeout = r.dbipCityLimit, dbipCityTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return r.fetchGeo(ctx, src, target, limit)
}

// fetchGeo downloads src and installs it at target, see updateGeo. limit caps
// the response and the unpacked database. ctx already carries the deadline.
func (r *Runner) fetchGeo(ctx context.Context, src GeoSource, target string, limit int64) (string, error) {
	dl, err := r.downloadGeo(ctx, src, target, limit)
	if statusOf(err) == http.StatusNotModified {
		return "not modified since " + dl.seen.LastModified, nil
	}
	if err != nil {
		return "", err
	}
	defer dl.body.Close()
	return r.installGeo(src.Kind, target, dl, limit)
}

// geoDownload is an answered download: the response body and headers, the URL
// that answered and what was remembered about the previous download.
type geoDownload struct {
	body   io.ReadCloser
	header http.Header
	url    string
	seen   GeoSeen
}

// downloadGeo fetches the database of src, trying each URL of geoURLs in turn
// and moving on to the next only after a 404.
// Ask only for what changed: the databases are tens of megabytes and come out a
// few times a week at most, so If-Modified-Since is sent only when the target
// is a database that works and the last download was from the same URL.
// limit caps the response size. The caller closes dl.body when err is nil.
// A 304 is returned as an error with that status, with dl.seen filled in.
func (r *Runner) downloadGeo(ctx context.Context, src GeoSource, target string, limit int64) (dl geoDownload, err error) {
	r.state(func(st *Status) { dl.seen = st.GeoSeen[src.Kind] })
	usable := geoFileUsable(target, src.Kind)
	user, pass := src.Credentials()
	req := fetchRequest{User: user, Pass: pass, Limit: limit}
	if isDBIP(src) {
		req.Allow = r.dbipAllowed
	}
	urls := geoURLs(src.URL, r.now())
	for i, u := range urls {
		req.URL, req.Header = u, nil
		if usable && dl.seen.URL == u && dl.seen.LastModified != "" {
			req.Header = map[string]string{"If-Modified-Since": dl.seen.LastModified}
		}
		dl.url = u
		dl.body, dl.header, err = r.send(ctx, req)
		if statusOf(err) == http.StatusNotFound && i+1 < len(urls) {
			continue
		}
		break
	}
	return dl, err
}

// geoFileBuilt opens the file at path and returns the build time of the
// database in it, when it is a database that fits a slot of the given kind.
// It is the quick check made before trusting what is installed; checkGeoDB is
// the full one made on a download. The error says the file is missing, does
// not open or holds a database of another type.
func geoFileBuilt(path, kind string) (time.Time, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer db.Close()
	if !kindMatches(kind, db.Metadata.DatabaseType) {
		return time.Time{}, fmt.Errorf("the file holds a %q database, not a %s one", db.Metadata.DatabaseType, kind)
	}
	return time.Unix(int64(db.Metadata.BuildEpoch), 0).UTC(), nil
}

// geoFileUsable reports whether geoFileBuilt accepts the file at path.
func geoFileUsable(path, kind string) bool {
	_, err := geoFileBuilt(path, kind)
	return err == nil
}

// installGeo unpacks the download into a temporary file next to target, checks
// it with checkGeoDB and only then replaces target with it, atomically. A file
// that is not a working database of the kind never replaces anything. When the
// target already works and its build is the one downloaded, it is left alone.
// The download is remembered either way, so the next run can ask for changes only.
// limit caps the unpacked size. It returns the result text, or the error of the
// unpacking, the check or the replacement.
func (r *Runner) installGeo(kind, target string, dl geoDownload, limit int64) (string, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	var have int
	r.state(func(st *Status) { have = st.GeoBuilt[kind] })
	var typ string
	var built int
	replaced, err := atomicfile.WriteFrom(target, 0o644,
		func(w io.Writer) error { return unpackMMDB(dl.body, w, limit) },
		func(tmp string) (err error) {
			if typ, built, err = r.checkGeoDB(tmp, kind); err != nil {
				return err
			}
			if have == built && geoFileUsable(target, kind) {
				return atomicfile.ErrKeep
			}
			return nil
		})
	if err != nil {
		return "", err
	}
	r.rememberGeo(kind, dl)
	if !replaced {
		return typ + " is up to date", nil
	}
	r.state(func(st *Status) {
		if st.GeoBuilt == nil {
			st.GeoBuilt = map[string]int{}
		}
		st.GeoBuilt[kind] = built
	})
	return typ + " updated", nil
}

// rememberGeo records the URL and Last-Modified of dl for the kind.
func (r *Runner) rememberGeo(kind string, dl geoDownload) {
	r.state(func(st *Status) {
		if st.GeoSeen == nil {
			st.GeoSeen = map[string]GeoSeen{}
		}
		st.GeoSeen[kind] = GeoSeen{URL: dl.url, LastModified: dl.header.Get("Last-Modified")}
	})
}

// checkGeoDB opens the database at path and decides whether it may replace the
// installed one. It must open, pass the library's structural check, have the
// type kindMatches expects for kind and, for dbip_asn, name an
// organization for r.probeIP.
// It returns the database type and the build epoch. The errors say what is
// wrong with the file and never include a path.
func (r *Runner) checkGeoDB(path, kind string) (typ string, built int, err error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return "", 0, errors.New("the download is not a MaxMind database")
	}
	defer db.Close()
	if err := db.Verify(); err != nil {
		return "", 0, fmt.Errorf("the database does not pass verification: %w", err)
	}
	typ, built = db.Metadata.DatabaseType, int(db.Metadata.BuildEpoch)
	if !kindMatches(kind, typ) {
		return "", 0, fmt.Errorf("expected a %s database, the download is %q", kind, typ)
	}
	if kind == KindDBIPASN {
		var rec struct {
			Organization string `maxminddb:"autonomous_system_organization"`
		}
		if err := db.Lookup(r.probeIP, &rec); err != nil || rec.Organization == "" {
			return "", 0, fmt.Errorf("the database does not pass verification: no organization for %s", r.probeIP)
		}
	}
	return typ, built, nil
}

// kindMatches reports whether a database of type dbType may fill a slot of the
// given kind. The comparison ignores case.
func kindMatches(kind, dbType string) bool {
	t := strings.ToLower(dbType)
	switch kind {
	case KindASN:
		return strings.Contains(t, "asn") || strings.Contains(t, "isp")
	case KindDBIPASN:
		return strings.Contains(t, "asn") && strings.Contains(t, "dbip")
	}
	return strings.Contains(t, "city") || strings.Contains(t, "country")
}

// unpackMMDB writes the database found in src to dst. src may be the .mmdb
// itself, a gzip of it, or a .tar.gz holding it (what MaxMind serves).
// limit is the most bytes of database it writes; a larger one is an error,
// not a truncated file.
func unpackMMDB(src io.Reader, dst io.Writer, limit int64) error {
	br := bufio.NewReader(src)
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		defer gz.Close()
		inner := bufio.NewReaderSize(gz, 1024)
		if head, _ := inner.Peek(512); len(head) == 512 && string(head[257:262]) == "ustar" {
			tr := tar.NewReader(inner)
			for {
				h, err := tr.Next()
				if err == io.EOF {
					return errors.New("no .mmdb file inside the archive")
				}
				if err != nil {
					return err
				}
				if h.Typeflag == tar.TypeReg && strings.HasSuffix(h.Name, ".mmdb") {
					return copyLimited(dst, tr, limit)
				}
			}
		}
		return copyLimited(dst, inner, limit)
	}
	return copyLimited(dst, br, limit)
}

// copyLimited copies src to dst and fails when src holds more than limit bytes.
func copyLimited(dst io.Writer, src io.Reader, limit int64) error {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("the database is larger than the allowed %d MiB", limit>>20)
	}
	return nil
}
