package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"sort"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

// entryFile is the file the engine serves as the page of a landing: its path and bytes.
// ok is false when no file is named or the named file does not exist, so nothing is
// served from it.
type entryFile struct {
	rel  string
	data []byte
	ok   bool
}

// resolveEntryFile finds the page of a landing with config.LandingEntry, the function
// the engine calls. entry is the catalog Entry and read returns the bytes of a file and
// whether it exists.
func resolveEntryFile(entry string, read func(rel string) ([]byte, bool)) entryFile {
	rel, ok := config.LandingEntry(entry, func(r string) bool {
		_, found := read(r)
		return found
	})
	if !ok {
		return entryFile{}
	}
	data, found := read(rel)
	return entryFile{rel: rel, data: data, ok: found}
}

// entryChange refuses a landing whose page changes to text the viewer may not write.
// before is the page now and after the page once the operation is done. A page with the
// same path and bytes is allowed; any other page is checked without exception, because
// the engine expands it and the viewer would choose where its data goes.
// It returns the client message, or "" when allowed.
func entryChange(g *macroGuard, before, after entryFile) string {
	if g == nil || !after.ok {
		return ""
	}
	if before.ok && before.rel == after.rel && bytes.Equal(before.data, after.data) {
		return ""
	}
	if msg := g.find(string(after.data)); msg != "" {
		return "landing entry " + after.rel + ": " + msg
	}
	return ""
}

// storedReader returns a reader over the files of landing id in the store.
func storedReader(files LandingFiles, id string) func(rel string) ([]byte, bool) {
	return func(rel string) ([]byte, bool) {
		data, err := files.Read(id, rel)
		return data, err == nil
	}
}

// catalogEntry returns the Entry of the catalog row of landing id, or "" when the store
// or the row is missing or unreadable.
func (s *Server) catalogEntry(ctx context.Context, id string) string {
	if s.cfg.Landings == nil {
		return ""
	}
	list, _ := s.cfg.Landings.List(ctx)
	for _, l := range list {
		if l.ID == id {
			return l.Entry
		}
	}
	return ""
}

// landingFileMacro refuses a landing file the viewer may not write: any file, of any
// path and type, whose bytes hold a macro the viewer may not write, unless the stored
// file of the same path holds exactly these bytes. Any file can become the entry, so no
// file is skipped. g is the guard of the viewer, nil when nothing is forbidden. data is
// the new content. It returns the client message, or "" when allowed.
func landingFileMacro(g *macroGuard, files LandingFiles, id, rel string, data []byte) string {
	if g == nil {
		return ""
	}
	msg := g.find(string(data))
	if msg == "" {
		return ""
	}
	if old, err := files.Read(id, rel); err == nil && bytes.Equal(old, data) {
		return ""
	}
	return msg
}

// fileOp is one change to a landing file: the cleaned path, the new bytes of a write,
// and deleted true for a removal.
type fileOp struct {
	rel     string
	data    []byte
	deleted bool
}

// fileOpMacro checks writing or deleting one file of landing id as g allows: the file
// text itself (a write only) and the page the landing has afterwards. The page is
// computed with the catalog Entry as stored. It returns the client message, or "" when
// allowed.
func (s *Server) fileOpMacro(ctx context.Context, g *macroGuard, files LandingFiles, id string, op fileOp) string {
	if g == nil {
		return ""
	}
	if !op.deleted {
		if msg := landingFileMacro(g, files, id, op.rel, op.data); msg != "" {
			return msg
		}
	}
	entry := s.catalogEntry(ctx, id)
	read := storedReader(files, id)
	after := func(r string) ([]byte, bool) {
		if r == op.rel {
			return op.data, !op.deleted
		}
		return read(r)
	}
	return entryChange(g, resolveEntryFile(entry, read), resolveEntryFile(entry, after))
}

// readArchive returns the files an archive would leave in a landing, by cleaned path,
// the way Unzip extracts them: directories and __MACOSX skipped, one shared top
// directory removed, a later file of the same path replacing an earlier one.
// ok is false for an archive that cannot be read as a whole: not a zip, too many or
// too large files, an unsafe path, or an entry that fails to open or read (checksum,
// unsupported compression). The upload is then refused before anything is checked or
// extracted, for every viewer.
func readArchive(data []byte) (files map[string][]byte, ok bool) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) > landingArchiveFiles {
		return nil, false
	}
	var names []string
	var entries []*zip.File
	for _, zf := range zr.File {
		if skipZipEntry(zf) {
			continue
		}
		names = append(names, strings.ReplaceAll(zf.Name, "\\", "/"))
		entries = append(entries, zf)
	}
	files = make(map[string][]byte, len(entries))
	var total int
	for i, rel := range stripTopDir(names) {
		clean, err := cleanLandingPath(rel)
		if err != nil {
			return nil, false
		}
		rc, err := entries[i].Open()
		if err != nil {
			return nil, false
		}
		body, err := io.ReadAll(io.LimitReader(rc, landingArchiveLimit+1))
		rc.Close()
		if err != nil || len(body) > landingArchiveLimit || total+len(body) > landingArchiveLimit {
			return nil, false
		}
		total += len(body)
		files[clean] = body
	}
	return files, true
}

// archiveMacroUse checks an archive upload as g allows: every file of it, and the page
// the landing has once the archive replaces its directory. The archive sets the catalog
// Entry to detectEntry of its files, so the page is config.LandingEntry over that value
// and the archive content, computed before anything is extracted. archive is the result
// of readArchive. It returns the client message, or "" when allowed.
func (s *Server) archiveMacroUse(ctx context.Context, g *macroGuard, files LandingFiles, id string, archive map[string][]byte) string {
	if g == nil {
		return ""
	}
	rels := make([]string, 0, len(archive))
	for rel := range archive {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if msg := landingFileMacro(g, files, id, rel, archive[rel]); msg != "" {
			return msg
		}
	}
	incoming := func(r string) ([]byte, bool) {
		b, found := archive[r]
		return b, found
	}
	before := resolveEntryFile(s.catalogEntry(ctx, id), storedReader(files, id))
	return entryChange(g, before, resolveEntryFile(detectEntry(rels), incoming))
}

// landingCheck is the userCheck of landings: the macro rules for the record, and the
// page rule. old is nil for a new landing.
// It returns the client message, or "" when allowed.
func (s *Server) landingCheck(v viewer, item config.Landing, old *config.Landing) string {
	if msg := macroCheck(v, item, old); msg != "" {
		return msg
	}
	g := newMacroGuard(v)
	if g == nil || s.cfg.LandingFiles == nil || item.ID == "" {
		return ""
	}
	read := storedReader(s.cfg.LandingFiles, item.ID)
	var before entryFile
	if old != nil {
		before = resolveEntryFile(old.Entry, read)
	}
	return entryChange(g, before, resolveEntryFile(item.Entry, read))
}
