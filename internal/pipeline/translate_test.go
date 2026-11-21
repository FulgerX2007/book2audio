package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/chapters"
	"github.com/FulgerX2007/book2audio/internal/translate"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// chapterText is a chapter to put into a test book.
type chapterText struct {
	n          int
	title      string
	paragraphs []string
}

// para returns a paragraph of about 105 characters that mentions id.
func para(id string) string {
	return fmt.Sprintf("Paragraph %s opens here. It goes on with a second sentence about %s. A third sentence closes paragraph %s.", id, id, id)
}

// paras returns the paragraphs named by ids.
func paras(ids ...string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = para(id)
	}
	return out
}

// newBook creates a working folder that already has the given chapters, as
// the chapters stage would leave them. Chunks hold about two paragraphs.
func newBook(t *testing.T, chs ...chapterText) *workspace.Workspace {
	t.Helper()
	tmp := t.TempDir()
	pdf := filepath.Join(tmp, "book.pdf")
	cfg := filepath.Join(tmp, "config.yaml")
	const config = "translate:\n  model: test-model\n  chunk_chars: 250\n  workers: 3\n" +
		"tts:\n  args: [\"--output_file\", \"{out}\"]\n  workers: 3\n"
	for path, content := range map[string]string{pdf: "%PDF-fake", cfg: config} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Init(pdf, workspace.InitOptions{ConfigPath: cfg, Workdir: filepath.Join(tmp, "work")})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	writeChapters(t, ws, chs...)
	return ws
}

// writeChapters writes chapters.json and the chapter files.
func writeChapters(t *testing.T, ws *workspace.Workspace, chs ...chapterText) {
	t.Helper()
	var starts []chapters.Start
	for i, ch := range chs {
		starts = append(starts, chapters.Start{N: ch.n, Title: ch.title, StartPage: i + 1, StartLine: 1})
		writeFile(t, ws.ChapterPath(ch.n), chapters.Format(ch.title, ch.paragraphs))
	}
	data, err := json.Marshal(starts)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, ws.ChaptersJSONPath(), string(data))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func translateAll(t *testing.T, ws *workspace.Workspace, tr translate.Translator) TranslateResult {
	t.Helper()
	res, err := Translate(context.Background(), ws, tr, quiet, TranslateOptions{Chapter: AllChapters})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	return res
}

// requestTexts returns the texts of the requests made since call number from.
func requestTexts(tr *translate.FakeTranslator, from int) []string {
	var out []string
	for _, req := range tr.Requests()[from:] {
		out = append(out, req.Text)
	}
	return out
}

// mentions reports whether any of the texts contains the paragraph id.
func mentions(texts []string, id string) bool {
	return slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, "Paragraph "+id+" ") })
}

func twoChapters() []chapterText {
	return []chapterText{
		{1, "Chapter 1", paras("A", "B", "C", "D", "E", "F")},
		{2, "Chapter 2", paras("P", "Q", "R", "S")},
	}
}

