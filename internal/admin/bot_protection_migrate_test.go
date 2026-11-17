package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

type countingGroups struct {
	memGroups
	saves atomic.Int32
}

func (c *countingGroups) Save(ctx context.Context, groups []config.Group) error {
	c.saves.Add(1)
	return c.memGroups.Save(ctx, groups)
}

func TestMigrateBotProtection404(t *testing.T) {
	const pageName = "page-0306632417d4.html"
	pageOut := "@page:" + pageName
	dir := t.TempDir()
	pagesDir := filepath.Join(dir, "pages")
	pages := NewFilePages(pagesDir)
	if err := pages.Write(pageName, "<html>bot wall</html>"); err != nil {
		t.Fatal(err)
	}
	if err := pages.Write("keep.html", "keep"); err != nil {
		t.Fatal(err)
	}

	groups := &countingGroups{memGroups: memGroups{list: []config.Group{{
		ID: "c1", Name: "Camp", Status: true,
		Streams: []config.Stream{
			{Name: "Bot Protection -❄️⚡️✅", Status: true, Out: config.Output{Redirect: "show_html_404", Out: pageOut, Chance: 100}},
			{Name: "white page", Status: true, Out: config.Output{Redirect: "show_html_404", Out: pageOut, Chance: 100}},
		},
	}}}}
	s := &Server{cfg: Config{Groups: groups, Pages: pages}}

	if err := s.migrateBotProtection404(context.Background()); err != nil {
		t.Fatal(err)
	}
	if groups.saves.Load() != 1 {
		t.Fatalf("saves %d", groups.saves.Load())
	}
	bot := groups.list[0].Streams[0]
	white := groups.list[0].Streams[1]
	if bot.Out.Redirect != "404_not_found" || bot.Out.Out != "" {
		t.Fatalf("bot stream %+v", bot.Out)
	}
	if white.Out.Redirect != "show_html_404" || white.Out.Out != pageOut {
		t.Fatalf("white stream %+v", white.Out)
	}
	if _, err := pages.Read(pageName); err != nil {
		t.Fatalf("shared page removed: %v", err)
	}

	if err := s.migrateBotProtection404(context.Background()); err != nil {
		t.Fatal(err)
	}
	if groups.saves.Load() != 1 {
		t.Fatalf("second pass saves %d", groups.saves.Load())
	}
	if body, err := pages.Read("keep.html"); err != nil || body != "keep" {
		t.Fatalf("keep %q err %v", body, err)
	}
}

func TestMigrateBotProtection404ClosingStream(t *testing.T) {
	const pageName = "page-a08857113c85.html"
	pageOut := "@page:" + pageName
	groups := &countingGroups{memGroups: memGroups{list: []config.Group{{
		ID: "c1", Status: true,
		Streams: []config.Stream{
			{Name: "Замыкающий", Status: true, Out: config.Output{Redirect: "show_html_404", Out: pageOut, Chance: 100}},
			{Name: "Замыкающий", Status: true, Out: config.Output{Redirect: "show_page_html", Out: pageOut, Chance: 100}},
		},
	}}}}
	s := &Server{cfg: Config{Groups: groups, Pages: NewFilePages(filepath.Join(t.TempDir(), "pages"))}}
	if err := s.migrateBotProtection404(context.Background()); err != nil {
		t.Fatal(err)
	}
	if groups.list[0].Streams[0].Out.Redirect != "404_not_found" || groups.list[0].Streams[0].Out.Out != "" {
		t.Fatalf("404 stream %+v", groups.list[0].Streams[0].Out)
	}
	if groups.list[0].Streams[1].Out.Redirect != "show_page_html" || groups.list[0].Streams[1].Out.Out != pageOut {
		t.Fatalf("page html stream %+v", groups.list[0].Streams[1].Out)
	}
}

func TestMigrateBotProtection404DeletesOrphanPage(t *testing.T) {
	const pageName = "page-0306632417d4.html"
	pages := NewFilePages(filepath.Join(t.TempDir(), "pages"))
	if err := pages.Write(pageName, "<html>only bot</html>"); err != nil {
		t.Fatal(err)
	}
	groups := &countingGroups{memGroups: memGroups{list: []config.Group{{
		ID: "c1", Status: true,
		Streams: []config.Stream{{
			Name: "Bot Protection", Status: true,
			Out: config.Output{Redirect: "show_html_404", Out: "@page:" + pageName, Chance: 100},
		}},
	}}}}
	s := &Server{cfg: Config{Groups: groups, Pages: pages}}
	if err := s.migrateBotProtection404(context.Background()); err != nil {
		t.Fatal(err)
	}
	if groups.list[0].Streams[0].Out.Redirect != "404_not_found" || groups.list[0].Streams[0].Out.Out != "" {
		t.Fatalf("stream %+v", groups.list[0].Streams[0].Out)
	}
	if _, err := pages.Read(pageName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan page remains: %v", err)
	}
}

func TestMigrateBotProtection404OnNew(t *testing.T) {
	const pageName = "page-0306632417d4.html"
	dir := t.TempDir()
	gpath := filepath.Join(dir, "groups.json")
	seed := []config.Group{{
		ID: "c1", Name: "Camp", Status: true,
		Streams: []config.Stream{{
			Name: "bot protection", Status: true,
			Out: config.Output{Redirect: "show_html_404", Out: "@page:" + pageName, Chance: 100},
		}},
	}}
	b, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gpath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	pages := NewFilePages(filepath.Join(dir, "pages"))
	if err := pages.Write(pageName, "<p>x</p>"); err != nil {
		t.Fatal(err)
	}
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Groups: NewFileGroups(gpath), Pages: pages,
		Limiter: allowAll{}, SessionTTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := NewFileGroups(gpath).List(context.Background())
	if err != nil || len(got) != 1 || len(got[0].Streams) != 1 {
		t.Fatalf("groups %+v err %v", got, err)
	}
	if got[0].Streams[0].Out.Redirect != "404_not_found" || got[0].Streams[0].Out.Out != "" {
		t.Fatalf("stream %+v", got[0].Streams[0].Out)
	}
	if _, err := pages.Read(pageName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("page file remains: %v", err)
	}
	info, err := os.Stat(gpath)
	if err != nil {
		t.Fatal(err)
	}
	mod := info.ModTime()
	if _, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Groups: NewFileGroups(gpath), Pages: pages,
		Limiter: allowAll{}, SessionTTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	info2, err := os.Stat(gpath)
	if err != nil {
		t.Fatal(err)
	}
	if !info2.ModTime().Equal(mod) {
		t.Fatal("groups.json rewritten on second start")
	}
}
