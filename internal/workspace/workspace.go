// Package workspace manages the on-disk working folder of one book: its
// layout, config, manifest and lock.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/FulgerX2007/book2audio/internal/config"
)

// ManifestVersion is the format version written to manifest.json.
const ManifestVersion = 1

// Files and directories inside a working folder.
const (
	manifestName = "manifest.json"
	configName   = "config.yaml"
	glossaryName = "glossary.yaml"

	pagesDir      = "01-pages"
	chaptersDir   = "02-chapters"
	translatedDir = "03-translated"
	audioDir      = "04-audio"
	outDir        = "out"
	cacheDir      = "cache"
)

// ErrNotInitialized is returned by Open when the book has no working folder.
var ErrNotInitialized = errors.New("the working folder of the book is not found, run init first")

const glossaryTemplate = `# Glossary: how to translate names and terms.
# Editing a term re-translates only the chunks that mention it.
#
# terms:
#   - source: Mr. Darcy
#     target: мистер Дарси
terms: []
`

// Source describes the PDF a working folder was created for.
type Source struct {
	Path   string `json:"path"` // absolute path at init time
	SHA256 string `json:"sha256"`
	Pages  int    `json:"pages,omitempty"`
	Title  string `json:"title,omitempty"`
	Author string `json:"author,omitempty"`
}

// manifestHeader is the part of manifest.json this file needs. The stage
// state is added by the manifest type.
type manifestHeader struct {
	Version int    `json:"version"`
	Source  Source `json:"source"`
}

// Workspace is an opened working folder.
type Workspace struct {
	Dir    string        // work/<slug>, absolute
	Config config.Config // config of this run
	Source Source        // the PDF as recorded at init time
	PDF    string        // where the PDF is now: the argument of Open or Source.Path

	// Manifest is the state of the stages. Change it only while holding
	// the lock taken by Lock.
	Manifest *Manifest

	lock *Lock
}

// InitOptions are the parameters of Init.
type InitOptions struct {
	ConfigPath string // config to copy into the folder; empty: defaults
	Workdir    string // parent of the working folder; empty: from config
	Slug       string // folder name; empty: derived from the PDF file name
}

// OpenOptions are the parameters of Open.
type OpenOptions struct {
	ConfigPath string // replaces the book's config.yaml for this run
	Workdir    string // where to look for the folder of a PDF; empty: from config
}