func TestTranslateFullPass(t *testing.T) {
	ws := newBook(t, twoChapters()...)
	tr := &translate.FakeTranslator{}

	res := translateAll(t, ws, tr)
	if res.Chapters != 2 || res.Chunks != 5 || res.Translated != 5 || res.Assembled != 2 || res.Failed != 0 {
		t.Errorf("Result = %+v, want 2 chapters, 5 chunks translated, 2 files assembled", res)
	}
	if tr.Calls() != 5 {
		t.Errorf("the translator was called %d times, want 5", tr.Calls())
	}

	// The fake keeps the text, so the assembled chapter is the source with marks.
	for _, ch := range twoChapters() {
		want := translate.FakeTranslation(strings.TrimSpace(chapters.Format(ch.title, ch.paragraphs))) + "\n"
		if got := readFile(t, ws.TranslatedChapterPath(ch.n)); got != want {
			t.Errorf("chapter %d:\n%s\nwant\n%s", ch.n, got, want)
		}
	}

	var refs []chunkRef
	if err := json.Unmarshal([]byte(readFile(t, ws.ChunksJSONPath(1))), &refs); err != nil || len(refs) != 3 {
		t.Fatalf("ch01.chunks.json = %+v, %v; want 3 chunks", refs, err)
	}
	for _, ref := range refs {
		if _, err := os.Stat(ws.ChunkPath(ref.Hash)); err != nil {
			t.Errorf("the cache file of chunk %s is missing", ref.Hash)
		}
	}
	if c := ws.Manifest.Counts(StageTranslate); c != (workspace.Counts{Total: 5, Done: 5}) {
		t.Errorf("Counts = %+v, want 5 done", c)
	}

	// Requests carry the languages and the end of the previous chunk.
	reqs := tr.Requests()
	for _, req := range reqs {
		if req.SourceLang != "en" || req.TargetLang != "ru" {
			t.Errorf("request languages = %q → %q", req.SourceLang, req.TargetLang)
		}
		first := strings.HasPrefix(req.Text, "# ")
		if first != (req.PrevContext == "") {
			t.Errorf("first chunk of a chapter = %v, context = %q", first, req.PrevContext)
		}
	}

	// A second run has nothing to do.
	res = translateAll(t, ws, tr)
	if res.Translated != 0 || res.Assembled != 0 || tr.Calls() != 5 {
		t.Errorf("second run: Result = %+v, calls = %d; want no work", res, tr.Calls())
	}
}

func TestTranslateResumesAfterFailure(t *testing.T) {
	ws := newBook(t, twoChapters()...)
	boom := errors.New("сервер недоступен")
	tr := &translate.FakeTranslator{Fail: func(req translate.Request) error {
		if strings.Contains(req.Text, "Paragraph C ") {
			return boom
		}
		return nil
	}}

	res, err := Translate(context.Background(), ws, tr, quiet, TranslateOptions{Chapter: AllChapters})
	if !errors.Is(err, workspace.ErrUnitsFailed) {
		t.Fatalf("error = %v, want ErrUnitsFailed", err)
	}
	if res.Failed != 1 || res.Translated != 4 || res.Assembled != 1 {
		t.Errorf("Result = %+v, want one failed chunk, the rest done, chapter 2 assembled", res)
	}
	if _, err := os.Stat(ws.TranslatedChapterPath(1)); !errors.Is(err, os.ErrNotExist) {
		t.Error("chapter 1 was assembled although one of its chunks failed")
	}
	if c := ws.Manifest.Counts(StageTranslate); c.Failed != 1 {
		t.Errorf("Counts = %+v, want the failure recorded", c)
	}
	for _, id := range ws.Manifest.UnitIDs(StageTranslate) {
		if u, _ := ws.Manifest.Unit(StageTranslate, id); u.Status == workspace.StatusFailed &&
			(u.Error != boom.Error() || u.Chapter != "ch01" || !strings.HasSuffix(u.Out, id+".ru.txt")) {
			t.Errorf("failed unit = %+v", u)
		}
	}

	// The next run translates only the chunk that failed.
	before := tr.Calls()
	tr.Fail = nil
	res = translateAll(t, ws, tr)
	if res.Translated != 1 || res.Assembled != 1 {
		t.Errorf("second run: Result = %+v, want one chunk and chapter 1", res)
	}
	if got := requestTexts(tr, before); len(got) != 1 || !mentions(got, "C") {
		t.Errorf("second run translated %q, want only the failed chunk", got)
	}
}

