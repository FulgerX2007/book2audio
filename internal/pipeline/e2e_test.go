package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/chapters"
	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/testdata"
	"github.com/FulgerX2007/book2audio/internal/translate"
	"github.com/FulgerX2007/book2audio/internal/tts"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// The end-to-end tests run the whole pipeline on the sample PDF with the
// real poppler tools and the real ffmpeg; only the translator and the
// speech engine are fakes.

// e2e is a book made from the sample PDF and the backends it runs with.
type e2e struct {
	ws  *workspace.Workspace
	tr  *translate.FakeTranslator
	syn *tts.FakeSynthesizer
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	for _, tool := range []string{"pdfinfo", "pdftotext", "ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "config.yaml")
	// Small chunks, so that a chapter of the sample has several of them.
	writeFile(t, cfg, "translate:\n  model: test-model\n  chunk_chars: 300\ntts:\n  args: [\"--output_file\", \"{out}\"]\n")
	ws, err := workspace.Init(testdata.Write(t, testdata.SamplePDF), workspace.InitOptions{
		ConfigPath: cfg, Workdir: filepath.Join(tmp, "work"),
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := ws.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	return &e2e{ws: ws, tr: &translate.FakeTranslator{}, syn: &tts.FakeSynthesizer{}}
}

func (e *e2e) deps() Deps {
	return Deps{
		Runner:         execx.OSRunner{},
		NewTranslator:  func(config.Translate) (translate.Translator, error) { return e.tr, nil },
		NewSynthesizer: func(config.TTS, execx.Runner) (tts.Synthesizer, error) { return e.syn, nil },
	}
}

func (e *e2e) run(t *testing.T) {
	t.Helper()
	if err := Run(context.Background(), e.ws, e.deps(), quiet, &bytes.Buffer{}, RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func (e *e2e) book() string {
	return filepath.Join(e.ws.OutDir(), filepath.Base(e.ws.Dir)+".m4b")
}

// chapterMarks returns the chapter titles of the audiobook, read by ffprobe.
func chapterMarks(t *testing.T, path string) []string {
	t.Helper()
	out, err := execx.OSRunner{}.Run(context.Background(), execx.Cmd{
		Name: "ffprobe", Args: []string{"-v", "error", "-show_chapters", "-of", "compact", path},
	})
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var titles []string
	for line := range strings.Lines(string(out)) {
		if _, title, ok := strings.Cut(strings.TrimSpace(line), "tag:title="); ok {
			titles = append(titles, title)
		}
	}
	return titles
}

func TestE2EFullRun(t *testing.T) {
	e := newE2E(t)
	e.run(t)

	// The table of contents page gives no chapters; the introduction is chapter 0.
	starts, err := chapters.Load(e.ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) != 5 || starts[0].N != 0 || starts[1].Title != "Chapter 1: The Lamp Room" || starts[1].StartPage != 2 {
		t.Fatalf("chapters = %+v, want the introduction and four chapters starting on page 2", starts)
	}

	// Every chapter has its Russian text.
	for _, s := range starts {
		text := readFile(t, e.ws.TranslatedChapterPath(s.N))
		if !strings.HasPrefix(text, "# "+translate.FakePrefix) || strings.Contains(text, "Contents") {
			t.Errorf("translated chapter %d:\n%s", s.N, text)
		}
	}

	// The audiobook has one mark per chapter, with the translated titles.
	marks := chapterMarks(t, e.book())
	want := []string{
		translate.FakePrefix + chapters.IntroTitle,
		translate.FakePrefix + "Chapter 1: The Lamp Room",
		translate.FakePrefix + "Chapter 2: Keeping the Log",
		translate.FakePrefix + "Chapter 3: Fog Signals",
		translate.FakePrefix + "Chapter 4: The Relief Boat",
	}
	if strings.Join(marks, "|") != strings.Join(want, "|") {
		t.Errorf("chapter marks = %q, want %q", marks, want)
	}

	st, err := ReadStatus(e.ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st.Stages {
		if s.Counts.Total == 0 || s.Counts.Done != s.Counts.Total {
			t.Errorf("stage %s = %+v, want everything done", s.Name, s.Counts)
		}
	}
}

func TestE2EResume(t *testing.T) {
	e := newE2E(t)
	// The first run is interrupted in the middle of the translate stage.
	ctx, cancel := context.WithCancel(context.Background())
	e.tr.Fail = func(translate.Request) error {
		if e.tr.Calls() >= 4 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	if err := Run(ctx, e.ws, e.deps(), quiet, &bytes.Buffer{}, RunOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted Run: error = %v, want context.Canceled", err)
	}
	done := e.ws.Manifest.Counts(StageTranslate).Done
	if done == 0 {
		t.Fatal("nothing was translated before the interruption")
	}

	e.tr.Fail = nil
	before := e.tr.Calls()
	e.run(t)

	// Chunks finished by the first run are not requested again.
	second := requestTexts(e.tr, before)
	total := e.ws.Manifest.Counts(StageTranslate).Total
	if len(second) != total-done {
		t.Errorf("the resumed run translated %d chunks, want %d of %d", len(second), total-done, total)
	}
	if _, err := os.Stat(e.book()); err != nil {
		t.Errorf("the audiobook was not built after the resume: %v", err)
	}
}

func TestE2EEditedChapterText(t *testing.T) {
	e := newE2E(t)
	e.run(t)

	// Insert a paragraph into chapter 1 after its first paragraph.
	path := e.ws.ChapterPath(1)
	paragraphs := strings.Split(strings.TrimSpace(readFile(t, path)), "\n\n")
	inserted := "A paragraph inserted by hand tells how the keeper counts the steps on the way up."
	edited := append(append(append([]string{}, paragraphs[:2]...), inserted), paragraphs[2:]...)
	writeFile(t, path, strings.Join(edited, "\n\n")+"\n")

	before := e.tr.Calls()
	e.run(t)
	got := requestTexts(e.tr, before)
	if len(got) == 0 {
		t.Fatal("the edit caused no translation")
	}
	for _, text := range got {
		if strings.Contains(text, "# ") {
			t.Errorf("the chunk with the chapter title, above the edit, was translated again: %q", text)
		}
		for _, other := range []string{"Every keeper writes", "Fog arrives", "relief boat", "This guide was written"} {
			if strings.Contains(text, other) {
				t.Errorf("a chunk of another chapter was translated again: %q", text)
			}
		}
	}
	if !strings.Contains(strings.Join(got, "\n"), inserted) {
		t.Errorf("the inserted paragraph was not translated: %q", got)
	}
	if !strings.Contains(readFile(t, e.ws.TranslatedChapterPath(1)), inserted) {
		t.Error("the translated chapter lacks the inserted paragraph")
	}
	// The edited source chapter is not rebuilt from the pages.
	if !strings.Contains(readFile(t, path), inserted) {
		t.Error("the hand-edited chapter file was overwritten")
	}
}

func TestE2EEditedTranslation(t *testing.T) {
	e := newE2E(t)
	e.run(t)

	path := e.ws.TranslatedChapterPath(2)
	original := readFile(t, path)
	edited := strings.Replace(original, "A good log is boring.", "A good log is boring, as the editor insists.", 1)
	if edited == original {
		t.Fatal("the test edit did not change the file")
	}
	writeFile(t, path, edited)

	trBefore, synBefore := e.tr.Calls(), e.syn.Calls()
	e.run(t)

	if e.tr.Calls() != trBefore {
		t.Error("editing the translation called the translator")
	}
	if got := readFile(t, path); got != edited {
		t.Error("the hand-edited translation was overwritten")
	}
	spoken := e.syn.Texts()[synBefore:]
	if len(spoken) != 1 || !strings.Contains(spoken[0], "as the editor insists") {
		t.Errorf("the speech engine got %q, want only the edited paragraph", spoken)
	}
	if st, _ := ReadStatus(e.ws); len(st.Edited) != 1 || st.Edited[0] != path {
		t.Errorf("status lists the edited files %v, want %s", st.Edited, path)
	}
}

func TestE2EEditedChapterBoundaries(t *testing.T) {
	e := newE2E(t)
	e.run(t)

	// The user edits the text of chapter 3, then merges chapters 3 and 4.
	ch3 := e.ws.ChapterPath(3)
	byHand := "# Chapter 3: Fog Signals\n\nThis chapter was rewritten by hand.\n"
	writeFile(t, ch3, byHand)

	starts, err := chapters.Load(e.ws)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := json.Marshal(append(slices.Clone(starts[:3]),
		chapters.Start{N: 3, Title: "Fog and Boats", StartPage: starts[3].StartPage, StartLine: starts[3].StartLine}))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, e.ws.ChaptersJSONPath(), string(merged))

	var log bytes.Buffer
	if err := Run(context.Background(), e.ws, e.deps(), quiet, &log, RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The boundaries come from the edited file…
	if marks := chapterMarks(t, e.book()); len(marks) != 4 {
		t.Errorf("chapter marks = %q, want 4 chapters after the merge", marks)
	}
	// …but the chapter file edited by hand is kept, and reported.
	if got := readFile(t, ch3); got != byHand {
		t.Errorf("the hand-edited chapter was overwritten:\n%s", got)
	}
	if !strings.Contains(log.String(), "1 kept with hand edits") {
		t.Errorf("the run does not report the kept chapter:\n%s", log.String())
	}

	// --force rebuilds the chapter from the pages with the new boundaries.
	e.ws.Manifest.Force = true
	e.run(t)
	e.ws.Manifest.Force = false
	rebuilt := readFile(t, ch3)
	if strings.Contains(rebuilt, "rewritten by hand") || !strings.Contains(rebuilt, "Fog arrives") {
		t.Errorf("with --force the chapter was not rebuilt:\n%s", rebuilt)
	}
}

func TestE2ESecondRunIsLocked(t *testing.T) {
	e := newE2E(t) // holds the lock of the book, like a running command
	other, err := workspace.Open(e.ws.Dir, workspace.OpenOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := other.Lock(); !errors.Is(err, workspace.ErrLocked) {
		t.Errorf("a second process could lock the book: error = %v", err)
	}
}