// Init creates the working folder for pdfPath. Calling it again for the same
// PDF is harmless: existing files, including hand-edited ones, are kept.
func Init(pdfPath string, opts InitOptions) (*Workspace, error) {
	sum, absPDF, err := hashPDF(pdfPath)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}

	slug := opts.Slug
	if slug == "" {
		slug = Slug(strings.TrimSuffix(filepath.Base(pdfPath), filepath.Ext(pdfPath)))
		if slug == "" {
			return nil, fmt.Errorf("cannot derive a working folder name from the file name %q, set it with --slug", filepath.Base(pdfPath))
		}
	} else if Slug(slug) != slug {
		return nil, fmt.Errorf("invalid --slug %q, %q would do", slug, Slug(slug))
	}

	workdir := opts.Workdir
	if workdir == "" {
		workdir = cfg.Workdir
	}
	dir := filepath.Join(workdir, slug)

	if hdr, err := readHeader(dir); err == nil {
		if hdr.Source.SHA256 != sum {
			return nil, fmt.Errorf("folder %s belongs to another book (%s), choose another name with --slug", dir, hdr.Source.Path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	for _, sub := range []string{
		pagesDir,
		chaptersDir,
		filepath.Join(translatedDir, cacheDir),
		filepath.Join(audioDir, cacheDir),
		outDir,
	} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("creating the working folder: %w", err)
		}
	}

	lock, err := AcquireLock(dir)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	cfgData, err := configBytes(opts.ConfigPath, cfg)
	if err != nil {
		return nil, err
	}
	hdrData, err := json.MarshalIndent(manifestHeader{
		Version: ManifestVersion,
		Source:  Source{Path: absPDF, SHA256: sum},
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	for _, file := range []struct {
		name string
		data []byte
	}{
		{configName, cfgData},
		{glossaryName, []byte(glossaryTemplate)},
		{manifestName, append(hdrData, '\n')},
	} {
		if err := writeIfAbsent(filepath.Join(dir, file.name), file.data); err != nil {
			return nil, err
		}
	}

	return Open(dir, OpenOptions{})
}

// Open opens the working folder named by arg: either the folder itself or
// the PDF it was created for. For a PDF the file must be the one recorded in
// the manifest.
func Open(arg string, opts OpenOptions) (*Workspace, error) {
	info, err := os.Stat(arg)
	if err != nil {
		return nil, fmt.Errorf("book: %w", err)
	}

	var dir string
	var hdr manifestHeader
	if info.IsDir() {
		dir = arg
		if hdr, err = readHeader(dir); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%w (%s is not a working folder)", ErrNotInitialized, dir)
			}
			return nil, err
		}
	} else {
		if dir, hdr, err = findByPDF(arg, opts); err != nil {
			return nil, err
		}
	}
	if hdr.Version != ManifestVersion {
		return nil, fmt.Errorf("%s: manifest version %d is not supported (need %d)",
			filepath.Join(dir, manifestName), hdr.Version, ManifestVersion)
	}
	// Every path handed out by the workspace is absolute, so it means the
	// same file for the manifest, for external tools and after a chdir.
	if dir, err = filepath.Abs(dir); err != nil {
		return nil, fmt.Errorf("book: %w", err)
	}

	cfgPath := opts.ConfigPath
	if cfgPath == "" {
		cfgPath = filepath.Join(dir, configName)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	// The translation and speech profiles of the books directory, if the
	// book uses them.
	if err := config.ApplyProfiles(&cfg, filepath.Dir(dir)); err != nil {
		return nil, err
	}
	manifest, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	pdf := hdr.Source.Path
	if !info.IsDir() {
		// The book was opened by its PDF: use that file even if it has
		// been moved since init (its content was checked against the manifest).
		if pdf, err = filepath.Abs(arg); err != nil {
			return nil, fmt.Errorf("book: %w", err)
		}
	}
	return &Workspace{Dir: dir, Config: cfg, Source: hdr.Source, PDF: pdf, Manifest: manifest}, nil
}

// findByPDF locates the working folder created for the PDF at pdfPath.
func findByPDF(pdfPath string, opts OpenOptions) (string, manifestHeader, error) {
	sum, _, err := hashPDF(pdfPath)
	if err != nil {
		return "", manifestHeader{}, err
	}
	workdir := opts.Workdir
	if workdir == "" {
		cfg, err := config.Load(opts.ConfigPath)
		if err != nil {
			return "", manifestHeader{}, err
		}
		workdir = cfg.Workdir
	}

	// The usual case: the folder is named after the file.
	slug := Slug(strings.TrimSuffix(filepath.Base(pdfPath), filepath.Ext(pdfPath)))
	if slug != "" {
		dir := filepath.Join(workdir, slug)
		hdr, err := readHeader(dir)
		switch {
		case err == nil && hdr.Source.SHA256 == sum:
			return dir, hdr, nil
		case err == nil:
			return "", manifestHeader{}, fmt.Errorf(
				"%s was created for another file (%s): the content of %s differs from the one recorded in the manifest",
				dir, hdr.Source.Path, pdfPath)
		case !errors.Is(err, fs.ErrNotExist):
			return "", manifestHeader{}, err
		}
	}

	// The folder may have been created with --slug: look for the same file.
	entries, err := os.ReadDir(workdir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", manifestHeader{}, fmt.Errorf("book: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(workdir, e.Name())
		if hdr, err := readHeader(dir); err == nil && hdr.Source.SHA256 == sum {
			return dir, hdr, nil
		}
	}
	return "", manifestHeader{}, fmt.Errorf("%w (%s)", ErrNotInitialized, pdfPath)
}

// Lock takes the exclusive lock of the folder for the lifetime of a command.
func (w *Workspace) Lock() error {
	if w.lock != nil {
		return nil
	}
	lock, err := AcquireLock(w.Dir)
	if err != nil {
		return err
	}
	w.lock = lock
	return nil
}

// Close releases the lock taken by Lock.
func (w *Workspace) Close() error {
	err := w.lock.Release()
	w.lock = nil
	return err
}

// Paths of the files inside the working folder.

func (w *Workspace) ManifestPath() string { return filepath.Join(w.Dir, manifestName) }
func (w *Workspace) ConfigPath() string   { return filepath.Join(w.Dir, configName) }
func (w *Workspace) GlossaryPath() string { return filepath.Join(w.Dir, glossaryName) }
func (w *Workspace) OutDir() string       { return filepath.Join(w.Dir, outDir) }

// PagePath is the raw text of page n (1-based).
func (w *Workspace) PagePath(n int) string {
	return filepath.Join(w.Dir, pagesDir, fmt.Sprintf("p%04d.txt", n))
}

// ChaptersJSONPath is the hand-editable list of chapter starts.
func (w *Workspace) ChaptersJSONPath() string {
	return filepath.Join(w.Dir, chaptersDir, "chapters.json")
}

// ChapterPath is the cleaned source text of chapter n.
func (w *Workspace) ChapterPath(n int) string {
	return filepath.Join(w.Dir, chaptersDir, chapterID(n)+".md")
}

// ChunkPath is the cached translation of the chunk with the given input hash.
func (w *Workspace) ChunkPath(hash string) string {
	return filepath.Join(w.Dir, translatedDir, cacheDir, hash+".ru.txt")
}

// ChunksJSONPath lists the chunk hashes of chapter n in order.
func (w *Workspace) ChunksJSONPath(n int) string {
	return filepath.Join(w.Dir, translatedDir, chapterID(n)+".chunks.json")
}

// TranslatedChapterPath is the assembled, hand-editable translation of chapter n.
func (w *Workspace) TranslatedChapterPath(n int) string {
	return filepath.Join(w.Dir, translatedDir, chapterID(n)+".ru.md")
}

// SegmentPath is the cached audio of the segment with the given input hash.
func (w *Workspace) SegmentPath(hash string) string {
	return filepath.Join(w.Dir, audioDir, cacheDir, hash+".wav")
}

// SegmentsJSONPath lists the segment hashes of chapter n in order.
func (w *Workspace) SegmentsJSONPath(n int) string {
	return filepath.Join(w.Dir, audioDir, chapterID(n)+".segments.json")
}

// ChapterWAVPath is the concatenated audio of chapter n.
func (w *Workspace) ChapterWAVPath(n int) string {
	return filepath.Join(w.Dir, audioDir, chapterID(n)+".wav")
}

func chapterID(n int) string { return fmt.Sprintf("ch%02d", n) }

// WriteFileAtomic writes data to path so that a crash leaves either the old
// or the new content, never a partial file: temporary file, fsync, rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Persist the rename itself; not every filesystem supports syncing a
	// directory, so a failure here is not an error.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func writeIfAbsent(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return WriteFileAtomic(path, data, 0o644)
}

// configBytes returns the content of the book's config.yaml: the user's file
// as written, or the defaults.
func configBytes(path string, cfg config.Config) ([]byte, error) {
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		return data, nil
	}
	return yaml.Marshal(cfg)
}

func readHeader(dir string) (manifestHeader, error) {
	path := filepath.Join(dir, manifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return manifestHeader{}, err
	}
	var hdr manifestHeader
	if err := json.Unmarshal(data, &hdr); err != nil {
		return manifestHeader{}, fmt.Errorf("%s is damaged: %w", path, err)
	}
	return hdr, nil
}

// hashPDF returns the sha256 of the file and its absolute path.
func hashPDF(path string) (sum, abs string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("book: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return "", "", fmt.Errorf("book: %w", err)
	} else if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("book: %s is not a file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", "", fmt.Errorf("book: %w", err)
	}
	if abs, err = filepath.Abs(path); err != nil {
		return "", "", fmt.Errorf("book: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), abs, nil
}
