package admin

import (
	"context"
	"net/http"
	"strconv"
)

// insert numbers, normalizes, validates and appends one new record.
// item is the decoded body. uid is the creating user, or empty for the environment administrator.
// It runs prep, takes an id from the counter when the id is empty, runs valid, sets the owner,
// and saves under hooks.mu. Both the HTTP create and server-side creators such as domain
// registration go through it.
// It returns the stored record and http.StatusCreated, or the zero record with an error status
// (400 invalid, 409 duplicate id, 500 store failure) and the client message.
func (c crudCatalog[T]) insert(ctx context.Context, item T, uid string) (T, int, string) {
	var zero T
	if c.hooks != nil && c.hooks.seq != nil {
		c.hooks.seq.mu.Lock()
		defer c.hooks.seq.mu.Unlock()
	}
	if c.hooks != nil && c.hooks.prep != nil {
		c.hooks.prep(&item)
	}
	if c.idOf(item) == "" && c.hooks != nil && c.hooks.seq != nil {
		floor := 1
		if c.hooks.floor != nil {
			n, err := c.hooks.floor()
			if err != nil {
				return zero, http.StatusInternalServerError, "read error"
			}
			floor = n
		}
		n, err := c.hooks.seq.take(floor)
		if err != nil {
			return zero, http.StatusInternalServerError, "save error"
		}
		c.setID(&item, strconv.Itoa(n))
	} else if c.hooks != nil && c.hooks.seq != nil {
		c.hooks.seq.note(c.idOf(item))
	}
	if msg := c.valid(item); msg != "" {
		return zero, http.StatusBadRequest, msg
	}
	if c.hooks != nil && c.hooks.setOwner != nil {
		c.hooks.setOwner(&item, uid)
	}
	if c.hooks != nil && c.hooks.setBoard != nil {
		c.hooks.setBoard(&item, boardFrom(ctx))
	}
	var unlockMu func()
	if c.hooks != nil && c.hooks.mu != nil {
		c.hooks.mu.Lock()
		unlockMu = c.hooks.mu.Unlock
		defer func() {
			if unlockMu != nil {
				unlockMu()
			}
		}()
	}
	list, err := c.store.List(ctx)
	if err != nil {
		return zero, http.StatusInternalServerError, "read error"
	}
	for _, cur := range list {
		if c.idOf(cur) == c.idOf(item) {
			return zero, http.StatusConflict, "already exists"
		}
	}
	if c.hooks != nil && c.hooks.beforeInsert != nil {
		if code, msg := c.hooks.beforeInsert(item, list); code != 0 {
			return zero, code, msg
		}
	}
	list = append(list, item)
	if c.hooks != nil && c.hooks.tidy != nil {
		list = c.hooks.tidy(list, c.idOf(item))
	}
	if err := c.store.Save(ctx, list); err != nil {
		return zero, http.StatusInternalServerError, "save error"
	}
	if unlockMu != nil {
		unlockMu()
		unlockMu = nil
	}
	if c.hooks != nil && c.hooks.afterWrite != nil {
		c.hooks.afterWrite(item, nil)
	}
	return item, http.StatusCreated, ""
}