func TestTranslateEditedChapterRedoesOnlyTheTail(t *testing.T) {
	chs := twoChapters()
	ws := newBook(t, chs...)
	tr := &translate.FakeTranslator{}
	translateAll(t, ws, tr)

	// Insert a paragraph in the middle of chapter 1 (after B, the end of chunk 1).
	edited := slices.Clone(chs)
	edited[0].paragraphs = slices.Insert(slices.Clone(chs[0].paragraphs), 2, para("NEW"))
	writeChapters(t, ws, edited...)

	before := tr.Calls()
	res := translateAll(t, ws, tr)
	got := requestTexts(tr, before)
	if mentions(got, "A") || mentions(got, "B") {
		t.Errorf("chunks above the edit were translated again: %q", got)
	}
	if mentions(got, "P") || mentions(got, "S") {
		t.Errorf("chunks of the other chapter were translated again: %q", got)
	}
	if !mentions(got, "NEW") || !mentions(got, "F") {
		t.Errorf("the tail of the edited chapter was not translated: %q", got)
	}
	if res.Assembled != 1 || !strings.Contains(readFile(t, ws.TranslatedChapterPath(1)), "Paragraph NEW ") {
		t.Errorf("chapter 1 was not reassembled with the new paragraph: %+v", res)
	}

	// Undoing the edit needs no translator: the old chunks are in the cache.
	writeChapters(t, ws, chs...)
	before = tr.Calls()
	res = translateAll(t, ws, tr)
	if tr.Calls() != before {
		t.Errorf("undoing the edit called the translator: %q", requestTexts(tr, before))
	}
	if res.Assembled != 1 || strings.Contains(readFile(t, ws.TranslatedChapterPath(1)), "NEW") {
		t.Errorf("chapter 1 was not restored: %+v", res)
	}
}

func TestTranslateGlossaryEditRedoesOnlyChunksWithTheTerm(t *testing.T) {
	ws := newBook(t,
		chapterText{1, "Chapter 1", []string{para("A"), para("B"), "The keeper winds the fog bell twice every minute until the air is clear again, and then he sleeps."}},
		chapterText{2, "Chapter 2", paras("P", "Q")},
	)
	tr := &translate.FakeTranslator{}
	translateAll(t, ws, tr)

	writeFile(t, ws.GlossaryPath(), "terms:\n  - {source: fog bell, target: туманный колокол}\n")
	before := tr.Calls()
	translateAll(t, ws, tr)
	reqs := tr.Requests()[before:]
	if len(reqs) != 1 || !strings.Contains(reqs[0].Text, "fog bell") {
		t.Fatalf("after adding a term %d chunks were translated, want only the one that mentions it", len(reqs))
	}
	if want := []translate.Term{{Source: "fog bell", Target: "туманный колокол"}}; !slices.Equal(reqs[0].Glossary, want) {
		t.Errorf("glossary of the request = %+v, want %+v", reqs[0].Glossary, want)
	}

	// Changing the translation of the term redoes the same single chunk.
	writeFile(t, ws.GlossaryPath(), "terms:\n  - {source: fog bell, target: противотуманный колокол}\n")
	before = tr.Calls()
	translateAll(t, ws, tr)
	if n := tr.Calls() - before; n != 1 {
		t.Errorf("after changing the term %d chunks were translated, want 1", n)
	}

	// A term that occurs nowhere changes nothing.
	writeFile(t, ws.GlossaryPath(), "terms:\n  - {source: fog bell, target: противотуманный колокол}\n  - {source: Pemberley, target: Пемберли}\n")
	before = tr.Calls()
	translateAll(t, ws, tr)
	if tr.Calls() != before {
		t.Error("a term that occurs in no chunk caused translations")
	}
}

func TestTranslateSettingsChangeRedoesEverything(t *testing.T) {
	for name, change := range map[string]func(*workspace.Workspace){
		"model":       func(ws *workspace.Workspace) { ws.Config.Translate.Model = "other-model" },
		"temperature": func(ws *workspace.Workspace) { ws.Config.Translate.Temperature = 0.7 },
		"target":      func(ws *workspace.Workspace) { ws.Config.TargetLang = "uk" },
	} {
		t.Run(name, func(t *testing.T) {
			ws := newBook(t, twoChapters()...)
			tr := &translate.FakeTranslator{}
			translateAll(t, ws, tr)

			change(ws)
			res := translateAll(t, ws, tr)
			if res.Translated != 5 || res.Assembled != 2 {
				t.Errorf("Result = %+v, want every chunk translated again", res)
			}
			if c := ws.Manifest.Counts(StageTranslate); c.Total != 5 {
				t.Errorf("Counts = %+v, want the old units pruned", c)
			}
		})
	}
}

