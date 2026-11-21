package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
	"github.com/FulgerX2007/book2audio/internal/translate"
	"github.com/FulgerX2007/book2audio/internal/tts"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// pageBody is a page of plain text long enough to have a "text layer".
func pageBody(id string) string {
	line := "This line is as wide as the lines of this page usually are, yes"
	return "Paragraph " + id + " " + line[len(id)+11:] + "\n" + line + "\nand it ends here."
}

// fakes bundles the fake backends of a whole pipeline.
type fakes struct {
	runner *encoder
	tr     *translate.FakeTranslator
	syn    *tts.FakeSynthesizer
}

// newFakes returns fake tools for a PDF with two chapters on three pages.
func newFakes() *fakes {
	f := &fakes{runner: newEncoder(), tr: &translate.FakeTranslator{}, syn: &tts.FakeSynthesizer{}}
	pages := []string{
		"Chapter 1: The Start\n" + pageBody("A"),
		pageBody("B") + "\n\nChapter 2: The Middle\n" + pageBody("C"),
		pageBody("D"),
	}
	f.runner.Stub("pdfinfo", fmt.Appendf(nil, "Title: Demo Book\nAuthor: Somebody\nPages: %d\n", len(pages)), nil)
	f.runner.Stub("pdftotext", []byte(strings.Join(pages, "\f")+"\f"), nil)
	return f
}

func (f *fakes) deps() Deps {
	return Deps{
		Runner:         f.runner,
		NewTranslator:  func(config.Translate) (translate.Translator, error) { return f.tr, nil },
		NewSynthesizer: func(config.TTS, execx.Runner) (tts.Synthesizer, error) { return f.syn, nil },
	}
}

// emptyBook creates a working folder for a stand-in PDF: nothing is
// extracted yet.
func emptyBook(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws := newBook(t)
	os.Remove(ws.ChaptersJSONPath())
	ws.Config.Extract.OCRMinChars = 0
	return ws
}

func TestRunAllStages(t *testing.T) {
	ws := emptyBook(t)
	f := newFakes()
	var out bytes.Buffer

	if err := Run(context.Background(), ws, f.deps(), quiet, &out, RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// One line per stage, in pipeline order.
	var stages []string
	for line := range strings.Lines(out.String()) {
		if name, _, ok := strings.Cut(line, ":"); ok && slices.Contains(Stages, name) {
			stages = append(stages, name)
		}
	}
	if !slices.Equal(stages, Stages) {
		t.Errorf("stages reported = %v, want %v\n%s", stages, Stages, out.String())
	}
	book := filepath.Join(ws.OutDir(), filepath.Base(ws.Dir)+".m4b")
	if _, err := os.Stat(book); err != nil || !strings.Contains(out.String(), book) {
		t.Errorf("the audiobook %s: %v\n%s", book, err, out.String())
	}
	if f.tr.Calls() == 0 || f.syn.Calls() == 0 || len(f.runner.CallsTo("ffmpeg")) != 1 {
		t.Errorf("calls: translator %d, synthesizer %d, ffmpeg %d", f.tr.Calls(), f.syn.Calls(), len(f.runner.CallsTo("ffmpeg")))
	}
	// The tags of the PDF reach the audiobook.
	if !strings.Contains(f.runner.metadata[0], "title=Demo Book\n") || !strings.Contains(f.runner.metadata[0], "artist=Somebody\n") {
		t.Errorf("metadata:\n%s", f.runner.metadata[0])
	}

	// A second run does no work at all.
	tr, syn, ffmpeg := f.tr.Calls(), f.syn.Calls(), len(f.runner.CallsTo("ffmpeg"))
	out.Reset()
	if err := Run(context.Background(), ws, f.deps(), quiet, &out, RunOptions{}); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if f.tr.Calls() != tr || f.syn.Calls() != syn || len(f.runner.CallsTo("ffmpeg")) != ffmpeg {
		t.Error("the second run called a backend again")
	}
	if !strings.Contains(out.String(), "0 translated now") || !strings.Contains(out.String(), "0 synthesised now") {
		t.Errorf("summary of the second run:\n%s", out.String())
	}
}

func TestRunFromTo(t *testing.T) {
	ws := emptyBook(t)
	f := newFakes()
	var out bytes.Buffer

	if err := Run(context.Background(), ws, f.deps(), quiet, &out, RunOptions{To: "chapters"}); err != nil {
		t.Fatalf("Run to chapters: %v", err)
	}
	if f.tr.Calls() != 0 || strings.Contains(out.String(), "translate:") {
		t.Errorf("stages after --to ran:\n%s", out.String())
	}
	if _, err := os.Stat(ws.ChapterPath(1)); err != nil {
		t.Errorf("chapters did not run: %v", err)
	}

	out.Reset()
	if err := Run(context.Background(), ws, f.deps(), quiet, &out, RunOptions{From: "translate", To: "synth"}); err != nil {
		t.Fatalf("Run translate..synth: %v", err)
	}
	if strings.Contains(out.String(), "extract:") || strings.Contains(out.String(), "build:") {
		t.Errorf("stages outside --from/--to ran:\n%s", out.String())
	}
	if f.tr.Calls() == 0 || f.syn.Calls() == 0 || len(f.runner.CallsTo("ffmpeg")) != 0 {
		t.Errorf("calls: translator %d, synthesizer %d, ffmpeg %d", f.tr.Calls(), f.syn.Calls(), len(f.runner.CallsTo("ffmpeg")))
	}

	for _, tt := range []struct {
		opts RunOptions
		want string
	}{
		{RunOptions{From: "polish"}, `--from: unknown stage "polish"`},
		{RunOptions{To: "publish"}, `--to: unknown stage "publish"`},
		{RunOptions{From: "synth", To: "extract"}, "comes after"},
	} {
		err := Run(context.Background(), ws, f.deps(), quiet, &out, tt.opts)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Run(%+v) error = %v, want one containing %q", tt.opts, err, tt.want)
		}
	}
}

