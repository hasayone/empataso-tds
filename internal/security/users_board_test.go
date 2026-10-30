package security

import (
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
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
	adminList := User{ID: "a2", Type: UserAdmin, BoardIDs: []string{"sales", ""}}
	if ids, all := adminList.VisibleBoards(false); all || len(ids) != 2 || ids[1] != config.MainBoardID {
		t.Fatalf("admin list: ids=%v all=%v", ids, all)
	}
	user := User{ID: "u1", Type: UserRegular}
	if ids, all := user.VisibleBoards(false); all || len(ids) != 1 || ids[0] != config.MainBoardID {
		t.Fatalf("user default: ids=%v all=%v", ids, all)
	}
	allBoards := User{ID: "u2", Type: UserRegular, AllBoards: true}
	if ids, all := allBoards.VisibleBoards(false); !all || ids != nil {
		t.Fatalf("all_boards: ids=%v all=%v", ids, all)
	}
}
