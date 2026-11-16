package admin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chgTarGz builds a snapshot archive from entries given as name and body.
func chgTarGz(t *testing.T, typeflag byte, entries ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, en := range entries {
		hdr := &tar.Header{Name: en[0], Mode: 0o644, Size: int64(len(en[1])), Typeflag: typeflag}
		if typeflag == tar.TypeSymlink {
			hdr.Size, hdr.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(en[1])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestAssistantChangesLandingTooLargeAtProposal checks a landing over the limits is not deleted and nothing is touched.
func TestAssistantChangesLandingTooLargeAtProposal(t *testing.T) {
	t.Run("bytes", func(t *testing.T) {
		e := newChgEnv(t)
		if err := e.s.cfg.LandingFiles.Write("l9", "big.bin", make([]byte, aiLandingMaxBytes+1)); err != nil {
			t.Fatal(err)
		}
		before := e.landingBytes(t, "l9")
		_, err := e.propose(t, "landings", aiOpDelete, `{"id":"l9"}`)
		wantChgErr(t, "bytes", err, http.StatusConflict, aiErrLandingTooLarge)
		if len(e.landingBytes(t, "l9")) != len(before) || e.hashOf(t, "landings", "l9") == "" {
			t.Fatal("a refused proposal changed the landing")
		}
	})
	t.Run("files", func(t *testing.T) {
		e := newChgEnv(t)
		for i := 0; i <= aiLandingMaxFiles; i++ {
			if err := e.s.cfg.LandingFiles.Write("l9", fmt.Sprintf("f/%d.txt", i), []byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		_, err := e.propose(t, "landings", aiOpDelete, `{"id":"l9"}`)
		wantChgErr(t, "files", err, http.StatusConflict, aiErrLandingTooLarge)
	})
}

// TestAssistantApplyLandingGrewAfterProposal checks the limit is checked again at apply, before anything is deleted.
func TestAssistantApplyLandingGrewAfterProposal(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	if err := e.s.cfg.LandingFiles.Write("l9", "big.bin", make([]byte, aiLandingMaxBytes+1)); err != nil {
		t.Fatal(err)
	}
	before := e.hashOf(t, "landings", "l9")
	_, err := e.apply(v.ID, aiApplyOpts{ConfirmDelete: true})
	wantChgErr(t, "grew", err, http.StatusConflict, aiErrLandingTooLarge)
	if e.hashOf(t, "landings", "l9") != before || len(e.landingBytes(t, "l9")) != 4 {
		t.Fatal("something was deleted although the snapshot failed")
	}
	if st := e.status(t, v.ID); st != aiChangeAborted {
		t.Fatalf("status %q, want aborted", st)
	}
	matches, _ := filepath.Glob(filepath.Join(e.dir, "assistant", "blobs", "*"))
	if len(matches) != 0 {
		t.Fatalf("snapshot files left: %v", matches)
	}
}

// TestAssistantChangesLandingProposalCountsFiles checks the card tells how many files a delete archives.
func TestAssistantChangesLandingProposalCountsFiles(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	if v.LandingFiles == nil || v.LandingFiles.Count != 3 || v.LandingFiles.Bytes == 0 {
		t.Fatalf("landing files %+v", v.LandingFiles)
	}
}

// TestAssistantLandingRestoreRefusesUnsafeSnapshots checks a damaged or hostile archive writes nothing.
func TestAssistantLandingRestoreRefusesUnsafeSnapshots(t *testing.T) {
	cases := map[string][]byte{}
	t.Run("build", func(t *testing.T) {
		cases["dotdot"] = chgTarGz(t, tar.TypeReg, [2]string{"ok.html", "a"}, [2]string{"../evil.html", "b"})
		cases["absolute"] = chgTarGz(t, tar.TypeReg, [2]string{"/etc/evil.html", "b"})
		cases["backslash"] = chgTarGz(t, tar.TypeReg, [2]string{`a\b.html`, "b"})
		cases["symlink"] = chgTarGz(t, tar.TypeSymlink, [2]string{"link.html", ""})
		cases["garbage"] = []byte("not an archive")
	})
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			e := newChgEnv(t)
			if err := e.s.cfg.LandingFiles.Remove("l9"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(e.dir, "snap.tar.gz")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			wrote, err := e.s.aiLandingRestore("l9", path)
			wantChgErr(t, name, err, http.StatusConflict, aiErrConflict)
			if wrote || len(e.landingBytes(t, "l9")) != 0 {
				t.Fatal("a refused snapshot wrote files")
			}
			if _, err := os.Stat(filepath.Join(e.dir, "evil.html")); err == nil {
				t.Fatal("a file escaped the landing directory")
			}
		})
	}
}

// TestAssistantLandingRestoreMissingSnapshot checks a revert without its snapshot is a conflict and restores nothing.
func TestAssistantLandingRestoreMissingSnapshot(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	e.mustApply(t, v.ID)
	if err := os.Remove(filepath.Join(e.dir, "assistant", "blobs", v.ID+".tar.gz")); err != nil {
		t.Fatal(err)
	}
	_, err := e.s.revertChange(context.Background(), e.root, v.ID)
	wantChgErr(t, "missing snapshot", err, http.StatusConflict, aiErrConflict)
	if e.hashOf(t, "landings", "l9") != "" || len(e.landingBytes(t, "l9")) != 0 {
		t.Fatal("a record or files came back without a snapshot")
	}
}

// TestAssistantRevertRestoresHiddenFilesAndEmptyDirs checks a landing comes back whole: a hidden
// file, a hidden directory and an empty directory included.
func TestAssistantRevertRestoresHiddenFilesAndEmptyDirs(t *testing.T) {
	e := newChgEnv(t)
	root := filepath.Join(e.dir, "landing-files", "l9")
	if err := os.WriteFile(filepath.Join(root, ".htaccess"), []byte("Options -Indexes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".well-known"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".well-known", "id.txt"), []byte("id"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	v := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	if v.LandingFiles == nil || v.LandingFiles.Count != 5 {
		t.Fatalf("hidden files are not counted: %+v", v.LandingFiles)
	}
	e.mustApply(t, v.ID)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("the landing directory stays after the delete")
	}
	e.mustRevert(t, v.ID)
	for name, want := range map[string]string{".htaccess": "Options -Indexes\n", ".well-known/id.txt": "id", "index.html": "<html>spare</html>"} {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	if info, err := os.Stat(filepath.Join(root, "empty")); err != nil || !info.IsDir() {
		t.Fatal("the empty directory is lost")
	}
}

// TestAssistantChangesLandingLinkIsRefused checks a landing with a symbolic link is neither
// archived nor deleted, at the proposal and at the apply.
func TestAssistantChangesLandingLinkIsRefused(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newChgEnv(t)
	root := filepath.Join(e.dir, "landing-files", "l9")
	before := e.landingBytes(t, "l9")
	v := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	if err := os.Symlink(outside, filepath.Join(root, "link.html")); err != nil {
		t.Skip("symbolic links are not available")
	}
	_, err := e.propose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	wantChgErr(t, "proposal", err, http.StatusConflict, aiErrLandingTooLarge)
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	_, err = e.apply(v.ID, aiApplyOpts{ConfirmDelete: true})
	wantChgErr(t, "apply", err, http.StatusConflict, aiErrLandingTooLarge)
	if e.hashOf(t, "landings", "l9") == "" {
		t.Fatal("the landing record was deleted")
	}
	if _, err := os.Lstat(filepath.Join(root, "link.html")); err != nil {
		t.Fatal("the link was touched")
	}
	for name, data := range before {
		if got, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(name))); string(got) != string(data) {
			t.Fatalf("%s changed", name)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "outside" {
		t.Fatal("the file behind the link changed")
	}
}

// TestAssistantLandingReadDoesNotFollowLinks checks a link is never read as a file.
func TestAssistantLandingReadDoesNotFollowLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symbolic links are not available")
	}
	if _, err := aiLandingReadFile(link); !errors.Is(err, errAILandingUnsafe) {
		t.Fatalf("link read: %v", err)
	}
	if b, err := aiLandingReadFile(target); err != nil || string(b) != "data" {
		t.Fatalf("plain read: %q %v", b, err)
	}
	if _, err := aiLandingWalk(link); !errors.Is(err, errAILandingUnsafe) {
		t.Fatalf("a link as the landing directory: %v", err)
	}
}
