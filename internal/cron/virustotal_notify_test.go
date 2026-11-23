package cron

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestVirusTotalNotifyOffSkipsTelegram checks that a flagged domain is still
// counted when the notify switch is off, and that Telegram is not called.
func TestVirusTotalNotifyOffSkipsTelegram(t *testing.T) {
	e := newEnv(t)
	e.mux.HandleFunc("/api/v3/domains/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, vtBody(3, 1))
	})
	off := false
	cfg := Default()
	cfg.Telegram = tg
	cfg.VirusTotal = VirusTotal{APIKey: "k", Domains: []string{"bad.example"}, Threshold: 1, Notify: &off}
	msg, err := e.r.runVirusTotal(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "1 flagged") || !strings.Contains(msg, "bad.example") {
		t.Fatalf("message = %q", msg)
	}
	if len(e.messages()) != 0 {
		t.Fatalf("telegram = %q", e.messages())
	}
	var flagged bool
	e.r.state(func(st *Status) { _, flagged = st.Flagged["bad.example"] })
	if !flagged {
		t.Fatal("the domain must stay flagged so a later switch-on does not repeat the alert")
	}
}
