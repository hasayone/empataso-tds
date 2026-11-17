package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

const mainBoardName = "По умолчанию"

var (
	// ErrBoardName is returned when a board name is empty.
	ErrBoardName = errors.New("admin: board name required")
	// ErrBoardMainCreate is returned when create would add a second main section.
	ErrBoardMainCreate = errors.New("admin: cannot create main board")
	// ErrBoardNotFound is returned when a board id is missing from boards.json.
	ErrBoardNotFound = errors.New("admin: board not found")
	// ErrBoardMainRename is returned when PATCH tries to change the main section name.
	ErrBoardMainRename = errors.New("admin: cannot rename main board")
	// ErrBoardMainDelete is returned when DELETE targets the main section.
	ErrBoardMainDelete = errors.New("admin: cannot delete main board")
	// ErrBoardInUse is returned when DELETE finds catalog rows on the section.
	ErrBoardInUse = errors.New("admin: board in use")
	// ErrBoardOrder is returned when a reorder list is not the current set of sections.
	ErrBoardOrder = errors.New("admin: board order")
	// ErrBoardIncludeAll is returned when include_all is sent for a section other than main.
	ErrBoardIncludeAll = errors.New("admin: include_all only on main")
)

// boardPatchFields holds optional name, icon, icon color and include_all updates decoded from JSON.
type boardPatchFields struct {
	NameSet       bool
	Name          string
	IconSet       bool
	Icon          string
	IconColorSet  bool
	IconColor     string
	IncludeAllSet bool
	IncludeAll    bool
}

// listBoards reads boards.json. A missing or empty store returns only the main section
// without writing a file. The main section is always present in the result.
func listBoards(ctx context.Context, store *JSONStore[config.Board]) ([]config.Board, error) {
	if store == nil {
		return []config.Board{mainBoardRecord()}, nil
	}
	raw, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	return boardsWithMain(raw), nil
}

// createBoard adds a section with name, optional icon and optional tint and saves boards.json.
// The first save also writes the main section. Main cannot be created twice.
// An unknown icon or tint returns config.ErrInvalidBoardIcon.
func createBoard(ctx context.Context, store *JSONStore[config.Board], name, icon, color string) (config.Board, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return config.Board{}, ErrBoardName
	}
	if reservedMainBoardName(name) {
		return config.Board{}, ErrBoardMainCreate
	}
	if err := config.ValidateBoardIcon(icon); err != nil {
		return config.Board{}, err
	}
	if err := config.ValidateBoardIconColor(color); err != nil {
		return config.Board{}, err
	}
	raw, err := store.List(ctx)
	if err != nil {
		return config.Board{}, err
	}
	id, err := uniqueBoardID(raw, name)
	if err != nil {
		return config.Board{}, err
	}
	item := config.Board{ID: id, Name: name, Icon: icon, IconColor: color}
	var toSave []config.Board
	if len(raw) == 0 {
		toSave = []config.Board{mainBoardRecord(), item}
	} else {
		toSave = append(append([]config.Board(nil), raw...), item)
	}
	if err := store.Save(ctx, toSave); err != nil {
		return config.Board{}, err
	}
	return item, nil
}

// decodeBoardPatch reads optional name, icon, icon_color and include_all from a JSON object body.
// A missing key leaves the field unchanged; an empty string clears icon or icon_color.
// include_all is a bool. A bad value returns the decode error and an empty patch.
func decodeBoardPatch(raw map[string]json.RawMessage) (boardPatchFields, error) {
	var patch boardPatchFields
	if v, ok := raw["name"]; ok {
		patch.NameSet = true
		if err := json.Unmarshal(v, &patch.Name); err != nil {
			return boardPatchFields{}, err
		}
		patch.Name = strings.TrimSpace(patch.Name)
	}
	if v, ok := raw["icon"]; ok {
		patch.IconSet = true
		if err := json.Unmarshal(v, &patch.Icon); err != nil {
			return boardPatchFields{}, err
		}
	}
	if v, ok := raw["icon_color"]; ok {
		patch.IconColorSet = true
		if err := json.Unmarshal(v, &patch.IconColor); err != nil {
			return boardPatchFields{}, err
		}
	}
	if v, ok := raw["include_all"]; ok {
		patch.IncludeAllSet = true
		if err := json.Unmarshal(v, &patch.IncludeAll); err != nil {
			return boardPatchFields{}, err
		}
	}
	return patch, nil
}

