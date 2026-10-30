package config

import "testing"

func TestValidateBoardMembers(t *testing.T) {
	if err := ValidateBoardMembers(nil); err != nil {
		t.Fatal(err)
	}
	ok := []BoardMember{{UserID: "u1", Role: BoardRoleEdit}}
	if err := ValidateBoardMembers(ok); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBoardMembers([]BoardMember{{UserID: "", Role: BoardRoleRead}}); err != ErrInvalidBoardMember {
		t.Fatalf("empty user: %v", err)
	}
	if err := ValidateBoardMembers([]BoardMember{{UserID: "u1", Role: "admin"}}); err != ErrInvalidBoardMember {
		t.Fatalf("bad role: %v", err)
	}
	if err := ValidateBoardMembers([]BoardMember{
		{UserID: "u1", Role: BoardRoleRead},
		{UserID: "u1", Role: BoardRoleEdit},
	}); err != ErrInvalidBoardMember {
		t.Fatalf("dup: %v", err)
	}
}