func TestTranslateKeepsHandEditedTranslation(t *testing.T) {
	chs := twoChapters()
	ws := newBook(t, chs...)
	tr := &translate.FakeTranslator{}
	translateAll(t, ws, tr)

	edited := "# Глава 1\n\nПеревод, исправленный вручную.\n"
	writeFile(t, ws.TranslatedChapterPath(1), edited)

	// Nothing changed in the source: the edited file is simply up to date.
	if res := translateAll(t, ws, tr); res.Assembled != 0 || res.Kept != 0 {
		t.Errorf("Result = %+v, want nothing to do", res)
	}

	// --force discards the edit even though the source is unchanged.
	ws.Manifest.Force = true
	if res := translateAll(t, ws, tr); res.Assembled != 1 || strings.Contains(readFile(t, ws.TranslatedChapterPath(1)), "исправленный вручную") {
		t.Errorf("with Force and an unchanged source: Result = %+v, want the chapter reassembled", res)
	}
	ws.Manifest.Force = false
	writeFile(t, ws.TranslatedChapterPath(1), edited)

	// The source changes: the chunks are translated, the edited file stays.
	changed := slices.Clone(chs)
	changed[0].paragraphs = append(slices.Clone(chs[0].paragraphs), para("G"))
	writeChapters(t, ws, changed...)
	res := translateAll(t, ws, tr)
	if res.Kept != 1 || res.Assembled != 0 || res.Translated == 0 {
		t.Errorf("Result = %+v, want the edited chapter kept", res)
	}
	if got := readFile(t, ws.TranslatedChapterPath(1)); got != edited {
		t.Errorf("the hand-edited translation was overwritten:\n%s", got)
	}

	// --force rebuilds it from the chunks.
	ws.Manifest.Force = true
	res = translateAll(t, ws, tr)
	if res.Assembled != 1 || !strings.Contains(readFile(t, ws.TranslatedChapterPath(1)), "Paragraph G ") {
		t.Errorf("with Force the chapter was not rebuilt: %+v", res)
	}
}