// updateBoard applies patch to the section id and saves boards.json.
// Main may be stored only virtually until the first patch; icon updates persist main in the file.
// include_all is accepted only for main. True is stored as nil. A false pointer disables the wide view.
// An empty patch returns an error and does not write. include_all on another section returns ErrBoardIncludeAll
// and does not write.
func updateBoard(ctx context.Context, store *JSONStore[config.Board], id string, patch boardPatchFields) (config.Board, error) {
	id = config.NormalizeBoardID(id)
	if !patch.NameSet && !patch.IconSet && !patch.IconColorSet && !patch.IncludeAllSet {
		return config.Board{}, errors.New("admin: board patch empty")
	}
	if patch.IncludeAllSet && !config.SameBoard(id, config.MainBoardID) {
		return config.Board{}, ErrBoardIncludeAll
	}
	raw, err := store.List(ctx)
	if err != nil {
		return config.Board{}, err
	}
	idx := boardIndex(raw, id)
	if idx < 0 {
		if !config.SameBoard(id, config.MainBoardID) {
			return config.Board{}, ErrBoardNotFound
		}
		raw = append([]config.Board{mainBoardRecord()}, raw...)
		idx = 0
	}
	cur := raw[idx]
	if config.SameBoard(cur.ID, config.MainBoardID) {
		if patch.NameSet && patch.Name != cur.Name {
			return config.Board{}, ErrBoardMainRename
		}
	} else if patch.NameSet {
		if patch.Name == "" {
			return config.Board{}, ErrBoardName
		}
		if reservedMainBoardName(patch.Name) {
			return config.Board{}, ErrBoardMainCreate
		}
		cur.Name = patch.Name
	}
	if patch.IconSet {
		if err := config.ValidateBoardIcon(patch.Icon); err != nil {
			return config.Board{}, err
		}
		cur.Icon = patch.Icon
	}
	if patch.IconColorSet {
		if err := config.ValidateBoardIconColor(patch.IconColor); err != nil {
			return config.Board{}, err
		}
		cur.IconColor = patch.IconColor
	}
	if patch.IncludeAllSet {
		if patch.IncludeAll {
			cur.IncludeAll = nil
		} else {
			off := false
			cur.IncludeAll = &off
		}
	}
	raw[idx] = cur
	if err := store.Save(ctx, raw); err != nil {
		return config.Board{}, err
	}
	return cur, nil
}

// deleteBoard removes a non-main section from boards.json.
func deleteBoard(ctx context.Context, store *JSONStore[config.Board], id string) error {
	id = config.NormalizeBoardID(id)
	if config.SameBoard(id, config.MainBoardID) {
		return ErrBoardMainDelete
	}
	raw, err := store.List(ctx)
	if err != nil {
		return err
	}
	idx := boardIndex(raw, id)
	if idx < 0 {
		return ErrBoardNotFound
	}
	raw = append(append([]config.Board(nil), raw[:idx]...), raw[idx+1:]...)
	return store.Save(ctx, raw)
}

// reorderBoards saves boards.json with main first and the other sections in ids order.
// ids must list every non-main section once. Main in the list, a duplicate, an unknown id,
// or a missing section returns ErrBoardOrder. The returned slice is the saved order.
func reorderBoards(ctx context.Context, store *JSONStore[config.Board], ids []string) ([]config.Board, error) {
	raw, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	list := boardsWithMain(raw)
	byID := make(map[string]config.Board, len(list))
	var main config.Board
	rest := 0
	for _, b := range list {
		if config.SameBoard(b.ID, config.MainBoardID) {
			main = b
			continue
		}
		if _, ok := byID[b.ID]; ok {
			return nil, ErrBoardOrder
		}
		byID[b.ID] = b
		rest++
	}
	seen := make(map[string]bool, len(ids))
	out := make([]config.Board, 0, rest+1)
	out = append(out, main)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || config.SameBoard(id, config.MainBoardID) {
			return nil, ErrBoardOrder
		}
		b, ok := byID[id]
		if !ok || seen[id] {
			return nil, ErrBoardOrder
		}
		seen[id] = true
		out = append(out, b)
	}
	if len(seen) != rest {
		return nil, ErrBoardOrder
	}
	if err := store.Save(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func boardIndex(list []config.Board, id string) int {
	for i, b := range list {
		if config.SameBoard(b.ID, id) {
			return i
		}
	}
	return -1
}

func mainBoardRecord() config.Board {
	return config.Board{ID: config.MainBoardID, Name: mainBoardName}
}

// reservedMainBoardName reports whether name names the built-in main section.
func reservedMainBoardName(name string) bool {
	switch strings.TrimSpace(name) {
	case mainBoardName, "Основной", "Основний":
		return true
	}
	return config.NormalizeBoardID(name) == config.MainBoardID
}

func boardsWithMain(list []config.Board) []config.Board {
	for _, b := range list {
		if config.SameBoard(b.ID, config.MainBoardID) {
			return append([]config.Board(nil), list...)
		}
	}
	out := make([]config.Board, 0, len(list)+1)
	out = append(out, mainBoardRecord())
	out = append(out, list...)
	return out
}

func boardIDFromName(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if (r == ' ' || r == '-' || r == '_') && b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) > 64 {
		runes := []rune(s)
		s = string(runes[:64])
		s = strings.Trim(s, "-")
	}
	return s
}

func uniqueBoardID(existing []config.Board, name string) (string, error) {
	taken := make(map[string]bool, len(existing)+1)
	taken[config.MainBoardID] = true
	for _, b := range existing {
		taken[config.NormalizeBoardID(b.ID)] = true
	}
	base := boardIDFromName(name)
	if base == "" {
		base = "board"
	}
	if config.SameBoard(base, config.MainBoardID) {
		base = "board"
	}
	id := base
	for n := 0; taken[id]; n++ {
		tok, err := security.RandomToken()
		if err != nil {
			return "", err
		}
		suffix := tok
		if len(suffix) > 8 {
			suffix = suffix[:8]
		}
		id = base + "-" + suffix
		if n > 16 {
			return "", errors.New("admin: board id collision")
		}
	}
	return id, nil
}
