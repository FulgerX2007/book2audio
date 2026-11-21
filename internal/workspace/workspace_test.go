package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// writePDF creates a stand-in "PDF": the workspace only hashes the file.
func writePDF(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSlug(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "report", "report"},
		{"upper case and spaces", "My Annual Report", "my-annual-report"},
		{"punctuation runs collapse", "a -- b__c (2nd ed.)", "a-b-c-2nd-ed"},
		{"leading and trailing junk", "  ..hello!!  ", "hello"},
		{"cyrillic is kept", "Война и мир. Том 1", "война-и-мир-том-1"},
		{"romanian diacritics are kept", "Amintiri din copilărie", "amintiri-din-copilărie"},
		{"digits", "2026-11-21 notes", "2026-11-21-notes"},
		{"nothing usable", "!!! ---", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Slug(tt.in); got != tt.want {
				t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSlugLimitsLength(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("a", 200),
		strings.Repeat("ab ", 100),
		strings.Repeat("я", 63) + " хвост",
	} {
		got := Slug(in)
		if n := utf8.RuneCountInString(got); n > maxSlugRunes {
			t.Errorf("Slug of %d runes: got %d runes, want at most %d", utf8.RuneCountInString(in), n, maxSlugRunes)
		}
		if strings.HasSuffix(got, "-") {
			t.Errorf("Slug ends with a dash: %q", got)
		}
		if Slug(got) != got {
			t.Errorf("Slug is not stable: Slug(%q) = %q", got, Slug(got))
		}
	}
}

func TestInitCreatesLayout(t *testing.T) {
	tmp := t.TempDir()
	pdf := writePDF(t, tmp, "My Book.pdf", "pdf-content")
	workdir := filepath.Join(tmp, "work")

	ws, err := Init(pdf, InitOptions{Workdir: workdir})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if want := filepath.Join(workdir, "my-book"); ws.Dir != want {
		t.Errorf("Dir = %q, want %q", ws.Dir, want)
	}
	for _, sub := range []string{
		"01-pages", "02-chapters", "03-translated/cache", "04-audio/cache", "out",
	} {
		if info, err := os.Stat(filepath.Join(ws.Dir, sub)); err != nil || !info.IsDir() {
			t.Errorf("directory %s is missing: %v", sub, err)
		}
	}
	for _, file := range []string{"manifest.json", "config.yaml", "glossary.yaml"} {
		if info, err := os.Stat(filepath.Join(ws.Dir, file)); err != nil || info.Size() == 0 {
			t.Errorf("file %s is missing or empty: %v", file, err)
		}
	}
	if len(ws.Source.SHA256) != 64 {
		t.Errorf("Source.SHA256 = %q, want a sha256 hex digest", ws.Source.SHA256)
	}
	if !filepath.IsAbs(ws.Source.Path) || filepath.Base(ws.Source.Path) != "My Book.pdf" {
		t.Errorf("Source.Path = %q, want the absolute path of the PDF", ws.Source.Path)
	}
	if ws.Config.TargetLang != "ru" {
		t.Errorf("the default config was not written: TargetLang = %q", ws.Config.TargetLang)
	}
}

func TestInitCopiesConfigAndUsesItsWorkdir(t *testing.T) {
	tmp := t.TempDir()
	pdf := writePDF(t, tmp, "book.pdf", "pdf-content")
	workdir := filepath.Join(tmp, "books")
	cfgText := "# мой конфиг\nworkdir: " + workdir + "\nsource_lang: de\n"
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o644); err != nil {
		t.Fatal(err)
	}

	ws, err := Init(pdf, InitOptions{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if want := filepath.Join(workdir, "book"); ws.Dir != want {
		t.Errorf("Dir = %q, want %q", ws.Dir, want)
	}
	got, err := os.ReadFile(ws.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != cfgText {
		t.Errorf("config.yaml = %q, want the user's file verbatim %q", got, cfgText)
	}
	if ws.Config.SourceLang != "de" {
		t.Errorf("SourceLang = %q, want %q", ws.Config.SourceLang, "de")
	}
}

func TestInitTwiceKeepsExistingFiles(t *testing.T) {
	tmp := t.TempDir()
	pdf := writePDF(t, tmp, "book.pdf", "pdf-content")
	opts := InitOptions{Workdir: filepath.Join(tmp, "work")}

	ws, err := Init(pdf, opts)
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}
	edited := "terms:\n  - source: Mr. Darcy\n    target: мистер Дарси\n"
	if err := os.WriteFile(ws.GlossaryPath(), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	page := ws.PagePath(1)
	if err := os.WriteFile(page, []byte("page one"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Init(pdf, opts); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if got, _ := os.ReadFile(ws.GlossaryPath()); string(got) != edited {
		t.Errorf("the hand-edited glossary was overwritten: %q", got)
	}
	if got, _ := os.ReadFile(page); string(got) != "page one" {
		t.Errorf("stage output was lost: %q", got)
	}
}

func TestInitSlug(t *testing.T) {
	tmp := t.TempDir()
	workdir := filepath.Join(tmp, "work")
	first := writePDF(t, tmp, "book.pdf", "first book")
	other := writePDF(t, filepath.Join(tmp), "other.pdf", "second book")

	if _, err := Init(first, InitOptions{Workdir: workdir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	t.Run("collision with another PDF", func(t *testing.T) {
		_, err := Init(other, InitOptions{Workdir: workdir, Slug: "book"})
		if err == nil || !strings.Contains(err.Error(), "--slug") {
			t.Errorf("error = %v, want a collision error that suggests --slug", err)
		}
	})
	t.Run("explicit slug", func(t *testing.T) {
		ws, err := Init(other, InitOptions{Workdir: workdir, Slug: "second"})
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		if filepath.Base(ws.Dir) != "second" {
			t.Errorf("Dir = %q, want .../second", ws.Dir)
		}
	})
	t.Run("invalid explicit slug", func(t *testing.T) {
		_, err := Init(other, InitOptions{Workdir: workdir, Slug: "My Book"})
		if err == nil || !strings.Contains(err.Error(), "my-book") {
			t.Errorf("error = %v, want a rejection that suggests %q", err, "my-book")
		}
	})
	t.Run("file name without letters", func(t *testing.T) {
		odd := writePDF(t, tmp, "---.pdf", "odd")
		_, err := Init(odd, InitOptions{Workdir: workdir})
		if err == nil || !strings.Contains(err.Error(), "--slug") {
			t.Errorf("error = %v, want an error that suggests --slug", err)
		}
	})
}

func TestInitErrors(t *testing.T) {
	tmp := t.TempDir()
	pdf := writePDF(t, tmp, "book.pdf", "pdf-content")

	t.Run("missing PDF", func(t *testing.T) {
		_, err := Init(filepath.Join(tmp, "absent.pdf"), InitOptions{Workdir: tmp})
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error = %v, want os.ErrNotExist", err)
		}
	})
	t.Run("directory instead of a PDF", func(t *testing.T) {
		if _, err := Init(tmp, InitOptions{Workdir: tmp}); err == nil {
			t.Error("Init accepted a directory")
		}
	})
	t.Run("invalid config", func(t *testing.T) {
		cfgPath := filepath.Join(tmp, "bad.yaml")
		if err := os.WriteFile(cfgPath, []byte("audio:\n  format: ogg\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Init(pdf, InitOptions{ConfigPath: cfgPath, Workdir: filepath.Join(tmp, "w")})
		if err == nil || !strings.Contains(err.Error(), "audio.format") {
			t.Errorf("error = %v, want a config validation error", err)
		}
		if _, statErr := os.Stat(filepath.Join(tmp, "w")); !errors.Is(statErr, os.ErrNotExist) {
			t.Error("a working folder was created despite the invalid config")
		}
	})
}

func TestOpen(t *testing.T) {
	tmp := t.TempDir()
	workdir := filepath.Join(tmp, "work")
	pdf := writePDF(t, tmp, "book.pdf", "pdf-content")
	created, err := Init(pdf, InitOptions{Workdir: workdir})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	t.Run("by folder", func(t *testing.T) {
		ws, err := Open(created.Dir, OpenOptions{})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if ws.Dir != created.Dir || ws.Source != created.Source {
			t.Errorf("Open = %+v, want the workspace created by Init", ws)
		}
	})
	t.Run("by PDF", func(t *testing.T) {
		ws, err := Open(pdf, OpenOptions{Workdir: workdir})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if ws.Dir != created.Dir {
			t.Errorf("Dir = %q, want %q", ws.Dir, created.Dir)
		}
	})
	t.Run("by PDF created with an explicit slug", func(t *testing.T) {
		other := writePDF(t, tmp, "other.pdf", "another book")
		want, err := Init(other, InitOptions{Workdir: workdir, Slug: "renamed"})
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		ws, err := Open(other, OpenOptions{Workdir: workdir})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if ws.Dir != want.Dir {
			t.Errorf("Dir = %q, want %q", ws.Dir, want.Dir)
		}
	})
	t.Run("PDF content differs from the manifest", func(t *testing.T) {
		changedDir := filepath.Join(tmp, "changed")
		if err := os.Mkdir(changedDir, 0o755); err != nil {
			t.Fatal(err)
		}
		changed := writePDF(t, changedDir, "book.pdf", "a different file with the same name")
		_, err := Open(changed, OpenOptions{Workdir: workdir})
		if err == nil || !strings.Contains(err.Error(), "differs from") {
			t.Errorf("error = %v, want a sha256 mismatch error", err)
		}
	})
	t.Run("PDF that was never initialised", func(t *testing.T) {
		fresh := writePDF(t, tmp, "fresh.pdf", "never seen")
		if _, err := Open(fresh, OpenOptions{Workdir: workdir}); !errors.Is(err, ErrNotInitialized) {
			t.Errorf("error = %v, want ErrNotInitialized", err)
		}
	})
	t.Run("folder that is not a working folder", func(t *testing.T) {
		if _, err := Open(tmp, OpenOptions{}); !errors.Is(err, ErrNotInitialized) {
			t.Errorf("error = %v, want ErrNotInitialized", err)
		}
	})
	t.Run("missing argument path", func(t *testing.T) {
		if _, err := Open(filepath.Join(tmp, "nope"), OpenOptions{}); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error = %v, want os.ErrNotExist", err)
		}
	})
}

func TestOpenConfigSelection(t *testing.T) {
	tmp := t.TempDir()
	pdf := writePDF(t, tmp, "book.pdf", "pdf-content")
	created, err := Init(pdf, InitOptions{Workdir: filepath.Join(tmp, "work")})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	t.Run("the book's config.yaml is the main config", func(t *testing.T) {
		if err := os.WriteFile(created.ConfigPath(), []byte("target_lang: uk\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ws, err := Open(created.Dir, OpenOptions{})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if ws.Config.TargetLang != "uk" {
			t.Errorf("TargetLang = %q, want %q", ws.Config.TargetLang, "uk")
		}
	})
	t.Run("--config replaces it for one run", func(t *testing.T) {
		override := filepath.Join(tmp, "override.yaml")
		if err := os.WriteFile(override, []byte("target_lang: ro\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ws, err := Open(created.Dir, OpenOptions{ConfigPath: override})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if ws.Config.TargetLang != "ro" {
			t.Errorf("TargetLang = %q, want %q", ws.Config.TargetLang, "ro")
		}
		if got, _ := os.ReadFile(created.ConfigPath()); string(got) != "target_lang: uk\n" {
			t.Errorf("the book's config.yaml was changed: %q", got)
		}
	})
	t.Run("invalid book config", func(t *testing.T) {
		if err := os.WriteFile(created.ConfigPath(), []byte("workdir: ''\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(created.Dir, OpenOptions{}); err == nil || !strings.Contains(err.Error(), "workdir") {
			t.Errorf("error = %v, want a config validation error", err)
		}
	})
}

func TestOpenRejectsBadManifest(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{"corrupted", "{not json", "is damaged"},
		{"unknown version", `{"version": 99, "source": {"sha256": "x"}}`, "manifest version 99"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(tt.manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Open(dir, OpenOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestLockIsExclusive(t *testing.T) {
	dir := t.TempDir()

	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	if _, err := AcquireLock(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireLock error = %v, want ErrLocked", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Errorf("second Release: %v", err)
	}
	again, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock after Release: %v", err)
	}
	again.Release()
}

func TestWorkspaceLock(t *testing.T) {
	tmp := t.TempDir()
	pdf := writePDF(t, tmp, "book.pdf", "pdf-content")
	ws, err := Init(pdf, InitOptions{Workdir: filepath.Join(tmp, "work")})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	other, err := Open(ws.Dir, OpenOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := ws.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := ws.Lock(); err != nil {
		t.Errorf("repeated Lock on the same workspace: %v", err)
	}
	if err := other.Lock(); !errors.Is(err, ErrLocked) {
		t.Errorf("Lock from a second workspace = %v, want ErrLocked", err)
	}
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := other.Lock(); err != nil {
		t.Errorf("Lock after Close: %v", err)
	}
	other.Close()
}

func TestPaths(t *testing.T) {
	ws := &Workspace{Dir: "/w/book"}
	hash := strings.Repeat("ab", 32)
	tests := []struct{ got, want string }{
		{ws.ManifestPath(), "/w/book/manifest.json"},
		{ws.ConfigPath(), "/w/book/config.yaml"},
		{ws.GlossaryPath(), "/w/book/glossary.yaml"},
		{ws.OutDir(), "/w/book/out"},
		{ws.PagePath(7), "/w/book/01-pages/p0007.txt"},
		{ws.ChaptersJSONPath(), "/w/book/02-chapters/chapters.json"},
		{ws.ChapterPath(0), "/w/book/02-chapters/ch00.md"},
		{ws.ChapterPath(12), "/w/book/02-chapters/ch12.md"},
		{ws.ChunkPath(hash), "/w/book/03-translated/cache/" + hash + ".ru.txt"},
		{ws.ChunksJSONPath(3), "/w/book/03-translated/ch03.chunks.json"},
		{ws.TranslatedChapterPath(3), "/w/book/03-translated/ch03.ru.md"},
		{ws.SegmentPath(hash), "/w/book/04-audio/cache/" + hash + ".wav"},
		{ws.SegmentsJSONPath(3), "/w/book/04-audio/ch03.segments.json"},
		{ws.ChapterWAVPath(3), "/w/book/04-audio/ch03.wav"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("path = %q, want %q", tt.got, tt.want)
		}
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")

	if err := WriteFileAtomic(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("second"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic (overwrite): %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "second" {
		t.Errorf("content = %q, want %q", got, "second")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("permissions = %v, want 0644", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary files were left behind: %v", entries)
	}

	if err := WriteFileAtomic(filepath.Join(dir, "absent", "file.txt"), []byte("x"), 0o644); err == nil {
		t.Error("WriteFileAtomic into a missing directory succeeded")
	}
}
