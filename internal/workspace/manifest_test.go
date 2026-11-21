package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// newBook initialises a working folder and returns it opened.
func newBook(t *testing.T) *Workspace {
	t.Helper()
	tmp := t.TempDir()
	pdf := writePDF(t, tmp, "book.pdf", "pdf-content")
	ws, err := Init(pdf, InitOptions{Workdir: filepath.Join(tmp, "work")})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return ws
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func needsWork(t *testing.T, m *Manifest, stage, unit, in string) bool {
	t.Helper()
	need, err := m.NeedsWork(stage, unit, in)
	if err != nil {
		t.Fatalf("NeedsWork: %v", err)
	}
	return need
}

func TestHashOf(t *testing.T) {
	if HashStrings("ab", "c") == HashStrings("a", "bc") {
		t.Error("parts are not delimited: (ab, c) and (a, bc) hash equally")
	}
	if HashStrings("a", "b") != HashOf([]byte("a"), []byte("b")) {
		t.Error("HashStrings and HashOf disagree")
	}
	if HashStrings("x") != HashStrings("x") || len(HashStrings("x")) != 64 {
		t.Error("the hash is not a stable sha256 hex digest")
	}
	if HashStrings() == HashStrings("") {
		t.Error("no parts and one empty part hash equally")
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeFile(t, a, "same")
	writeFile(t, b, "same")

	ha, err := HashFile(a)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	if hb, _ := HashFile(b); ha != hb {
		t.Error("equal files hash differently")
	}
	writeFile(t, b, "other")
	if hb, _ := HashFile(b); ha == hb {
		t.Error("different files hash equally")
	}
	if _, err := HashFile(filepath.Join(dir, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v, want os.ErrNotExist", err)
	}
}

func TestNeedsWork(t *testing.T) {
	ws := newBook(t)
	m := ws.Manifest
	out := ws.PagePath(1)

	if !needsWork(t, m, "extract", "p0001", "in-1") {
		t.Error("an unknown unit does not need work")
	}

	writeFile(t, out, "page text")
	if err := m.MarkDone("extract", "p0001", Unit{In: "in-1"}, out); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if needsWork(t, m, "extract", "p0001", "in-1") {
		t.Error("a done unit with the same input needs work")
	}
	if !needsWork(t, m, "extract", "p0001", "in-2") {
		t.Error("a changed input hash does not trigger work")
	}
	if !needsWork(t, m, "chapters", "p0001", "in-1") {
		t.Error("units of different stages are mixed up")
	}

	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	if !needsWork(t, m, "extract", "p0001", "in-1") {
		t.Error("a missing result file does not trigger work")
	}
}

func TestManifestSurvivesReload(t *testing.T) {
	ws := newBook(t)
	out := ws.PagePath(7)
	writeFile(t, out, "scanned page")
	if err := ws.Manifest.MarkDone("extract", "p0007", Unit{In: "in", OCR: true}, out); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if err := ws.Manifest.SetSource(Source{Path: "/x/book.pdf", SHA256: "abc", Pages: 300, Title: "T", Author: "A"}); err != nil {
		t.Fatalf("SetSource: %v", err)
	}

	m, err := LoadManifest(ws.Dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	u, ok := m.Unit("extract", "p0007")
	if !ok {
		t.Fatal("the unit was not persisted")
	}
	want := Unit{In: "in", Out: "01-pages/p0007.txt", OutSHA256: u.OutSHA256, Status: StatusDone, OCR: true}
	if u != want || len(u.OutSHA256) != 64 {
		t.Errorf("unit = %+v, want %+v with a sha256", u, want)
	}
	if src := m.GetSource(); src.Pages != 300 || src.Title != "T" || src.Author != "A" {
		t.Errorf("source = %+v, want the values set by SetSource", src)
	}
	if needsWork(t, m, "extract", "p0007", "in") {
		t.Error("a done unit needs work after a reload")
	}
}

func TestMarkFailedAndManualResult(t *testing.T) {
	ws := newBook(t)
	m := ws.Manifest
	out := ws.ChunkPath("hash-1")

	if err := m.MarkFailed("translate", "hash-1", Unit{In: "hash-1", Chapter: "ch01"}, out, errors.New("сервер недоступен")); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	u, _ := m.Unit("translate", "hash-1")
	if u.Status != StatusFailed || u.Error != "сервер недоступен" || u.Out != "03-translated/cache/hash-1.ru.txt" || u.Chapter != "ch01" {
		t.Errorf("failed unit = %+v", u)
	}
	if !needsWork(t, m, "translate", "hash-1", "hash-1") {
		t.Error("a failed unit without a result does not need work")
	}
	if c := m.Counts("translate"); c != (Counts{Total: 1, Failed: 1}) {
		t.Errorf("Counts = %+v, want one failed unit", c)
	}

	// The user supplies the result by hand at the path shown by status.
	writeFile(t, out, "перевод, сделанный вручную")
	if needsWork(t, m, "translate", "hash-1", "hash-1") {
		t.Error("a failed unit with a hand-made result still needs work")
	}
	u, _ = m.Unit("translate", "hash-1")
	if u.Status != StatusDone || u.Error != "" || u.OutSHA256 == "" {
		t.Errorf("unit after the manual result = %+v, want done", u)
	}
	if edited, _ := m.Edited("translate", "hash-1"); edited {
		t.Error("the accepted manual result is reported as edited")
	}

	// A later success replaces the failure record as well.
	if err := m.MarkFailed("translate", "hash-2", Unit{In: "hash-2"}, ws.ChunkPath("hash-2"), errors.New("x")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ws.ChunkPath("hash-2"), "перевод")
	if err := m.MarkDone("translate", "hash-2", Unit{In: "hash-2"}, ws.ChunkPath("hash-2")); err != nil {
		t.Fatal(err)
	}
	if u, _ := m.Unit("translate", "hash-2"); u.Status != StatusDone || u.Error != "" {
		t.Errorf("unit after MarkDone = %+v, want done without an error", u)
	}
}

func TestEditedAndProtected(t *testing.T) {
	ws := newBook(t)
	m := ws.Manifest
	out := ws.ChapterPath(1)

	if edited, err := m.Edited("chapters", "ch01"); err != nil || edited {
		t.Errorf("unknown unit: Edited = %v, %v", edited, err)
	}

	writeFile(t, out, "# Chapter 1\n\nText.\n")
	if err := m.MarkDone("chapters", "ch01", Unit{In: "in"}, out); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if edited, err := m.Edited("chapters", "ch01"); err != nil || edited {
		t.Errorf("untouched file: Edited = %v, %v", edited, err)
	}

	writeFile(t, out, "# Chapter 1\n\nText fixed by hand.\n")
	if edited, err := m.Edited("chapters", "ch01"); err != nil || !edited {
		t.Errorf("hand-edited file: Edited = %v, %v", edited, err)
	}
	if protected, err := m.Protected("chapters", "ch01"); err != nil || !protected {
		t.Errorf("hand-edited file: Protected = %v, %v", protected, err)
	}

	m.Force = true
	if protected, err := m.Protected("chapters", "ch01"); err != nil || protected {
		t.Errorf("with Force: Protected = %v, %v", protected, err)
	}
	if edited, _ := m.Edited("chapters", "ch01"); !edited {
		t.Error("Force changed what Edited reports")
	}
	m.Force = false

	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	if edited, err := m.Edited("chapters", "ch01"); err != nil || edited {
		t.Errorf("deleted file: Edited = %v, %v", edited, err)
	}
}

func TestPrune(t *testing.T) {
	ws := newBook(t)
	m := ws.Manifest
	for n := 1; n <= 3; n++ {
		writeFile(t, ws.PagePath(n), "text")
		if err := m.MarkDone("extract", fmt.Sprintf("p%04d", n), Unit{In: "in"}, ws.PagePath(n)); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.Prune("extract", []string{"p0001", "p0003", "p0009"}); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got := m.UnitIDs("extract"); !slices.Equal(got, []string{"p0001", "p0003"}) {
		t.Errorf("units after Prune = %v, want [p0001 p0003]", got)
	}
	if _, err := os.Stat(ws.PagePath(2)); err != nil {
		t.Errorf("Prune removed the result file: %v", err)
	}
	if err := m.Prune("no-such-stage", nil); err != nil {
		t.Errorf("Prune of an unknown stage: %v", err)
	}

	reloaded, err := LoadManifest(ws.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if c := reloaded.Counts("extract"); c != (Counts{Total: 2, Done: 2}) {
		t.Errorf("Counts after reload = %+v, want 2 done", c)
	}
}

func TestMarkErrors(t *testing.T) {
	ws := newBook(t)
	m := ws.Manifest

	t.Run("result file is missing", func(t *testing.T) {
		err := m.MarkDone("extract", "p0001", Unit{In: "in"}, ws.PagePath(1))
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error = %v, want os.ErrNotExist", err)
		}
		if _, ok := m.Unit("extract", "p0001"); ok {
			t.Error("a unit without a result was recorded as done")
		}
	})
	t.Run("result outside the working folder", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "x.txt")
		writeFile(t, outside, "x")
		if err := m.MarkDone("extract", "p0001", Unit{In: "in"}, outside); err == nil || !strings.Contains(err.Error(), "вне рабочей папки") {
			t.Errorf("MarkDone error = %v", err)
		}
		if err := m.MarkFailed("extract", "p0001", Unit{In: "in"}, outside, errors.New("x")); err == nil {
			t.Error("MarkFailed accepted a path outside the working folder")
		}
	})
	t.Run("manifest cannot be written", func(t *testing.T) {
		broken := &Manifest{Version: ManifestVersion, dir: filepath.Join(t.TempDir(), "absent")}
		if err := broken.SetSource(Source{}); err == nil || !strings.Contains(err.Error(), "запись манифеста") {
			t.Errorf("error = %v, want a manifest write error", err)
		}
	})
}

func TestLoadManifestErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"corrupted", "{not json", "повреждён"},
		{"unknown version", `{"version": 99}`, "версия манифеста 99"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, manifestName), tt.content)
			if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		if _, err := LoadManifest(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error = %v, want os.ErrNotExist", err)
		}
	})
}

