package admin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// engineReloadTimeout bounds the wait of a save for the engine to swap its campaign set.
// The engine parses the whole groups file, so it is longer than a plain health probe.
const engineReloadTimeout = 10 * time.Second

// EngineReloader returns the function that tells the engine to re-read the groups file now.
// engineURL is the address the admin uses for the engine; an empty one returns a function that
// does nothing. log may be nil.
// The returned function sends POST /internal/reload and waits for the answer. A refusal or an
// unreachable engine is logged and not returned: the save is already on disk and the engine
// takes it at its next timer tick (EMTDS_RELOAD_INTERVAL), so the write must not fail.
func EngineReloader(engineURL string, log *slog.Logger) func() {
	base := strings.TrimRight(strings.TrimSpace(engineURL), "/")
	if base == "" {
		return func() {}
	}
	client := &http.Client{Timeout: engineReloadTimeout}
	return func() {
		if err := postEngineReload(client, base); err != nil && log != nil {
			log.Warn("engine reload after save failed, it will see the change at its next tick", "err", err)
		}
	}
}

// postEngineReload sends POST /internal/reload to the engine at base.
// client carries the timeout. base is the engine address without a trailing slash.
// It returns an error when the request fails or the engine answers with a status other than 200.
func postEngineReload(client *http.Client, base string) error {
	ctx, cancel := context.WithTimeout(context.Background(), engineReloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/internal/reload", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("engine answered %d", resp.StatusCode)
	}
	return nil
}