func TestRunStopsAtAStageWithFailedUnits(t *testing.T) {
	ws := emptyBook(t)
	f := newFakes()
	boom := errors.New("сервер недоступен")
	f.tr.Fail = func(req translate.Request) error {
		if strings.Contains(req.Text, "Paragraph C ") {
			return boom
		}
		return nil
	}
	var out bytes.Buffer

	err := Run(context.Background(), ws, f.deps(), quiet, &out, RunOptions{})
	if !errors.Is(err, workspace.ErrUnitsFailed) || !strings.Contains(err.Error(), "stage translate") {
		t.Fatalf("error = %v, want ErrUnitsFailed from the translate stage", err)
	}
	if f.syn.Calls() != 0 || len(f.runner.CallsTo("ffmpeg")) != 0 {
		t.Error("later stages ran although translate had failed units")
	}
	if !strings.Contains(out.String(), "translate:") || !strings.Contains(out.String(), "1 failed") {
		t.Errorf("the summary of the failed stage is missing:\n%s", out.String())
	}

	// Once the cause is gone the run completes, redoing only what failed.
	f.tr.Fail = nil
	before := f.tr.Calls()
	if err := Run(context.Background(), ws, f.deps(), quiet, &out, RunOptions{}); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if n := f.tr.Calls() - before; n != 1 {
		t.Errorf("the second run translated %d chunks, want 1", n)
	}
}