func TestTranslateValidatesResults(t *testing.T) {
	t.Run("one bad answer is asked again", func(t *testing.T) {
		ws := newBook(t, chapterText{1, "Chapter 1", paras("A")})
		calls := 0
		tr := &translate.FakeTranslator{Reply: func(req translate.Request) string {
			if calls++; calls == 1 {
				return "  "
			}
			return translate.FakeTranslation(req.Text)
		}}
		res := translateAll(t, ws, tr)
		if res.Translated != 1 || tr.Calls() != 2 {
			t.Errorf("Result = %+v, calls = %d; want success on the second answer", res, tr.Calls())
		}
	})
	for name, bad := range map[string]string{
		"always empty":     "",
		"always too short": "Нет.",
		"always too long":  strings.Repeat("слово ", 400),
	} {
		t.Run(name, func(t *testing.T) {
			ws := newBook(t, chapterText{1, "Chapter 1", paras("A")})
			tr := &translate.FakeTranslator{Reply: func(translate.Request) string { return bad }}

			res, err := Translate(context.Background(), ws, tr, quiet, TranslateOptions{Chapter: AllChapters})
			if !errors.Is(err, workspace.ErrUnitsFailed) || res.Failed != 1 {
				t.Fatalf("Result = %+v, error = %v; want one failed chunk", res, err)
			}
			if tr.Calls() != 2 {
				t.Errorf("the translator was called %d times, want exactly one repeat", tr.Calls())
			}
			id := ws.Manifest.UnitIDs(StageTranslate)[0]
			if u, _ := ws.Manifest.Unit(StageTranslate, id); !strings.Contains(u.Error, "translation") {
				t.Errorf("recorded error = %q", u.Error)
			}
			// The rejected answer is kept for inspection, beside the cache
			// path and never at it.
			rejected := RejectedPath(ws.ChunkPath(id))
			if bad == "" {
				if _, err := os.Stat(rejected); !errors.Is(err, os.ErrNotExist) {
					t.Error("a file was written for an empty answer")
				}
			} else if got := readFile(t, rejected); strings.TrimSpace(got) != strings.TrimSpace(bad) {
				t.Errorf("rejected answer file = %q, want the answer of the model", got)
			}
			if _, err := os.Stat(ws.ChunkPath(id)); !errors.Is(err, os.ErrNotExist) {
				t.Error("the rejected answer was stored at the cache path")
			}

			// Moved to the cache path by hand, the answer is accepted.
			if bad != "" {
				if err := os.Rename(rejected, ws.ChunkPath(id)); err != nil {
					t.Fatal(err)
				}
				before := tr.Calls()
				res := translateAll(t, ws, tr)
				if tr.Calls() != before || res.Assembled != 1 {
					t.Errorf("after accepting the answer by hand: calls +%d, Result = %+v", tr.Calls()-before, res)
				}
			}
		})
	}
}

func TestCheckTranslation(t *testing.T) {
	long := strings.Repeat("a", 100)
	tests := []struct {
		source, translated string
		ok                 bool
	}{
		{long, strings.Repeat("б", 100), true},
		{long, strings.Repeat("б", 30), true},
		{long, strings.Repeat("б", 29), false},
		{long, strings.Repeat("б", 300), true},
		{long, strings.Repeat("б", 301), false},
		{long, "", false},
		{"Short.", "Очень длинный перевод короткой фразы, длиннее втрое.", true},
		{"Short.", "", false},
	}
	for _, tt := range tests {
		if err := checkTranslation(tt.source, tt.translated); (err == nil) != tt.ok {
			t.Errorf("checkTranslation(%d chars, %d chars) = %v, want ok=%v", len(tt.source), len([]rune(tt.translated)), err, tt.ok)
		}
	}
}

func TestTranslateSingleChapter(t *testing.T) {
	ws := newBook(t, twoChapters()...)
	tr := &translate.FakeTranslator{}

	res, err := Translate(context.Background(), ws, tr, quiet, TranslateOptions{Chapter: 2})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if res.Chapters != 1 || res.Chunks != 2 || res.Translated != 2 || res.Assembled != 1 {
		t.Errorf("Result = %+v, want only chapter 2", res)
	}
	if got := requestTexts(tr, 0); mentions(got, "A") {
		t.Errorf("chapter 1 was translated: %q", got)
	}
	if _, err := os.Stat(ws.TranslatedChapterPath(1)); !errors.Is(err, os.ErrNotExist) {
		t.Error("chapter 1 was assembled")
	}

	// Working on one chapter must not drop the units of the other.
	translateAll(t, ws, tr)
	before := tr.Calls()
	if _, err := Translate(context.Background(), ws, tr, quiet, TranslateOptions{Chapter: 1}); err != nil {
		t.Fatal(err)
	}
	if c := ws.Manifest.Counts(StageTranslate); c.Total != 5 || tr.Calls() != before {
		t.Errorf("Counts = %+v, new calls = %d; want all 5 units kept and no work", c, tr.Calls()-before)
	}

	if _, err := Translate(context.Background(), ws, tr, quiet, TranslateOptions{Chapter: 9}); err == nil || !strings.Contains(err.Error(), "chapter 9 is not") {
		t.Errorf("unknown chapter: error = %v", err)
	}
}

