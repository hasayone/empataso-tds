package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

func TestListBoardsNoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "boards.json")
	store := NewJSONStore[config.Board](path)
	ctx := context.Background()

	got, err := listBoards(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != config.MainBoardID || got[0].Name != mainBoardName {
		t.Fatalf("list = %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("list must not create boards.json")
	}
}

func TestCreateBoardWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "boards.json")
	store := NewJSONStore[config.Board](path)
	ctx := context.Background()

	created, err := createBoard(ctx, store, "Sales", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "Sales" {
		t.Fatalf("created = %+v", created)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	compact := strings.ReplaceAll(string(b), " ", "")
	if !strings.Contains(compact, `"id":"main"`) || !strings.Contains(compact, `"id":"sales"`) {
		t.Fatalf("file = %s", b)
	}
	list, err := listBoards(ctx, store)
	if err != nil || len(list) != 2 {
		t.Fatalf("after create list = %+v err %v", list, err)
	}
}

func TestReorderBoards(t *testing.T) {
	store := NewJSONStore[config.Board](filepath.Join(t.TempDir(), "boards.json"))
	ctx := context.Background()
	in := []config.Board{
		{ID: "main", Name: mainBoardName},
		{ID: "a", Name: "A"},
		{ID: "b", Name: "B"},
	}
	if err := store.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := reorderBoards(ctx, store, []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "main" || got[1].ID != "b" || got[2].ID != "a" {
		t.Fatalf("order = %+v", got)
	}
	listed, err := listBoards(ctx, store)
	if err != nil || len(listed) != 3 || listed[1].ID != "b" || listed[2].ID != "a" {
		t.Fatalf("saved = %+v err %v", listed, err)
	}
	for _, ids := range [][]string{{"a"}, {"b", "a", "a"}, {"b", "nope"}, {"main", "b", "a"}} {
		if _, err := reorderBoards(ctx, store, ids); err != ErrBoardOrder {
			t.Fatalf("ids %v err = %v", ids, err)
		}
	}
}

func TestEmptyBoardIDIsMain(t *testing.T) {
	if config.NormalizeBoardID("") != config.MainBoardID {
		t.Fatal("empty board_id must be main")
	}
}

func TestFileGroupsKeepsBoardID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	fg := NewFileGroups(path)
	ctx := context.Background()
	in := []config.Group{{
		ID:      "g1",
		Name:    "demo",
		Status:  true,
		BoardID: "sales",
		Streams: []config.Stream{{Name: "a", Status: true}},
	}}
	if err := fg.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := fg.List(ctx)
	if err != nil || len(got) != 1 || got[0].BoardID != "sales" {
		t.Fatalf("round-trip board_id: got %+v err %v", got, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []config.Group
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].BoardID != "sales" {
		t.Fatalf("json board_id lost: %+v", decoded)
	}
	if err := fg.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	raw2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw2), `"board_id":"sales"`) {
		t.Fatalf("resave stripped board_id: %s", raw2)
	}
}

func TestCreateBoardCyrillicName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "boards.json")
	store := NewJSONStore[config.Board](path)
	ctx := context.Background()

	created, err := createBoard(ctx, store, "СЕО", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "СЕО" || config.SameBoard(created.ID, config.MainBoardID) {
		t.Fatalf("created = %+v", created)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	compact := strings.ReplaceAll(string(b), " ", "")
	if !strings.Contains(compact, `"id":"main"`) || !strings.Contains(compact, `"name":"СЕО"`) {
		t.Fatalf("file = %s", b)
	}
}

func TestCreateBoardRejectsMainName(t *testing.T) {
	ctx := context.Background()
	store := NewJSONStore[config.Board](filepath.Join(t.TempDir(), "boards.json"))
	for _, name := range []string{"main", "Основной", "По умолчанию", "MAIN"} {
		if _, err := createBoard(ctx, store, name, "", ""); err != ErrBoardMainCreate {
			t.Fatalf("createBoard(%q) err = %v; want ErrBoardMainCreate", name, err)
		}
	}
}

func TestCreateBoardWithIcon(t *testing.T) {
	ctx := context.Background()
	store := NewJSONStore[config.Board](filepath.Join(t.TempDir(), "boards.json"))
	created, err := createBoard(ctx, store, "Demo", "flows", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Icon != "flows" {
		t.Fatalf("icon = %q", created.Icon)
	}
	if _, err := createBoard(ctx, store, "Bad", "nope", ""); err != config.ErrInvalidBoardIcon {
		t.Fatalf("invalid icon err = %v", err)
	}
}

func TestCreateBoardLatinSlug(t *testing.T) {
	ctx := context.Background()
	store := NewJSONStore[config.Board](filepath.Join(t.TempDir(), "boards.json"))
	created, err := createBoard(ctx, store, "SEO", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "seo" && !strings.HasPrefix(created.ID, "seo-") {
		t.Fatalf("id = %q; want seo or seo-*", created.ID)
	}
}

func TestListBoardsEnsuresMainInFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "boards.json")
	store := NewJSONStore[config.Board](path)
	ctx := context.Background()
	if err := store.Save(ctx, []config.Board{{ID: "sales", Name: "Sales"}}); err != nil {
		t.Fatal(err)
	}
	got, err := listBoards(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != config.MainBoardID {
		t.Fatalf("list = %+v", got)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `"id":"main"`) {
		t.Fatal("list must not write main into file")
	}
}
