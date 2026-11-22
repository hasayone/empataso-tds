package geo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// DB — a resolver backed by MaxMind-format databases: one City (or Country)
// file for location, and optionally two ASN files for the network: MaxMind
// GeoLite2-ASN and DB-IP ASN Lite. Any of them may be absent; what is missing
// simply stays empty in the answer.
//
// Files are read into memory and swapped atomically when they change on disk
// (Watch), so a fresh database dropped in by the updater goes live without a
// restart. Reading into memory rather than mapping the file is deliberate: a
// reader that is being replaced may still be in use by a request, and a byte
// slice the garbage collector owns cannot be unmapped from under it.
type DB struct {
	city source
	asn  source
	dbip source
	log  *slog.Logger
}

// Paths names the database files of a resolver. An empty path means "no
// such database".
type Paths struct {
	City    string // GeoLite2/GeoIP2 City or Country
	ASN     string // MaxMind GeoLite2-ASN
	DBIPASN string // DB-IP ASN Lite (same schema as GeoLite2-ASN)
}

// Kinds of a database slot, the same names the cron service uses for its geo sources.
const (
	KindCity    = "city"
	KindASN     = "asn"
	KindDBIPASN = "dbip_asn"
)

type source struct {
	kind string
	path string
	r    atomic.Pointer[maxminddb.Reader]
	st   atomic.Pointer[fileStamp]
}

type fileStamp struct {
	mod  time.Time
	size int64
}

// OpenWith builds a resolver from the given files. An empty path means "no
// such database". A configured file that cannot be read is an error only if
// no database loaded at all (the first failure is returned) — otherwise the
// resolver starts with what it has and Watch picks the rest up when it
// appears (the updater may not have downloaded it yet). The returned DB is
// usable even alongside an error, so the caller may keep it for Watch.
func OpenWith(p Paths, log *slog.Logger) (*DB, error) {
	if log == nil {
		log = slog.Default()
	}
	d := &DB{log: log}
	d.city.path, d.asn.path, d.dbip.path = p.City, p.ASN, p.DBIPASN
	d.city.kind, d.asn.kind, d.dbip.kind = KindCity, KindASN, KindDBIPASN
	d.ensureDirs()
	var first error
	loaded := false
	for _, s := range d.sources() {
		if err := s.load(); err != nil && first == nil {
			first = err
		}
		loaded = loaded || s.r.Load() != nil
	}
	if !loaded {
		return d, first
	}
	return d, nil
}

// sources lists the databases in a fixed order: City, MaxMind ASN, DB-IP ASN.
func (d *DB) sources() []*source { return []*source{&d.city, &d.asn, &d.dbip} }

// ensureDirs creates the parent directory of each configured database at 0o755,
// the mode the process already uses when it reads these paths.
// It does not create the database file: an empty or broken mmdb is worse than
// a missing one, and Watch keeps waiting for a real file. A path with no
// directory (a bare file name) is skipped. A mkdir failure is logged; OpenWith
// still returns the database that could not be read.
func (d *DB) ensureDirs() {
	seen := map[string]bool{}
	for _, s := range d.sources() {
		if s.path == "" {
			continue
		}
		dir := filepath.Dir(s.path)
		if dir == "" || dir == "." || seen[dir] {
			continue
		}
		seen[dir] = true
		if err := os.MkdirAll(dir, 0o755); err != nil {
			d.log.Warn("geo directory", "path", dir, "err", err)
		}
	}
}

// load reads the file if it is configured and has changed since the last
// successful load. The previous reader stays in place on any failure: a
// half-written or corrupt download must not blank the geo data.
func (s *source) load() error {
	if s.path == "" {
		return nil
	}
	fi, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("geo: %w", err)
	}
	st := &fileStamp{mod: fi.ModTime(), size: fi.Size()}
	if cur := s.st.Load(); cur != nil && cur.mod.Equal(st.mod) && cur.size == st.size {
		return nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("geo: %w", err)
	}
	r, err := maxminddb.FromBytes(b)
	if err != nil {
		return fmt.Errorf("geo: open %s: %w", s.path, err)
	}
	s.r.Store(r)
	s.st.Store(st)
	return nil
}

// Reload re-reads the files that changed on disk and reports which did.
// A nil DB has nothing to reload and returns nil.
func (d *DB) Reload() (changed []string) {
	if d == nil {
		return nil
	}
	for _, s := range d.sources() {
		before := s.st.Load()
		if err := s.load(); err != nil {
			// A missing file is the normal state before the first download;
			// anything else is worth a line.
			if !errors.Is(err, os.ErrNotExist) {
				d.log.Warn("geo db not reloaded", "path", s.path, "err", err)
			}
			continue
		}
		if s.st.Load() != before {
			changed = append(changed, s.path)
		}
	}
	return changed
}

// Watch reloads changed databases every interval until ctx is done.
func (d *DB) Watch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, p := range d.Reload() {
				d.log.Info("geo db reloaded", "path", p)
			}
		}
	}
}

// Resolve determines geo by IP. On an invalid IP or a miss, returns empty values.
//
// The network comes from DB-IP ASN Lite first and from MaxMind GeoLite2-ASN
// as a fallback (see mergeASN): filters migrated from Keitaro are written
// with DB-IP organization names ("Google LLC"), MaxMind says "GOOGLE", and
// Geo.ASNAlt and Geo.OrgAlt keep the MaxMind number and spelling so older
// filters still match.
func (d *DB) Resolve(ip netip.Addr) Geo {
	g := None()
	if d == nil || !ip.IsValid() {
		return g
	}
	nip := net.IP(ip.AsSlice())
	if r := d.city.r.Load(); r != nil {
		var rec record
		if err := r.Lookup(nip, &rec); err == nil {
			g = fromRecord(rec)
		}
	}
	rd, rm := d.dbip.r.Load(), d.asn.r.Load()
	if rd == nil && rm == nil {
		return g
	}
	g.ASN, g.ASNAlt, g.Org, g.OrgAlt = mergeASN(lookupASN(rd, nip), lookupASN(rm, nip))
	return g
}

// lookupASN reads the ASN record of ip from r. A nil reader, a lookup error
// and a miss all return the zero record (Number == 0).
func lookupASN(r *maxminddb.Reader, ip net.IP) asnRecord {
	if r == nil {
		return asnRecord{}
	}
	var rec asnRecord
	if err := r.Lookup(ip, &rec); err != nil {
		return asnRecord{}
	}
	return rec
}

// Info describes a loaded database, for the admin panel.
type Info struct {
	Kind   string    `json:"kind"` // KindCity, KindASN or KindDBIPASN
	Path   string    `json:"path"`
	Type   string    `json:"type"`   // "GeoLite2-City", "GeoLite2-ASN", …
	Built  time.Time `json:"built"`  // build time from the database metadata
	Loaded bool      `json:"loaded"` // false: configured but not readable yet
}

// Databases lists the configured databases and what is loaded from them.
func (d *DB) Databases() []Info {
	if d == nil {
		return nil
	}
	var out []Info
	for _, s := range d.sources() {
		if s.path == "" {
			continue
		}
		in := Info{Kind: s.kind, Path: s.path}
		if r := s.r.Load(); r != nil {
			in.Loaded = true
			in.Type = r.Metadata.DatabaseType
			in.Built = time.Unix(int64(r.Metadata.BuildEpoch), 0).UTC()
		}
		out = append(out, in)
	}
	return out
}

// Close releases the database. The readers hold plain memory owned by the
// garbage collector, so there is nothing to free and it always returns nil;
// it exists so DB satisfies io.Closer for callers that close what they open.
func (d *DB) Close() error { return nil }
