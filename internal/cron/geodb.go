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

	"github.com/oschwald/maxminddb-golang"
)

const (
	// maxGeoBytes caps a MaxMind download and the database unpacked from it.
	maxGeoBytes = 1 << 30
	// dbipMaxBytes caps a DB-IP ASN download and the database unpacked from it.
	dbipMaxBytes = 256 << 20
	// dbipTimeout bounds one DB-IP ASN download together with unpacking it.
	dbipTimeout = 3 * time.Minute
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

// runGeoDB refreshes every source in cfg. ctx cancels the downloads.
// The string is one "kind: result" per source that finished or was skipped,
// joined with "; ". A source that fails is skipped, its error is joined into
// the returned error, and later sources still run. A MaxMind source with no
// account id and no license key is skipped with a message, not an error:
// the default config lists them before anyone has a key.
func (r *Runner) runGeoDB(ctx context.Context, cfg GeoDB) (string, error) {
	if len(cfg.Sources) == 0 {
		return "no sources configured", nil
	}
	var done []string
	var errs []error
	for _, src := range cfg.Sources {
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
// with the previous one. A dbip_asn source is held to the DB-IP host (also for
// redirects), a smaller size cap and a three minute deadline. Any failure
// leaves the file at target as it was.
// ctx cancels the download, src is the source and target the installed file.
// It returns a short result text, or an error that names the host or the
// problem with the file, never a URL with a key.
func (r *Runner) updateGeo(ctx context.Context, src GeoSource, target string) (string, error) {
	limit := int64(maxGeoBytes)
	if src.Kind == KindDBIPASN {
		limit = r.dbipLimit
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, dbipTimeout)
		defer cancel()
	}
	dl, err := r.downloadGeo(ctx, src, target, limit)
	if statusOf(err) == http.StatusNotModified {
		return "not modified since " + dl.seen.LastModified, nil
	}
	if err != nil {
		return "", err
	}
	defer dl.body.Close()
	tmp, typ, built, err := r.stageGeo(dl.body, src.Kind, target, limit)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	return r.installGeo(src.Kind, target, tmp, typ, built, dl)
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
// exists and the last download was from the same URL.
// limit caps the response size. The caller closes dl.body when err is nil.
// A 304 is returned as an error with that status, with dl.seen filled in.
func (r *Runner) downloadGeo(ctx context.Context, src GeoSource, target string, limit int64) (dl geoDownload, err error) {
	r.state(func(st *Status) { dl.seen = st.GeoSeen[src.Kind] })
	_, statErr := os.Stat(target)
	user, pass := src.Credentials()
	req := fetchRequest{User: user, Pass: pass, Limit: limit}
	if src.Kind == KindDBIPASN {
		req.Allow = r.dbipAllowed
	}
	urls := geoURLs(src.URL, r.now())
	for i, u := range urls {
		req.URL, req.Header = u, nil
		if statErr == nil && dl.seen.URL == u && dl.seen.LastModified != "" {
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

// stageGeo unpacks body into a temporary file next to target and checks it
// with checkGeoDB for the given kind. limit caps the unpacked size.
// It returns the temporary file's path, which the caller removes, and the
// database type and build epoch. On any error the temporary file is already gone.
func (r *Runner) stageGeo(body io.Reader, kind, target string, limit int64) (tmp, typ string, built int, err error) {
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".dl*")
	if err != nil {
		return "", "", 0, err
	}
	err = unpackMMDB(body, f, limit)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		typ, built, err = r.checkGeoDB(f.Name(), kind)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", "", 0, err
	}
	return f.Name(), typ, built, nil
}

// installGeo puts the checked database at tmp in place of target. When the
// installed build is the one downloaded, the file is left alone. The download
// is remembered either way, so the next run can ask for changes only.
// It returns the result text, or the error of the permission change or rename.
func (r *Runner) installGeo(kind, target, tmp, typ string, built int, dl geoDownload) (string, error) {
	var have int
	r.state(func(st *Status) { have = st.GeoBuilt[kind] })
	if _, err := os.Stat(target); err == nil && have == built {
		r.rememberGeo(kind, dl)
		return typ + " is up to date", nil
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	r.state(func(st *Status) {
		if st.GeoBuilt == nil {
			st.GeoBuilt = map[string]int{}
		}
		st.GeoBuilt[kind] = built
	})
	r.rememberGeo(kind, dl)
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
