package admin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	// aiLandingMaxBytes is the most file data a landing snapshot may hold: 10 MiB.
	aiLandingMaxBytes = 10 << 20
	// aiLandingMaxFiles is the most files, and the most directories, a landing snapshot may hold.
	aiLandingMaxFiles = 2000
	// aiLandingDirMode and aiLandingFileMode are the modes of what a restore writes, as the landing
	// file store writes them.
	aiLandingDirMode  = 0o755
	aiLandingFileMode = 0o644
)

// errAILandingSize and errAILandingFiles are 409 ai_landing_too_large: the landing is over the
// snapshot limits, so a delete would lose files the revert cannot bring back. Their params are
// {"reason": "size" | "files", "limit": <bytes | count>}.
var (
	errAILandingSize = newAIErrorP(http.StatusConflict, aiErrLandingTooLarge,
		"the landing holds more than 10 MiB; it cannot be deleted by the assistant",
		map[string]any{"reason": aiReasonSize, "limit": aiLandingMaxBytes})
	errAILandingFiles = newAIErrorP(http.StatusConflict, aiErrLandingTooLarge,
		"the landing has more than 2000 files or directories; it cannot be deleted by the assistant",
		map[string]any{"reason": aiReasonFiles, "limit": aiLandingMaxFiles})
)

// errAILandingUnsafe is 409 ai_landing_too_large for a landing that holds a symbolic link, a
// special file or a directory that is not a plain one: they cannot be archived and restored
// as they are, so the assistant leaves the landing alone.
var errAILandingUnsafe = newAIErrorP(http.StatusConflict, aiErrLandingTooLarge,
	"the landing holds a symbolic link or a special file; it cannot be archived, so the assistant cannot delete it",
	map[string]any{"reason": aiReasonSymlink})

// aiLandingNode is one entry of a landing directory: the path relative to the landing, with
// forward slashes, whether it is a directory, and the size of a file.
type aiLandingNode struct {
	rel  string
	dir  bool
	size int64
}

// aiLandingRoot returns the directory of the landing id, "" when the server has no landing files
// store. Only the file store of the panel is supported: any other store is a 500 error.
func (s *Server) aiLandingRoot(id string) (string, error) {
	switch f := s.cfg.LandingFiles.(type) {
	case nil:
		return "", nil
	case *FileLandings:
		root, err := f.root(id)
		if err != nil {
			return "", aiBadPatch("bad landing id")
		}
		return root, nil
	}
	return "", aiPlainError(http.StatusInternalServerError, "the landing files store cannot be archived")
}

// aiLandingSafeName reports whether name is a relative path with forward slashes that cannot
// leave the landing: not empty, not absolute, no NUL or backslash, and no empty, "." or ".." part.
// A name that starts with a dot is fine: .htaccess is a landing file.
func aiLandingSafeName(name string) bool {
	if name == "" || len(name) > 4096 || strings.ContainsAny(name, "\x00\\") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// aiLandingWalk lists the landing directory root, hidden files included, without following any
// link. A missing directory is an empty landing. It returns errAILandingUnsafe for a symbolic
// link, a special file, or a root that is not a plain directory, errAILandingFiles or
// errAILandingSize as soon as the count of files or directories or the file bytes pass the limits,
// or the error of the walk.
func aiLandingWalk(root string) ([]aiLandingNode, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errAILandingUnsafe
	}
	var out []aiLandingNode
	var files, dirs int
	var total int64
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !aiLandingSafeName(rel) {
			return errAILandingUnsafe
		}
		switch {
		case d.IsDir():
			if dirs++; dirs > aiLandingMaxFiles {
				return errAILandingFiles
			}
			out = append(out, aiLandingNode{rel: rel, dir: true})
		case d.Type().IsRegular():
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if files++; files > aiLandingMaxFiles {
				return errAILandingFiles
			}
			if total += fi.Size(); total > aiLandingMaxBytes {
				return errAILandingSize
			}
			out = append(out, aiLandingNode{rel: rel, size: fi.Size()})
		default:
			return errAILandingUnsafe
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// aiLandingScan counts the files of the landing id, hidden ones included, and checks that a
// snapshot can hold them. A landing without a files store or without a directory has no files.
// It returns the count and the byte total, errAILandingFiles or errAILandingSize when a limit is passed,
// errAILandingUnsafe for a link or a special file, or the error of the walk.
func (s *Server) aiLandingScan(id string) (aiLandingFiles, error) {
	var stats aiLandingFiles
	root, err := s.aiLandingRoot(id)
	if err != nil || root == "" {
		return stats, err
	}
	nodes, err := aiLandingWalk(root)
	if err != nil {
		return stats, err
	}
	for _, n := range nodes {
		if !n.dir {
			stats.Count++
			stats.Bytes += n.size
		}
	}
	return stats, nil
}

// aiLandingReadFile reads one regular file without following a link: the path must be a regular
// file by lstat, and the opened file must be that same file. At most the byte limit plus one
// byte is read. It returns errAILandingUnsafe when the path is not that file.
func aiLandingReadFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errAILandingUnsafe
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) {
		return nil, errAILandingUnsafe
	}
	return io.ReadAll(io.LimitReader(f, aiLandingMaxBytes+1))
}