func TestConcurrentMarks(t *testing.T) {
	ws := newBook(t)
	m := ws.Manifest
	const n = 40

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Go(func() {
			hash := fmt.Sprintf("hash-%02d", i)
			out := ws.ChunkPath(hash)
			if err := os.WriteFile(out, []byte(hash), 0o644); err != nil {
				errs <- err
				return
			}
			if i%4 == 0 {
				errs <- m.MarkFailed("translate", hash, Unit{In: hash}, out, errors.New("сбой"))
				return
			}
			errs <- m.MarkDone("translate", hash, Unit{In: hash}, out)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	reloaded, err := LoadManifest(ws.Dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if c := reloaded.Counts("translate"); c != (Counts{Total: n, Done: n - n/4, Failed: n / 4}) {
		t.Errorf("Counts = %+v, want %d units, %d failed", c, n, n/4)
	}
}

// TestRelativeWorkdir is a regression test: with the default, relative
// workdir ("work") the result paths were resolved against the working
// folder twice.
func TestRelativeWorkdir(t *testing.T) {
	tmp := t.TempDir()
	pdf := writePDF(t, tmp, "book.pdf", "pdf-content")
	t.Chdir(tmp)

	ws, err := Init("book.pdf", InitOptions{Workdir: "work"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !filepath.IsAbs(ws.Dir) || !filepath.IsAbs(ws.PagePath(1)) {
		t.Errorf("Dir = %q, PagePath = %q; want absolute paths", ws.Dir, ws.PagePath(1))
	}
	writeFile(t, ws.PagePath(1), "page text")
	if err := ws.Manifest.MarkDone("extract", "p0001", Unit{In: "in"}, ws.PagePath(1)); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
	if u, _ := ws.Manifest.Unit("extract", "p0001"); u.Out != "01-pages/p0001.txt" {
		t.Errorf("Out = %q, want the path relative to the working folder", u.Out)
	}

	// Both relative forms of the argument open the same folder.
	for _, arg := range []string{"book.pdf", filepath.Join("work", "book"), pdf} {
		got, err := Open(arg, OpenOptions{Workdir: "work"})
		if err != nil {
			t.Fatalf("Open(%q): %v", arg, err)
		}
		if got.Dir != ws.Dir {
			t.Errorf("Open(%q).Dir = %q, want %q", arg, got.Dir, ws.Dir)
		}
		if need, _ := got.Manifest.NeedsWork("extract", "p0001", "in"); need {
			t.Errorf("Open(%q): the recorded unit is not found", arg)
		}
	}
}
