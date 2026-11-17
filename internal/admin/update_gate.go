package admin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/cron"
)

// updatePendingTick is how often the pending watcher checks the pending file.
const updatePendingTick = 2 * time.Second

// updatePendingMaxAge is the maximum age of a pending file for it to be considered active.
const updatePendingMaxAge = time.Hour

// WatchUpdatePending watches update/pending every 2 seconds and maintains the keitaro
// update gate. When a pending file is active and the job is completely idle, ready.json
// is written. The goroutine runs until ctx is done. It returns immediately when
// UpdateDir is not configured.
func (s *Server) WatchUpdatePending(ctx context.Context) {
	if s.cfg.UpdateDir == "" {
		return
	}
	tick := time.NewTicker(updatePendingTick)
	defer tick.Stop()
	for {
		s.checkPendingOnce()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// checkPendingOnce reads update/pending and updates the keitaro update gate.
// When pending is active and the keitaro job is idle, ready.json is written.
// When pending is absent or stale the gate is released.
// It does not return an error; failures are silently ignored.
func (s *Server) checkPendingOnce() {
	updateDir := s.cfg.UpdateDir
	pendingPath := filepath.Join(updateDir, cron.UpdatePendingFile)

	pendingText, err := cron.ReadUpdateText(pendingPath, cron.UpdateMaxPending)
	if err != nil {
		s.ktJob.setUpdating(false)
		return
	}
	pendingID := strings.TrimSpace(pendingText)
	if !cron.ValidUpdateID(pendingID) {
		s.ktJob.setUpdating(false)
		return
	}

	// Use Lstat so a symlink at pendingPath is treated as absent, not followed.
	info, statErr := os.Lstat(pendingPath)
	if statErr != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) > updatePendingMaxAge {
		s.ktJob.setUpdating(false)
		return
	}

	// Determine whether pending is active by inspecting state.json.
	// Fail-closed: any read error that is not os.ErrNotExist (bad JSON, symlink, oversized)
	// is treated as "state unknown" and the gate stays up.
	statePath := filepath.Join(updateDir, cron.UpdateStateFile)
	var state cron.StateFile
	stateErr := cron.ReadUpdateJSON(statePath, cron.UpdateMaxJSON, &state)

	// Pending is NOT active only when state.json is readable, has the same ID,
	// and the run is finished (FinishedAt != nil). Every other case is active.
	finished := stateErr == nil && state.ID == pendingID && state.FinishedAt != nil
	if finished {
		s.ktJob.setUpdating(false)
		return
	}

	// Pending is active: raise the gate.
	s.ktJob.setUpdating(true)

	// Write ready.json only when the job is completely idle and it has not yet been
	// written for this id.
	readyPath := filepath.Join(updateDir, cron.UpdateReadyFile)
	var existingReady cron.ReadyFile
	if cron.ReadUpdateJSON(readyPath, cron.UpdateMaxJSON, &existingReady) == nil && existingReady.ID == pendingID {
		return
	}

	if s.ktJob.idleForUpdate() {
		ready := cron.ReadyFile{
			ID:          pendingID,
			KeitaroIdle: true,
			At:          time.Now().UTC(),
		}
		_ = cron.WriteUpdateJSON(readyPath, ready)
	}
}
