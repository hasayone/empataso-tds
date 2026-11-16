package admin

import (
	"context"
	"net/http"
	"sync"
)

// catalogHooks assigns a number and tidies the list around one catalog.
// A nil hooks value means no numbering: an empty id is rejected by valid.
// onDelete runs after the record is removed, the file is saved and the write lock is released.
// view, when set, shapes the GET list response.
type catalogHooks[T any] struct {
	seq      *idSeq
	floor    func() (int, error)
	prep     func(*T)
	tidy     func([]T, string) []T
	onDelete func(id string)
	// view converts the list for the GET response. Nil returns the list as stored.
	view func(ctx context.Context, list []T) (any, error)
	res  string
	// mu, when set, serializes List, modify and Save in create, replace and delete.
	mu *sync.Mutex
	// keep copies server-owned fields from the stored record into the replacement on PUT.
	// It runs after prep and before valid. Nil keeps the request body as sent.
	keep func(item *T, old T)
	// guard runs on PUT after keep and may adjust item. A non-zero status refuses the write
	// with that status and message. Nil allows every replacement.
	guard func(item *T, old T) (int, string)
	// guardDelete runs on DELETE before the record is removed. A non-zero status refuses
	// the delete with that status and message. Nil allows every delete.
	guardDelete func(old T) (int, string)
	// beforeInsert runs inside insert, under mu, after the id check and with the stored list.
	// A non-zero status refuses the new record with that status and message. Nil allows it.
	beforeInsert func(item T, list []T) (int, string)
	// preDelete runs first on DELETE, before any lock is taken. It returns true when it has
	// answered the request itself. Nil leaves every DELETE to the common path.
	preDelete func(w http.ResponseWriter, r *http.Request) bool
	// label and owner feed grant checks. setOwner writes CreatedBy on create.
	label    func(T) string
	owner    func(T) string
	setOwner func(*T, string)
	// boardID reads the section id of a row. When set, list/get/insert are scoped to the current section.
	boardID func(T) string
	// setBoard writes the section id on create. Nil leaves board_id as sent.
	setBoard  func(*T, string)
	userLimit func(*T, *T) string
	// userCheck runs for a non-admin caller on create (old is nil) and replace, after
	// userLimit. A non-empty message refuses the write with 400. Nil allows it.
	// ctx is the request context, for a check that reads another store.
	userCheck func(ctx context.Context, v viewer, item T, old *T) string
	// maskOut removes the values the caller may not see from item before list, get,
	// create and replace send it. Nil sends item as stored.
	maskOut func(v viewer, item *T)
	// afterWrite runs after a successful create or replace save; it must not block the response.
	// old is nil on create and the previous row on replace.
	afterWrite func(item T, old *T)
	// formatOne shapes a single-item JSON response; nil sends the stored record.
	formatOne func(ctx context.Context, s *Server, item T) (any, error)
	// keepIn runs on a non-admin write: it puts the stored value back into item for
	// every field the caller may not see, or the zero value when old is nil. Nil keeps the body.
	keepIn func(v viewer, item *T, old *T)
	// restoreCheck runs when a deleted record is put back, under the write lock, and may read
	// other stores. A non-empty message means a record the item links to is gone; the restore
	// is refused with ai_conflict. Nil accepts every record.
	restoreCheck func(ctx context.Context, item T) string
}

// crudCatalog holds shared state for one JSON catalog's HTTP handlers.
type crudCatalog[T any] struct {
	s     *Server
	store *JSONStore[T]
	idOf  func(T) string
	setID func(*T, string)
	valid func(T) string
	hooks *catalogHooks[T]
}

// catalogItemRef builds a grant ref for item using hooks label and owner funcs.
func catalogItemRef[T any](idOf func(T) string, hooks *catalogHooks[T], item T) ref {
	r := ref{id: idOf(item)}
	if hooks != nil {
		if hooks.label != nil {
			r.label = hooks.label(item)
		}
		if hooks.owner != nil {
			r.owner = hooks.owner(item)
		}
		if hooks.boardID != nil {
			r.board = hooks.boardID(item)
		}
	}
	return r
}

// visibleCatalog returns list entries the viewer may read for hooks.res.
// A row is kept when seesBoard allows its section, then when canSee allows the record.
// hooks may be nil. idOf reads the record id. list is the stored catalog and is not modified.
// It does not return an error.
func visibleCatalog[T any](v viewer, hooks *catalogHooks[T], idOf func(T) string, list []T) []T {
	if hooks != nil && hooks.boardID != nil {
		filtered := make([]T, 0, len(list))
		for _, item := range list {
			if v.seesBoard(hooks.boardID(item)) {
				filtered = append(filtered, item)
			}
		}
		list = filtered
	}
	if v.admin || hooks == nil || hooks.res == "" {
		return list
	}
	out := make([]T, 0, len(list))
	for _, item := range list {
		if v.canSee(hooks.res, catalogItemRef(idOf, hooks, item)) {
			out = append(out, item)
		}
	}
	return out
}

// maskedItem returns item after hooks.maskOut for v. item is a copy, so the stored
// record is not touched. An administrator gets item as it is.
func maskedItem[T any](v viewer, hooks *catalogHooks[T], item T) T {
	if !v.admin && hooks != nil && hooks.maskOut != nil {
		hooks.maskOut(v, &item)
	}
	return item
}

// maskedCatalog returns list with hooks.maskOut applied for v. The input slice is not
// modified: a masked copy is returned, and an administrator gets the list as it is.
func maskedCatalog[T any](v viewer, hooks *catalogHooks[T], list []T) []T {
	if v.admin || hooks == nil || hooks.maskOut == nil {
		return list
	}
	out := make([]T, len(list))
	for i, item := range list {
		out[i] = maskedItem(v, hooks, item)
	}
	return out
}

// macroCheck is the userCheck of catalogs whose records hold links: it refuses a record
// that gained a macro of a field hidden for v. old is nil for a new record.
func macroCheck[T any](v viewer, item T, old *T) string {
	var base any
	if old != nil {
		base = *old
	}
	return hiddenMacroUse(v, item, base)
}

// applyCatalogCreatedBy sets CreatedBy on item from old unless an admin sent a non-empty value.
func applyCatalogCreatedBy[T any](v viewer, hooks *catalogHooks[T], item *T, old T) {
	if hooks == nil || hooks.setOwner == nil || hooks.owner == nil {
		return
	}
	if v.admin {
		if hooks.owner(*item) == "" {
			hooks.setOwner(item, hooks.owner(old))
		}
		return
	}
	hooks.setOwner(item, hooks.owner(old))
}