func TestTranslatePrunesStaleUnits(t *testing.T) {
	chs := twoChapters()
	ws := newBook(t, chs...)
	tr := &translate.FakeTranslator{}
	translateAll(t, ws, tr)

	// Chapter 2 disappears and chapter 1 gets shorter.
	writeChapters(t, ws, chapterText{1, "Chapter 1", paras("A", "B")})
	translateAll(t, ws, tr)
	if c := ws.Manifest.Counts(StageTranslate); c.Total != 1 {
		t.Errorf("chunk units = %+v, want only the remaining chunk", c)
	}
	if ids := ws.Manifest.UnitIDs(StageTranslated); !slices.Equal(ids, []string{"ch01"}) {
		t.Errorf("chapter units = %v, want [ch01]", ids)
	}
}

func TestTranslateSameTextIsTranslatedOnce(t *testing.T) {
	ws := newBook(t,
		chapterText{1, "Note", paras("A")},
		chapterText{2, "Note", paras("A")},
	)
	tr := &translate.FakeTranslator{}
	res := translateAll(t, ws, tr)
	if tr.Calls() != 1 || res.Assembled != 2 {
		t.Errorf("calls = %d, Result = %+v; want one translation shared by both chapters", tr.Calls(), res)
	}
}

func TestTranslatePreconditions(t *testing.T) {
	t.Run("no model", func(t *testing.T) {
		ws := newBook(t, twoChapters()...)
		ws.Config.Translate.Model = ""
		_, err := Translate(context.Background(), ws, &translate.FakeTranslator{}, quiet, TranslateOptions{Chapter: AllChapters})
		if err == nil || !strings.Contains(err.Error(), "translate.model") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("chapters stage has not run", func(t *testing.T) {
		ws := newBook(t)
		os.Remove(ws.ChaptersJSONPath())
		_, err := Translate(context.Background(), ws, &translate.FakeTranslator{}, quiet, TranslateOptions{Chapter: AllChapters})
		if err == nil || !strings.Contains(err.Error(), "chapters") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("chapter file is missing", func(t *testing.T) {
		ws := newBook(t, twoChapters()...)
		os.Remove(ws.ChapterPath(2))
		_, err := Translate(context.Background(), ws, &translate.FakeTranslator{}, quiet, TranslateOptions{Chapter: AllChapters})
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error = %v, want os.ErrNotExist", err)
		}
	})
	t.Run("broken glossary", func(t *testing.T) {
		ws := newBook(t, twoChapters()...)
		writeFile(t, ws.GlossaryPath(), "terms: [")
		_, err := Translate(context.Background(), ws, &translate.FakeTranslator{}, quiet, TranslateOptions{Chapter: AllChapters})
		if err == nil || !strings.Contains(err.Error(), "glossary") {
			t.Errorf("error = %v", err)
		}
	})
}

func TestTranslateCancellationIsNotAFailure(t *testing.T) {
	ws := newBook(t, twoChapters()...)
	ctx, cancel := context.WithCancel(context.Background())
	tr := &translate.FakeTranslator{}
	tr.Fail = func(translate.Request) error {
		if tr.Calls() >= 2 {
			cancel()
			return context.Canceled
		}
		return nil
	}

	_, err := Translate(ctx, ws, tr, quiet, TranslateOptions{Chapter: AllChapters})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if c := ws.Manifest.Counts(StageTranslate); c.Failed != 0 {
		t.Errorf("Counts = %+v: an interrupted chunk was recorded as failed", c)
	}

	// The interrupted run is picked up where it stopped.
	done := ws.Manifest.Counts(StageTranslate).Done
	tr.Fail = nil
	before := tr.Calls()
	res := translateAll(t, ws, tr)
	if res.Translated != 5-done || tr.Calls()-before != 5-done {
		t.Errorf("resumed run translated %d chunks, want %d", res.Translated, 5-done)
	}
}