// aiLandingSnapshot archives the landing id, hidden files and empty directories included, into
// the tar.gz file at path.
// It checks the limits again right before the copy, so a landing that grew since the proposal is
// errAILandingSize and nothing is written; a link or a special file is errAILandingUnsafe.
// It refuses with 507 ai_disk_full when the snapshots would pass the size cap of the assistant
// directory. The file is written durably with mode 0600. It returns the error of the checks, of
// reading a file or of the write.
func (s *Server) aiLandingSnapshot(id, path string) error {
	if path == "" {
		return errAIChgNoDir
	}
	root, err := s.aiLandingRoot(id)
	if err != nil {
		return err
	}
	var nodes []aiLandingNode
	if root != "" {
		if nodes, err = aiLandingWalk(root); err != nil {
			return err
		}
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	var total int64
	for _, n := range nodes {
		hdr := &tar.Header{Name: n.rel, Mode: aiLandingFileMode, Typeflag: tar.TypeReg}
		var data []byte
		if n.dir {
			hdr.Name, hdr.Mode, hdr.Typeflag = n.rel+"/", aiLandingDirMode, tar.TypeDir
		} else {
			if data, err = aiLandingReadFile(filepath.Join(root, filepath.FromSlash(n.rel))); err != nil {
				return err
			}
			if total += int64(len(data)); total > aiLandingMaxBytes {
				return errAILandingSize
			}
			hdr.Size = int64(len(data))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if s.aiBlobsBytes()+int64(buf.Len()) > aiChangesDirCap {
		return newAIError(http.StatusInsufficientStorage, aiErrDiskFull, "no room for the landing snapshot")
	}
	return aiChgWriteDurable(path, buf.Bytes())
}

// aiBlobsBytes returns the total size of the snapshot files. A missing directory is zero.
func (s *Server) aiBlobsBytes() int64 {
	entries, err := os.ReadDir(filepath.Join(s.cfg.AIDir, aiBlobsDir))
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			total += info.Size()
		}
	}
	return total
}

// aiLandingMustBeFree checks that the landing id holds no file, as a revert of a create needs
// before it removes the directory and a restore needs before it writes into it. A directory that
// is missing, or holds only empty directories, is free.
// It returns 409 ai_conflict when a file, a link or a special file is there, or the error of the
// walk.
func (s *Server) aiLandingMustBeFree(id string) error {
	root, err := s.aiLandingRoot(id)
	if err != nil || root == "" {
		return err
	}
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return aiChgConflict(aiReasonLandingDir, "the landing path is not a directory")
	}
	taken := errors.New("taken")
	err = filepath.WalkDir(root, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return taken
		}
		return nil
	})
	if errors.Is(err, taken) {
		return aiChgConflict(aiReasonLandingDir, "the landing has files that the assistant did not create")
	}
	return err
}

// aiLandingRestore unpacks the snapshot at path into the landing id, byte for byte.
// The landing must have no files: a directory that holds files is 409 ai_conflict and nothing is
// touched. Every entry must be a regular file or a directory with a safe relative name (no
// absolute path, "..", backslash or NUL; a leading dot is fine), the count and size stay under the
// snapshot limits, and the whole archive is read before the first file is written, so a damaged
// snapshot writes nothing. A missing snapshot is 409 ai_conflict.
// It returns true once files were written (the caller removes them when the later save fails), or
// the error; after a failed write it removes what it wrote itself.
func (s *Server) aiLandingRestore(id, path string) (bool, error) {
	if path == "" {
		return false, aiChgConflict(aiReasonSnapshot, "the landing snapshot is missing")
	}
	nodes, err := aiLandingRead(path)
	if err != nil {
		return false, err
	}
	root, err := s.aiLandingRoot(id)
	if err != nil || root == "" {
		return false, err
	}
	if err := s.aiLandingMustBeFree(id); err != nil {
		return false, err
	}
	wrote := false
	fail := func() (bool, error) {
		_ = os.RemoveAll(root)
		return false, aiChgConflict(aiReasonRestore, "the landing files cannot be restored")
	}
	for _, n := range nodes {
		full := filepath.Join(root, filepath.FromSlash(n.rel))
		if n.dir {
			if err := os.MkdirAll(full, aiLandingDirMode); err != nil {
				return fail()
			}
			wrote = true
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), aiLandingDirMode); err != nil {
			return fail()
		}
		if err := writeFileAtomic(full, n.data, aiLandingFileMode); err != nil {
			return fail()
		}
		wrote = true
	}
	return wrote, nil
}

// aiLandingEntry is one entry of a snapshot: a relative name, and the content of a file.
type aiLandingEntry struct {
	rel  string
	dir  bool
	data []byte
}

// aiLandingRead reads the whole snapshot at path into memory and checks every entry as
// aiLandingRestore requires. It returns 409 ai_conflict for a missing, damaged or unsafe snapshot.
func aiLandingRead(path string) ([]aiLandingEntry, error) {
	bad := aiChgConflict(aiReasonSnapshot, "the landing snapshot is damaged or missing")
	f, err := os.Open(path)
	if err != nil {
		return nil, bad
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, bad
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var out []aiLandingEntry
	var files, dirs int
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, bad
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if !aiLandingSafeName(name) {
			return nil, bad
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if dirs++; dirs > aiLandingMaxFiles {
				return nil, bad
			}
			out = append(out, aiLandingEntry{rel: name, dir: true})
		case tar.TypeReg:
			if files++; files > aiLandingMaxFiles || strings.HasSuffix(hdr.Name, "/") {
				return nil, bad
			}
			data, err := io.ReadAll(io.LimitReader(tr, aiLandingMaxBytes-total+1))
			if err != nil {
				return nil, bad
			}
			if total += int64(len(data)); total > aiLandingMaxBytes {
				return nil, bad
			}
			out = append(out, aiLandingEntry{rel: name, data: data})
		default:
			return nil, bad
		}
	}
}
