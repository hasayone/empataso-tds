package main

import (
	"net/http"
	"testing"
	"time"
)

// TestEngineServerTimeouts checks the engine HTTP limits and their fallback.
func TestEngineServerTimeouts(t *testing.T) {
	h := http.NotFoundHandler()
	srv := engineServer(":0", h, 0, -time.Second)
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 30*time.Second || srv.IdleTimeout != 120*time.Second {
		t.Fatalf("defaults header=%v read=%v idle=%v", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("write timeout %v must stay off for streamed responses", srv.WriteTimeout)
	}
	if srv.IdleTimeout <= 60*time.Second {
		t.Fatal("idle timeout must outlast nginx upstream keepalive_timeout 60s")
	}
	srv = engineServer(":0", h, 45*time.Second, 90*time.Second)
	if srv.ReadTimeout != 45*time.Second || srv.IdleTimeout != 90*time.Second || srv.Addr != ":0" || srv.Handler == nil {
		t.Fatalf("explicit read=%v idle=%v", srv.ReadTimeout, srv.IdleTimeout)
	}
}
