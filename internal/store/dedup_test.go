package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/egerkuzma/emtds/internal/logbuf"
)

// batchConn is a driver.Conn whose PrepareBatch records the context it gets
// and fails, so InsertEvents stops right after it has built its settings.
type batchConn struct {
	driver.Conn
	ctxs []context.Context
}

func (f *batchConn) PrepareBatch(ctx context.Context, _ string, _ ...driver.PrepareBatchOption) (driver.Batch, error) {
	f.ctxs = append(f.ctxs, ctx)
	return nil, errFakeConn
}

// insertedSettings runs InsertEvents with ctx on a recording connection and
// returns the settings the insert was prepared with.
func insertedSettings(t *testing.T, ctx context.Context) map[string]string {
	t.Helper()
	f := &batchConn{}
	c := &CH{conn: f, limits: testLimits()}
	c.originEvents.Store(true)
	c.originConv.Store(true)
	c.rootEvents.Store(true)
	c.rootConv.Store(true)
	if err := c.InsertEvents(ctx, []logbuf.Event{{Ts: time.Now(), CID: "x"}}); err == nil {
		t.Fatal("the fake connection must fail the insert")
	}
	if len(f.ctxs) != 1 {
		t.Fatalf("PrepareBatch called %d times", len(f.ctxs))
	}
	return driverSettings(t, f.ctxs[0])
}

// driverSettings returns the settings the clickhouse driver would send with an
// operation on ctx: the QueryOptions that clickhouse.Context stored in the
// nearest layer of the context. The driver keeps them unexported, so they are
// read by reflection; a layer that is not a plain value context is stepped
// through by its embedded Context. Values are returned as the strings fmt.Sprint
// gives, so 1 and "1" compare equal.
func driverSettings(t *testing.T, ctx context.Context) map[string]string {
	t.Helper()
	for ctx != nil {
		rv := reflect.ValueOf(ctx)
		if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
			break
		}
		layer := rv.Elem()
		if val := layer.FieldByName("val"); val.IsValid() && val.Kind() == reflect.Interface && !val.IsNil() &&
			val.Elem().Type().String() == "clickhouse.QueryOptions" {
			out := map[string]string{}
			settings := val.Elem().FieldByName("settings")
			for it := settings.MapRange(); it.Next(); {
				v := it.Value()
				if v.Kind() == reflect.Interface {
					v = v.Elem()
				}
				switch v.Kind() {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					out[it.Key().String()] = strconv.FormatInt(v.Int(), 10)
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					out[it.Key().String()] = strconv.FormatUint(v.Uint(), 10)
				case reflect.String:
					out[it.Key().String()] = v.String()
				case reflect.Bool:
					out[it.Key().String()] = strconv.FormatBool(v.Bool())
				default:
					t.Fatalf("setting %s has the unexpected kind %s", it.Key().String(), v.Kind())
				}
			}
			return out
		}
		parent := layer.FieldByName("Context")
		if !parent.IsValid() || parent.IsNil() {
			break
		}
		ctx, _ = parent.Interface().(context.Context)
	}
	t.Fatal("the context carries no clickhouse.QueryOptions")
	return nil
}

func TestInsertEventsWithTokenDeduplicates(t *testing.T) {
	s := insertedSettings(t, logbuf.WithDedupToken(context.Background(), "tok-1"))
	if s["insert_deduplicate"] != "1" || s["insert_deduplication_token"] != "tok-1" || s["priority"] != strconv.Itoa(insertPriority) {
		t.Fatalf("settings the driver sends with a token = %v", s)
	}
}

func TestInsertEventsWithoutTokenDoesNot(t *testing.T) {
	s := insertedSettings(t, context.Background())
	if s["insert_deduplicate"] != "0" || s["priority"] != strconv.Itoa(insertPriority) {
		t.Fatalf("settings the driver sends without a token = %v, want insert_deduplicate=0 and the priority", s)
	}
	if _, ok := s["insert_deduplication_token"]; ok {
		t.Fatalf("a token appeared from nowhere: %v", s)
	}
}

// The settings of an events insert stay on its own context: an insert that
// does not go through InsertEvents keeps only the priority.
func TestInsertSettingsDoNotLeak(t *testing.T) {
	parent := context.Background()
	derived := withInsertSettings(parent, eventInsertSettings("tok"))
	merged := insertSettings(derived)
	if merged["priority"] != insertPriority || merged["insert_deduplication_token"] != "tok" {
		t.Fatalf("events insert settings = %v", merged)
	}
	plain := insertSettings(parent)
	if len(plain) != 1 || plain["priority"] != insertPriority {
		t.Fatalf("a plain insert got %v, want only the priority", plain)
	}
}

func TestEventInsertSettingsOverridePriority(t *testing.T) {
	ctx := withInsertSettings(context.Background(), clickhouse.Settings{"priority": 99})
	if got := insertSettings(ctx)["priority"]; got != 99 {
		t.Fatalf("priority = %v, the extra settings must win", got)
	}
}

