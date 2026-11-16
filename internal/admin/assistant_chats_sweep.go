package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// aiChatRemoveUser removes every chat of the user uid, as the account is deleted. A turn the user
// still has is canceled first. The changes and the audit stay. It returns the removal error.
func (s *Server) aiChatRemoveUser(uid string) error {
	dir := s.aiChatDirOf(uid)
	if dir == "" || uid == "" {
		return nil
	}
	s.aiTurnCancelUser(uid)
	s.ai.chats.mu.Lock()
	defer s.ai.chats.mu.Unlock()
	return os.RemoveAll(dir)
}

// aiChatDirsLocked lists the user directories under the chat root. The caller holds s.ai.chats.mu.
func (s *Server) aiChatDirsLocked() []string {
	entries, err := os.ReadDir(filepath.Join(s.cfg.AIDir, aiChatsDir))
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(s.cfg.AIDir, aiChatsDir, e.Name()))
		}
	}
	return dirs
}

// aiRecoverChats runs once at start: a chat left "running" by a stopped process becomes
// "interrupted", with its time unchanged. It logs only counts.
func (s *Server) aiRecoverChats() {
	if s.cfg.AIDir == "" {
		return
	}
	s.ai.chats.mu.Lock()
	defer s.ai.chats.mu.Unlock()
	fixed := 0
	for _, dir := range s.aiChatDirsLocked() {
		for _, m := range s.aiChatRebuildLocked(dir) {
			if m.Status != aiChatRunning {
				continue
			}
			raw, err := os.ReadFile(aiChatFilePath(dir, m.ID))
			var c aiChat
			if err != nil || json.Unmarshal(raw, &c) != nil {
				continue
			}
			c.Status = aiChatInterrupted
			if s.aiChatSaveLocked(&c, false) == nil {
				fixed++
			}
		}
	}
	if fixed > 0 {
		s.logInfo("assistant chats interrupted by a restart", "chats", fixed)
	}
}

// aiRotateChats removes every chat that was last changed more than the retention before now,
// except a chat that runs. A user left without chats loses its directory. It returns how many
// chats were removed and the first error.
func (s *Server) aiRotateChats(now time.Time) (int, error) {
	if s.cfg.AIDir == "" {
		return 0, nil
	}
	s.ai.chats.mu.Lock()
	defer s.ai.chats.mu.Unlock()
	cutoff := now.Add(-s.ai.chats.retention())
	removed := 0
	var first error
	for _, dir := range s.aiChatDirsLocked() {
		metas := s.aiChatRebuildLocked(dir)
		kept := metas[:0]
		for _, m := range metas {
			if m.Updated.Before(cutoff) && m.Status != aiChatRunning {
				if err := os.Remove(aiChatFilePath(dir, m.ID)); err != nil && first == nil && !errors.Is(err, os.ErrNotExist) {
					first = err
				}
				removed++
				continue
			}
			kept = append(kept, m)
		}
		var err error
		if len(kept) == 0 {
			err = os.RemoveAll(dir)
		} else if len(kept) != len(metas) {
			err = s.aiChatWriteIndexLocked(dir, kept)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return removed, first
}

// WatchAssistant applies the retention of chats (60 days) and of changes (90 days) at once and
// then every hour until ctx ends. It returns at once when the assistant has no directory. The
// caller starts it once after New: New itself never starts it.
func (s *Server) WatchAssistant(ctx context.Context) {
	if s.cfg.AIDir == "" {
		return
	}
	ticker := time.NewTicker(aiChatSweepEvery)
	defer ticker.Stop()
	for {
		s.aiSweep(time.Now().UTC())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// aiSweep runs one retention pass over chats and changes and logs counts or the failure.
func (s *Server) aiSweep(now time.Time) {
	if n, err := s.aiRotateChats(now); err != nil {
		s.logWarn("assistant chat retention failed", "error_type", fmt.Sprintf("%T", err))
	} else if n > 0 {
		s.logInfo("assistant chats removed", "chats", n)
	}
	if n, err := s.aiPruneChanges(now); err != nil {
		s.logWarn("assistant change retention failed", "error_type", fmt.Sprintf("%T", err))
	} else if n > 0 {
		s.logInfo("assistant changes removed", "changes", n)
	}
}
