package main

import (
	"net/http"

	"github.com/egerkuzma/kuztds/internal/config"
)

// xrwMaxBytes is the stored length of the X-Requested-With header.
const xrwMaxBytes = 64

// requestXRW returns the visitor X-Requested-With header, at most 64 bytes.
// r is the click request. apiMode is true for an API-client call: those
// headers belong to the calling server, so the result is empty.
// A nil request returns an empty string. There is no error.
func requestXRW(r *http.Request, apiMode bool) string {
	if apiMode || r == nil {
		return ""
	}
	return config.LimitBytes(r.Header.Get("X-Requested-With"), xrwMaxBytes)
}
