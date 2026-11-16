package main

import (
	"net/http"

	"github.com/egerkuzma/emtds/internal/server"
)

// setEngineCookie sends c to the visitor, marked Secure when the request came
// over HTTPS (server.IsHTTPS). Every cookie the engine sets goes through it, so
// the rule lives in one place.
//
// w receives the cookie, r is the request it answers and c is the cookie with
// its name, value, path, lifetime and the other flags set. c is changed in place.
func setEngineCookie(w http.ResponseWriter, r *http.Request, c *http.Cookie) {
	if server.IsHTTPS(r) {
		c.Secure = true
	}
	http.SetCookie(w, c)
}