func TestDedupWindowOf(t *testing.T) {
	cases := []struct {
		ddl  string
		want int
	}{
		{"CREATE TABLE kuztds.events (...) ENGINE = MergeTree ORDER BY ts SETTINGS index_granularity = 8192", 0},
		{"CREATE TABLE kuztds.events (...) ENGINE = MergeTree ORDER BY ts SETTINGS non_replicated_deduplication_window = 10000, index_granularity = 8192", 10000},
		{"... SETTINGS index_granularity = 8192, non_replicated_deduplication_window=500", 500},
		{"", 0},
	}
	for _, tc := range cases {
		if got := dedupWindowOf(tc.ddl); got != tc.want {
			t.Errorf("dedupWindowOf(%q) = %d, want %d", tc.ddl, got, tc.want)
		}
	}
}

func TestDedupReadyNilClient(t *testing.T) {
	var c *CH
	if c.DedupReady() {
		t.Fatal("a nil client cannot deduplicate")
	}
	if (&CH{}).DedupReady() {
		t.Fatal("dedupReady starts false")
	}
}

// The migration file, the statement of ensureSchema and the window the probe
// demands must name the same number.
func TestDedupMigrationMatchesConstants(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationDirForTest(), "014_events_dedup.sql"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`non_replicated_deduplication_window\s*=\s*(\d+)`)
	fromFile := re.FindStringSubmatch(string(raw))
	fromSQL := re.FindStringSubmatch(eventsDedupSQL)
	if fromFile == nil || fromSQL == nil {
		t.Fatalf("setting not found: file=%v sql=%v", fromFile, fromSQL)
	}
	for _, got := range []string{fromFile[1], fromSQL[1]} {
		if n, _ := strconv.Atoi(got); n != eventsDedupWindow {
			t.Fatalf("window %s differs from eventsDedupWindow %d", got, eventsDedupWindow)
		}
	}
}

// ddlConn is a driver.Conn that serves the CREATE statement of events from
// system.tables and counts the ALTERs it is given; an ALTER switches the window
// on, like the real table.
type ddlConn struct {
	driver.Conn
	window   int
	alters   atomic.Int32
	readFail atomic.Bool
}

func (f *ddlConn) QueryRow(context.Context, string, ...any) driver.Row {
	if f.readFail.Load() {
		return ddlRow{err: errors.New("connection reset")}
	}
	ddl := "CREATE TABLE kuztds.events (ts DateTime) ENGINE = MergeTree ORDER BY ts SETTINGS index_granularity = 8192"
	if f.window > 0 {
		ddl += ", non_replicated_deduplication_window = " + strconv.Itoa(f.window)
	}
	return ddlRow{ddl: ddl}
}

func (f *ddlConn) Exec(_ context.Context, query string, _ ...any) error {
	f.alters.Add(1)
	f.window = eventsDedupWindow
	return nil
}

type ddlRow struct {
	ddl string
	err error
}

func (r ddlRow) Err() error { return r.err }
func (r ddlRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*string)) = r.ddl
	return nil
}
func (r ddlRow) ScanStruct(any) error { return r.err }

// A table that already deduplicates is read and left alone.
func TestEnsureDedupDoesNotAlterAGoodTable(t *testing.T) {
	f := &ddlConn{window: eventsDedupWindow}
	c := &CH{conn: f}
	if !c.EnsureDedup() || !c.DedupReady() {
		t.Fatal("a table with the window in force must be ready")
	}
	if n := f.alters.Load(); n != 0 {
		t.Fatalf("%d ALTERs against a table that was already right", n)
	}
}

// A table without the window gets the ALTER once and is then ready.
func TestEnsureDedupAltersATableWithoutTheWindow(t *testing.T) {
	f := &ddlConn{}
	c := &CH{conn: f}
	if !c.EnsureDedup() {
		t.Fatal("the window must be in force after the ALTER")
	}
	if n := f.alters.Load(); n != 1 {
		t.Fatalf("%d ALTERs, want 1", n)
	}
	c.EnsureDedup()
	if n := f.alters.Load(); n != 1 {
		t.Fatalf("%d ALTERs after the second call, want still 1", n)
	}
}

// A read that fails says nothing about the table: what was known stays.
func TestEnsureDedupKeepsTheKnownValueOnAReadError(t *testing.T) {
	f := &ddlConn{window: eventsDedupWindow}
	c := &CH{conn: f}
	if !c.EnsureDedup() {
		t.Fatal("setup: the table must be ready")
	}
	f.readFail.Store(true)
	if !c.EnsureDedup() || !c.DedupReady() {
		t.Fatal("a failed read must not switch deduplication off")
	}
	if n := f.alters.Load(); n != 0 {
		t.Fatalf("%d ALTERs after a failed read, want none", n)
	}
	fresh := &CH{conn: f}
	if fresh.EnsureDedup() {
		t.Fatal("a client that never read the table cannot claim it deduplicates")
	}
}

func TestBadDataOf(t *testing.T) {
	for code, want := range map[int32]bool{6: true, 27: true, 53: true, 117: true, 159: false, 202: false, 241: false, 252: false} {
		err := badDataOf(fmt.Errorf("send: %w", &clickhouse.Exception{Code: code, Message: "m"}))
		if got := logbuf.IsBadData(err); got != want {
			t.Errorf("code %d: IsBadData = %v, want %v", code, got, want)
		}
	}
	for _, err := range []error{nil, errors.New("read tcp: i/o timeout"), context.DeadlineExceeded} {
		if got := badDataOf(err); got != err || logbuf.IsBadData(got) {
			t.Errorf("%v must pass through unchanged", err)
		}
	}
}