func TestRunChecksConfigBeforeAnyWork(t *testing.T) {
	for name, tt := range map[string]struct {
		change func(*workspace.Workspace)
		opts   RunOptions
		want   string
	}{
		"no model":            {func(ws *workspace.Workspace) { ws.Config.Translate.Model = "" }, RunOptions{}, "translate.model"},
		"no engine arguments": {func(ws *workspace.Workspace) { ws.Config.TTS.Args = nil }, RunOptions{}, "tts.args"},
	} {
		t.Run(name, func(t *testing.T) {
			ws := emptyBook(t)
			tt.change(ws)
			f := newFakes()
			err := Run(context.Background(), ws, f.deps(), quiet, &bytes.Buffer{}, tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
			if len(f.runner.Calls()) != 0 {
				t.Error("a stage ran although the config is incomplete")
			}
		})
	}

	// A run that stops before the stage does not need its settings.
	ws := emptyBook(t)
	ws.Config.Translate.Model = ""
	ws.Config.TTS.Args = nil
	if err := Run(context.Background(), ws, newFakes().deps(), quiet, &bytes.Buffer{}, RunOptions{To: "chapters"}); err != nil {
		t.Errorf("Run to chapters without translation settings: %v", err)
	}
}

func TestRunBackendCreationFails(t *testing.T) {
	ws := emptyBook(t)
	f := newFakes()
	deps := f.deps()
	boom := errors.New("нет такого бэкенда")
	deps.NewTranslator = func(config.Translate) (translate.Translator, error) { return nil, boom }
	if err := Run(context.Background(), ws, deps, quiet, &bytes.Buffer{}, RunOptions{}); !errors.Is(err, boom) {
		t.Errorf("error = %v, want %v", err, boom)
	}

	deps = f.deps()
	deps.NewSynthesizer = func(config.TTS, execx.Runner) (tts.Synthesizer, error) { return nil, boom }
	if err := Run(context.Background(), ws, deps, quiet, &bytes.Buffer{}, RunOptions{}); !errors.Is(err, boom) {
		t.Errorf("error = %v, want %v", err, boom)
	}
}

func TestRunCancellation(t *testing.T) {
	ws := emptyBook(t)
	f := newFakes()
	ctx, cancel := context.WithCancel(context.Background())
	f.tr.Fail = func(translate.Request) error {
		cancel()
		return context.Canceled
	}

	err := Run(ctx, ws, f.deps(), quiet, &bytes.Buffer{}, RunOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if f.syn.Calls() != 0 {
		t.Error("the synth stage ran after the cancellation")
	}

	// The manifest is intact and the run can be resumed.
	reopened, err := workspace.Open(ws.Dir, workspace.OpenOptions{})
	if err != nil {
		t.Fatalf("the working folder cannot be reopened: %v", err)
	}
	reopened.Config = ws.Config
	f.tr.Fail = nil
	if err := Run(context.Background(), reopened, f.deps(), quiet, &bytes.Buffer{}, RunOptions{}); err != nil {
		t.Errorf("resumed Run: %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, reopened, f.deps(), quiet, &bytes.Buffer{}, RunOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Run with a cancelled context: error = %v", err)
	}
}

func TestStatus(t *testing.T) {
	t.Run("new book", func(t *testing.T) {
		ws := emptyBook(t)
		st, err := ReadStatus(ws)
		if err != nil {
			t.Fatalf("ReadStatus: %v", err)
		}
		for _, s := range st.Stages {
			if s.Counts != (workspace.Counts{}) {
				t.Errorf("stage %s of a new book = %+v", s.Name, s.Counts)
			}
		}
		if len(st.Failures) != 0 || len(st.Edited) != 0 {
			t.Errorf("a new book has failures or edited files: %+v", st)
		}
		var out bytes.Buffer
		st.Write(&out)
		if !strings.Contains(out.String(), ws.Dir) || strings.Contains(out.String(), "Failures:") || strings.Contains(out.String(), "Edited by hand") {
			t.Errorf("status of a new book:\n%s", out.String())
		}
	})

	t.Run("finished book", func(t *testing.T) {
		ws := emptyBook(t)
		if err := Run(context.Background(), ws, newFakes().deps(), quiet, &bytes.Buffer{}, RunOptions{}); err != nil {
			t.Fatal(err)
		}
		st, err := ReadStatus(ws)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]int{"extract": 3, "chapters": 3, "translated": 2, "build": 3}
		for _, s := range st.Stages {
			if s.Counts.Done != s.Counts.Total || s.Counts.Failed != 0 || s.Counts.Total == 0 {
				t.Errorf("stage %s = %+v, want everything done", s.Name, s.Counts)
			}
			if n, ok := want[s.Name]; ok && s.Counts.Total != n {
				t.Errorf("stage %s has %d units, want %d", s.Name, s.Counts.Total, n)
			}
		}
		var out bytes.Buffer
		st.Write(&out)
		for _, col := range []string{"Stage", "Total", "Done", "Failed", "extract", "translate", "synth", "build"} {
			if !strings.Contains(out.String(), col) {
				t.Errorf("the table lacks %q:\n%s", col, out.String())
			}
		}
	})

	t.Run("failures and hand-edited files", func(t *testing.T) {
		ws := emptyBook(t)
		f := newFakes()
		f.tr.Fail = func(req translate.Request) error {
			if strings.Contains(req.Text, "Paragraph C ") {
				return errors.New("сервер недоступен")
			}
			return nil
		}
		Run(context.Background(), ws, f.deps(), quiet, &bytes.Buffer{}, RunOptions{})
		// The user edits chapter 1 of the source and its translation.
		writeFile(t, ws.ChapterPath(1), readFile(t, ws.ChapterPath(1))+"\nДобавлено вручную.\n")
		writeFile(t, ws.TranslatedChapterPath(1), "# Глава 1\n\nИсправлено вручную.\n")

		st, err := ReadStatus(ws)
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Failures) != 1 {
			t.Fatalf("failures = %+v, want one", st.Failures)
		}
		fail := st.Failures[0]
		if fail.Stage != StageTranslate || fail.Chapter != "ch02" || fail.Error != "сервер недоступен" ||
			!strings.HasPrefix(fail.Out, ws.Dir) || !strings.HasSuffix(fail.Out, ".ru.txt") {
			t.Errorf("failure = %+v", fail)
		}
		if !slices.Equal(st.Edited, []string{ws.ChapterPath(1), ws.TranslatedChapterPath(1)}) {
			t.Errorf("edited files = %v", st.Edited)
		}

		var out bytes.Buffer
		st.Write(&out)
		for _, want := range []string{"Failures:", "translate ch02: сервер недоступен", "a result can be supplied by hand at: " + fail.Out,
			"Edited by hand", ws.ChapterPath(1), ws.TranslatedChapterPath(1)} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("status lacks %q:\n%s", want, out.String())
			}
		}

		// A translation put by hand at the reported path is accepted: the
		// next run does not call the translator for that chunk.
		writeFile(t, fail.Out, "Перевод, сделанный вручную.\n")
		f.tr.Fail = nil
		before := f.tr.Calls()
		if err := Run(context.Background(), ws, f.deps(), quiet, &bytes.Buffer{}, RunOptions{}); err != nil {
			t.Fatalf("Run after the manual result: %v", err)
		}
		// (Chapter 1 was edited above, so its changed chunk is translated;
		// the chunk that had failed must not be requested again.)
		if got := requestTexts(f.tr, before); mentions(got, "C") {
			t.Errorf("the translator was called for the chunk whose result was supplied by hand: %q", got)
		}
		if got := readFile(t, ws.TranslatedChapterPath(2)); !strings.Contains(got, "Перевод, сделанный вручную.") {
			t.Errorf("the manual translation is not in chapter 2:\n%s", got)
		}
		if st, _ := ReadStatus(ws); len(st.Failures) != 0 {
			t.Errorf("failures after the manual result = %+v", st.Failures)
		}
	})
}

