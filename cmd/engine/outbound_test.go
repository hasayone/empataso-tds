package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/hasayone/empataso-tds/internal/security"
)

// logCapture is a slog.Handler that records log messages for assertion in tests.
type logCapture struct {
	mu   sync.Mutex
	msgs []string
}

// Enabled reports whether the handler is active for the given level.
// Returns true for all levels so no message is dropped.
func (h *logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

// Handle appends the record message to the captured list.
func (h *logCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}

// WithAttrs returns the same handler; attributes are not inspected in tests.
func (h *logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }

// WithGroup returns the same handler; groups are not inspected in tests.
func (h *logCapture) WithGroup(string) slog.Handler { return h }

// hasMessage reports whether any captured message contains sub.
func (h *logCapture) hasMessage(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msgs {
		if len(m) >= len(sub) {
			for i := 0; i <= len(m)-len(sub); i++ {
				if m[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

func TestOutboundURLAllowedNewNames(t *testing.T) {
	blocked := []string{
		"http://php/",
		"http://admin:8090/",
		"http://cron/x",
		"http://[::1]/",
		"http://nohost/", // single-label name without a dot
	}
	allowed := []string{
		"http://[2001:db8::1]/",
		"https://example.com/",
	}
	for _, u := range blocked {
		if outboundURLAllowed(u) {
			t.Errorf("outboundURLAllowed(%q) = true, want false", u)
		}
	}
	for _, u := range allowed {
		if !outboundURLAllowed(u) {
			t.Errorf("outboundURLAllowed(%q) = false, want true", u)
		}
	}
}

// guardBlockTransport is a RoundTripper that simulates a GuardDial rejection by
// returning ErrPrivateAddr wrapped in a net.OpError, as the real dial would.
type guardBlockTransport struct{}

func (guardBlockTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: security.ErrPrivateAddr,
	}
}

// TestSendOutboundBlockedOutcome verifies that when the guarded transport rejects
// the connection with ErrPrivateAddr, sendOutbound writes "blocked" to the
// postback log and increments the failed counter.
func TestSendOutboundBlockedOutcome(t *testing.T) {
	logs := &logCapture{}
	d := &engineDeps{log: slog.New(logs)}

	// Use a URL that passes outboundURLAllowed (public FQDN with dot) so the
	// rejection comes from the transport, not from the URL pre-filter.
	job := outboundJob{
		Method: "GET",
		URL:    "https://postback.example.com/pb?cid=abc",
		CID:    "abc",
		Status: "sale",
	}

	client := &http.Client{Transport: guardBlockTransport{}}
	d.sendOutbound(client, job)

	if d.stats.outbound.failed.Load() != 1 {
		t.Errorf("failed counter = %d, want 1", d.stats.outbound.failed.Load())
	}
	if d.stats.outbound.sent.Load() != 0 {
		t.Errorf("sent counter = %d, want 0", d.stats.outbound.sent.Load())
	}
	if !logs.hasMessage("blocked") {
		t.Errorf("expected a log message containing %q, got %v", "blocked", logs.msgs)
	}
}
