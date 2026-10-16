package main

import "net/http"

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
	return clipBytes(r.Header.Get("X-Requested-With"), xrwMaxBytes)
}

// clipBytes returns s cut to at most n bytes.
// n <= 0 or an empty s yields an empty string. A shorter s is unchanged.
// There is no error.
func clipBytes(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n]
}