func TestStatusChapters(t *testing.T) {
	t.Run("before the chapters stage there is no chapter list", func(t *testing.T) {
		st, err := ReadStatus(emptyBook(t))
		if err != nil || len(st.Chapters) != 0 {
			t.Errorf("chapters = %+v, %v; want none", st.Chapters, err)
		}
	})

	t.Run("finished book", func(t *testing.T) {
		ws := emptyBook(t)
		if err := Run(context.Background(), ws, newFakes().deps(), quiet, &bytes.Buffer{}, RunOptions{}); err != nil {
			t.Fatal(err)
		}
		st, err := ReadStatus(ws)
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Chapters) != 2 {
			t.Fatalf("chapters = %+v, want 2", st.Chapters)
		}
		for _, ch := range st.Chapters {
			if ch.Chunks == 0 || ch.ChunksDone != ch.Chunks || !ch.Translated ||
				ch.Segments == 0 || ch.SegmentsDone != ch.Segments || !ch.Audio ||
				ch.ChunksFailed != 0 || ch.SegmentsFailed != 0 {
				t.Errorf("chapter %d = %+v, want everything done", ch.N, ch)
			}
		}
		if st.Chapters[0].Title != "Chapter 1: The Start" {
			t.Errorf("title = %q", st.Chapters[0].Title)
		}
		var out bytes.Buffer
		st.Write(&out)
		for _, want := range []string{"Chapters:", "Title", "Translation", "Speech", "Chapter 2: The Middle", "✓"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("status lacks %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("partly done book", func(t *testing.T) {
		ws := emptyBook(t)
		f := newFakes()
		f.tr.Fail = func(req translate.Request) error {
			if strings.Contains(req.Text, "Paragraph C ") {
				return errors.New("сервер недоступен")
			}
			return nil
		}
		// Translation fails in chapter 2, so only chapter 1 is assembled;
		// then chapter 1 alone is synthesised.
		Run(context.Background(), ws, f.deps(), quiet, &bytes.Buffer{}, RunOptions{})
		if _, err := Synth(context.Background(), ws, f.syn, quiet, SynthOptions{Chapter: 1}); err != nil {
			t.Fatal(err)
		}

		st, err := ReadStatus(ws)
		if err != nil {
			t.Fatal(err)
		}
		one, two := st.Chapters[0], st.Chapters[1]
		if !one.Translated || one.ChunksDone != one.Chunks || one.Segments == 0 || one.SegmentsDone != one.Segments || one.Audio {
			t.Errorf("chapter 1 = %+v, want translated and spoken, audio not joined", one)
		}
		if two.Translated || two.ChunksFailed != 1 || two.ChunksDone != two.Chunks-1 || two.Segments != 0 || two.SegmentsDone != 0 {
			t.Errorf("chapter 2 = %+v, want one failed chunk and no speech", two)
		}
		var out bytes.Buffer
		st.Write(&out)
		if !strings.Contains(out.String(), "failed 1") {
			t.Errorf("status does not show the failed chunk of chapter 2:\n%s", out.String())
		}
	})

	t.Run("while a stage is running the totals are already known", func(t *testing.T) {
		ws := emptyBook(t)
		f := newFakes()
		// Interrupt the translation, as status sees it from another window.
		ctx, cancel := context.WithCancel(context.Background())
		f.tr.Fail = func(translate.Request) error {
			if f.tr.Calls() >= 2 {
				cancel()
				return context.Canceled
			}
			return nil
		}
		Run(ctx, ws, f.deps(), quiet, &bytes.Buffer{}, RunOptions{})

		st, err := ReadStatus(ws)
		if err != nil {
			t.Fatal(err)
		}
		done, total := 0, 0
		for _, ch := range st.Chapters {
			if ch.Chunks == 0 || ch.Translated {
				t.Errorf("chapter %d = %+v, want a known total and no assembled translation", ch.N, ch)
			}
			done += ch.ChunksDone
			total += ch.Chunks
		}
		if done == 0 || done >= total {
			t.Errorf("done = %d of %d, want a partial count", done, total)
		}
		var out bytes.Buffer
		st.Write(&out)
		if strings.Contains(out.String(), "/?") {
			t.Errorf("status shows an unknown total:\n%s", out.String())
		}
	})
}

func TestProgressAndShorten(t *testing.T) {
	for _, tt := range []struct {
		done, total, failed int
		complete            bool
		want                string
	}{
		{0, 0, 0, false, "—"},
		{3, 8, 0, false, "3/8"},
		{8, 8, 0, true, "8/8 ✓"},
		{7, 8, 1, false, "7/8, failed 1"},
		{4, 0, 0, false, "4/?"},
		{0, 5, 0, false, "0/5"},
	} {
		if got := progress(tt.done, tt.total, tt.failed, tt.complete); got != tt.want {
			t.Errorf("progress(%d, %d, %d, %v) = %q, want %q", tt.done, tt.total, tt.failed, tt.complete, got, tt.want)
		}
	}
	if got := shorten("короткий", 40); got != "короткий" {
		t.Errorf("shorten of a short title = %q", got)
	}
	if got := shorten(strings.Repeat("я", 50), 40); len([]rune(got)) != 40 || !strings.HasSuffix(got, "…") {
		t.Errorf("shorten of a long title = %q", got)
	}
}
