package security

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

func TestUserCloneCopiesBoardIDs(t *testing.T) {
	u := User{BoardIDs: []string{"sales", "main"}}
	c := u.clone()
	c.BoardIDs[0] = "changed"
	if u.BoardIDs[0] != "sales" {
		t.Fatalf("clone shares board_ids: %+v", u.BoardIDs)
	}
}

func TestVisibleBoards(t *testing.T) {
	env := User{Source: SourceEnv}
	if ids, all := env.VisibleBoards(false); !all || ids != nil {
		t.Fatalf("env source: ids=%v all=%v", ids, all)
	}
	blank := User{}
	if ids, all := blank.VisibleBoards(true); !all || ids != nil {
		t.Fatalf("env admin flag: ids=%v all=%v", ids, all)
	}
	admin := User{ID: "a1", Type: UserAdmin, BoardIDs: nil}
	if ids, all := admin.VisibleBoards(false); !all || ids != nil {
		t.Fatalf("admin empty boards: ids=%v all=%v", ids, all)
	}
	adminList := User{ID: "a2", Type: UserAdmin, BoardIDs: []string{"sales"}}
	if ids, all := adminList.VisibleBoards(false); !all || ids != nil {
		t.Fatalf("admin with membership list must see every section: ids=%v all=%v", ids, all)
	}
	user := User{ID: "u1", Type: UserRegular}
	if ids, all := user.VisibleBoards(false); all || len(ids) != 1 || ids[0] != config.MainBoardID {
		t.Fatalf("user default: ids=%v all=%v", ids, all)
	}
	userList := User{ID: "u3", Type: UserRegular, BoardIDs: []string{"sales", "", "sales"}}
	if ids, all := userList.VisibleBoards(false); all || !slices.Equal(ids, []string{config.MainBoardID, "sales"}) {
		t.Fatalf("user list: ids=%v all=%v", ids, all)
	}
	allBoards := User{ID: "u2", Type: UserRegular, AllBoards: true, BoardIDs: []string{"sales"}, NoMainBoard: true}
	if ids, all := allBoards.VisibleBoards(false); !all || ids != nil {
		t.Fatalf("all_boards: ids=%v all=%v", ids, all)
	}
	adminNoMain := User{ID: "a3", Type: UserAdmin, NoMainBoard: true}
	if ids, all := adminNoMain.VisibleBoards(false); !all || ids != nil {
		t.Fatalf("admin with no_main_board must see every section: ids=%v all=%v", ids, all)
	}
}

func TestVisibleBoardsMainFlag(t *testing.T) {
	cases := []struct {
		name string
		u    User
		want []string
	}{
		{"member of x keeps main", User{ID: "u", Type: UserRegular, BoardIDs: []string{"x"}}, []string{config.MainBoardID, "x"}},
		{"removed from x keeps main", User{ID: "u", Type: UserRegular}, []string{config.MainBoardID}},
		{"main unticked", User{ID: "u", Type: UserRegular, NoMainBoard: true, BoardIDs: []string{"x"}}, []string{"x"}},
		{"main in board_ids is ignored", User{ID: "u", Type: UserRegular, NoMainBoard: true, BoardIDs: []string{"main", "x"}}, []string{"x"}},
		{"no section at all", User{ID: "u", Type: UserRegular, NoMainBoard: true}, []string{}},
	}
	for _, c := range cases {
		ids, all := c.u.VisibleBoards(false)
		if all || ids == nil || !slices.Equal(ids, c.want) {
			t.Errorf("%s: ids=%v all=%v, want %v", c.name, ids, all, c.want)
		}
	}
}

func TestLoadUserWithoutMainFlagKeepsMain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	raw := `[{"id":"old","login":"old","type":"USER","enabled":true,"prefs":{},"access":{"sections":[],"grants":{}},"board_ids":["seo"],"created_at":"2026-01-01T00:00:00Z"}]`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := LoadUserSet(path)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := set.Get("old")
	if !ok || u.NoMainBoard {
		t.Fatalf("old record: ok=%v no_main_board=%v", ok, u.NoMainBoard)
	}
	if ids, all := u.VisibleBoards(false); all || !slices.Equal(ids, []string{config.MainBoardID, "seo"}) {
		t.Fatalf("old record sees %v all=%v, want main and seo", ids, all)
	}
	if err := set.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "no_main_board") {
		t.Fatalf("a record with main writes no_main_board: %s", data)
	}
}
