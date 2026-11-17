package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

// keitaroFileEntry is one tracker connection in keitaro.json (object or list element).
type keitaroFileEntry struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	BoardID string `json:"board_id,omitempty"`
}

// keitaroConn is a resolved tracker connection with its manifest and sync paths.
type keitaroConn struct {
	Base         string
	Key          string
	BoardID      string
	global       bool
	Manifest     ManifestStore
	SyncFile     string
	manifestPath string
}

// parseKeitaroFile reads keitaro.json as one object or a list of connections.
// A single object is the legacy global connection. A JSON array holds one entry per section.
func parseKeitaroFile(raw []byte) (entries []keitaroFileEntry, list bool, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false, fmt.Errorf("empty file")
	}
	if raw[0] == '[' {
		var out []keitaroFileEntry
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, true, err
		}
		return out, true, nil
	}
	var one keitaroFileBody
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, false, err
	}
	return []keitaroFileEntry{{
		BaseURL: one.BaseURL,
		APIKey:  one.APIKey,
	}}, false, nil
}

// keitaroConnPaths returns manifest and sync file paths for one connection board label.
// Empty boardLabel keeps the legacy keitaro-import.json and keitaro-sync.json names.
func keitaroConnPaths(stateDir, boardLabel string) (manifest, sync string) {
	if boardLabel == "" {
		return filepath.Join(stateDir, "keitaro-import.json"),
			filepath.Join(stateDir, "keitaro-sync.json")
	}
	return filepath.Join(stateDir, "keitaro-import-"+boardLabel+".json"),
		filepath.Join(stateDir, "keitaro-sync-"+boardLabel+".json")
}

// buildKeitaroConns turns file entries into live connections with stores.
func buildKeitaroConns(stateDir string, entries []keitaroFileEntry, defaultManifest ManifestStore, defaultSync string) ([]keitaroConn, error) {
	out := make([]keitaroConn, 0, len(entries))
	for _, e := range entries {
		base, err := normalizeKeitaroBase(e.BaseURL)
		if err != nil || strings.TrimSpace(e.APIKey) == "" {
			return nil, fmt.Errorf("base_url or api_key is not valid")
		}
		boardLabel := ""
		boardNorm := config.MainBoardID
		if strings.TrimSpace(e.BoardID) != "" {
			boardNorm = config.NormalizeBoardID(e.BoardID)
			boardLabel = boardNorm
		}
		manifestPath, syncPath := keitaroConnPaths(stateDir, boardLabel)
		var manifest ManifestStore
		if boardLabel == "" && defaultManifest != nil {
			manifest = defaultManifest
		} else {
			manifest = NewFileManifest(manifestPath)
		}
		syncFile := syncPath
		if boardLabel == "" && defaultSync != "" {
			syncFile = defaultSync
		}
		out = append(out, keitaroConn{
			Base:         base,
			Key:          e.APIKey,
			BoardID:      boardNorm,
			global:       boardLabel == "",
			Manifest:     manifest,
			SyncFile:     syncFile,
			manifestPath: manifestPath,
		})
	}
	return out, nil
}

// importBoardFrom resolves the target section for one import from the body and request context.
func importBoardFrom(req keitaroImportReq, ctx context.Context) string {
	fallback := boardFrom(ctx)
	if strings.TrimSpace(req.Board) != "" {
		return config.NormalizeBoardID(req.Board)
	}
	return fallback
}

// keitaroConnForBoard picks the connection for an import on board.
// A section-specific entry wins over a global entry with empty board_id.
func (s *Server) keitaroConnForBoard(board string) keitaroConn {
	board = config.NormalizeBoardID(board)
	s.ktMu.RLock()
	defer s.ktMu.RUnlock()
	var global *keitaroConn
	var exact *keitaroConn
	for i := range s.ktConns {
		c := &s.ktConns[i]
		if c.global {
			if global == nil {
				global = c
			}
			continue
		}
		if config.SameBoard(c.BoardID, board) {
			exact = c
		}
	}
	if exact != nil {
		return *exact
	}
	if global != nil {
		return *global
	}
	return keitaroConn{Base: s.ktBase, Key: s.ktKey, BoardID: config.MainBoardID, global: true, Manifest: s.cfg.Manifest, SyncFile: s.cfg.KeitaroSyncFile}
}

// keitaroImportBundle is creds and manifest store for one import board.
func (s *Server) keitaroImportBundle(board string) (ktCreds, ManifestStore) {
	c := s.keitaroConnForBoard(board)
	creds := ktCreds{Base: c.Base, Key: c.Key}
	store := c.Manifest
	if store == nil {
		store = s.cfg.Manifest
	}
	return creds, store
}

// listKeitaroConns returns a copy of configured tracker connections.
func (s *Server) listKeitaroConns() []keitaroConn {
	s.ktMu.RLock()
	defer s.ktMu.RUnlock()
	if len(s.ktConns) == 0 {
		return nil
	}
	return append([]keitaroConn(nil), s.ktConns...)
}

// reloadKeitaroConns reads keitaro.json and rebuilds s.ktConns. ktBase and ktKey mirror the first entry.
func (s *Server) reloadKeitaroConns() error {
	env, source := s.keitaroEnv()
	stateDir := ""
	if s.cfg.KeitaroFile != "" {
		stateDir = filepath.Dir(s.cfg.KeitaroFile)
	}
	if s.cfg.KeitaroFile == "" {
		s.ktMu.Lock()
		s.ktConns = nil
		s.ktFileList = false
		s.ktBase, s.ktKey, s.ktSource = env.Base, env.Key, source
		s.ktMu.Unlock()
		return nil
	}
	raw, err := os.ReadFile(s.cfg.KeitaroFile)
	if errors.Is(err, os.ErrNotExist) {
		s.ktMu.Lock()
		s.ktConns = nil
		s.ktFileList = false
		s.ktBase, s.ktKey, s.ktSource = env.Base, env.Key, source
		s.ktMu.Unlock()
		return nil
	}
	if err != nil {
		return fmt.Errorf("admin: keitaro file %s: %w", s.cfg.KeitaroFile, err)
	}
	entries, list, err := parseKeitaroFile(raw)
	if err != nil {
		return fmt.Errorf("admin: keitaro file %s: %w", s.cfg.KeitaroFile, err)
	}
	conns, err := buildKeitaroConns(stateDir, entries, s.cfg.Manifest, s.cfg.KeitaroSyncFile)
	if err != nil {
		return fmt.Errorf("admin: keitaro file %s: %w", s.cfg.KeitaroFile, err)
	}
	s.ktMu.Lock()
	s.ktConns = conns
	s.ktFileList = list
	if len(conns) > 0 {
		s.ktBase, s.ktKey, s.ktSource = conns[0].Base, conns[0].Key, keitaroSourceFile
	} else {
		s.ktBase, s.ktKey, s.ktSource = env.Base, env.Key, source
	}
	s.ktMu.Unlock()
	return nil
}
