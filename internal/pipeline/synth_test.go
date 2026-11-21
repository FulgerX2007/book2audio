package pipeline

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/FulgerX2007/book2audio/internal/audio"
	"github.com/FulgerX2007/book2audio/internal/translate"
	"github.com/FulgerX2007/book2audio/internal/tts"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// translatedBook returns a book whose chapters are already translated (by
// the fake translator).
func translatedBook(t *testing.T, chs ...chapterText) *workspace.Workspace {
	t.Helper()
	ws := newBook(t, chs...)
	translateAll(t, ws, &translate.FakeTranslator{})
	return ws
}

func synthAll(t *testing.T, ws *workspace.Workspace, syn tts.Synthesizer) SynthResult {
	t.Helper()
	res, err := Synth(context.Background(), ws, syn, quiet, SynthOptions{Chapter: AllChapters})
	if err != nil {
		t.Fatalf("Synth: %v", err)
	}
	return res
}

// spoke reports whether any of the texts contains the paragraph id.
func spoke(texts []string, id string) bool { return mentions(texts, id) }

func TestSynthFullPass(t *testing.T) {
	ws := translatedBook(t, twoChapters()...)
	syn := &tts.FakeSynthesizer{}

	res := synthAll(t, ws, syn)
	// Chapter 1: title + 6 paragraphs; chapter 2: title + 4 paragraphs.
	if res != (SynthResult{Chapters: 2, Segments: 12, Synthesized: 12}) {
		t.Errorf("Result = %+v, want 12 segments synthesised", res)
	}
	if syn.Calls() != 12 {
		t.Errorf("the synthesizer was called %d times, want 12", syn.Calls())
	}

	refs, err := LoadSegments(ws, 1)
	if err != nil || len(refs) != 7 {
		t.Fatalf("segments of chapter 1 = %d, %v; want 7", len(refs), err)
	}
	if !refs[0].Heading || !refs[0].EndsParagraph {
		t.Errorf("the first segment is not the heading: %+v", refs[0])
	}
	for i, ref := range refs {
		if i > 0 && ref.Heading {
			t.Errorf("segment %d is marked as a heading", i)
		}
		if !ref.EndsParagraph {
			t.Errorf("segment %d does not end its paragraph although each paragraph is one segment", i)
		}
		clip, err := audio.ReadWAV(ws.SegmentPath(ref.Hash))
		if err != nil || clip.Frames() == 0 {
			t.Errorf("audio of segment %d: %d frames, %v", i, clip.Frames(), err)
		}
	}
	if c := ws.Manifest.Counts(StageSynth); c != (workspace.Counts{Total: 12, Done: 12}) {
		t.Errorf("Counts = %+v, want 12 done", c)
	}
	for _, text := range syn.Texts() {
		if strings.Contains(text, "#") || strings.Contains(text, "\n") {
			t.Errorf("unprepared text was sent to the engine: %q", text)
		}
	}

	// A second run has nothing to do.
	res = synthAll(t, ws, syn)
	if res.Synthesized != 0 || syn.Calls() != 12 {
		t.Errorf("second run: Result = %+v, calls = %d; want no work", res, syn.Calls())
	}
}

func TestSynthResumesAfterFailure(t *testing.T) {
	ws := translatedBook(t, twoChapters()...)
	boom := errors.New("piper: exit status 1")
	syn := &tts.FakeSynthesizer{Fail: func(text string) error {
		if strings.Contains(text, "Paragraph C ") {
			return boom
		}
		return nil
	}}

	res, err := Synth(context.Background(), ws, syn, quiet, SynthOptions{Chapter: AllChapters})
	if !errors.Is(err, workspace.ErrUnitsFailed) {
		t.Fatalf("error = %v, want ErrUnitsFailed", err)
	}
	if res.Failed != 1 || res.Synthesized != 11 {
		t.Errorf("Result = %+v, want one failed segment and the rest done", res)
	}
	var failed workspace.Unit
	for _, id := range ws.Manifest.UnitIDs(StageSynth) {
		if u, _ := ws.Manifest.Unit(StageSynth, id); u.Status == workspace.StatusFailed {
			failed = u
		}
	}
	if failed.Error != boom.Error() || failed.Chapter != "ch01" || !strings.HasSuffix(failed.Out, ".wav") {
		t.Errorf("failed unit = %+v", failed)
	}

	before := syn.Calls()
	syn.Fail = nil
	res = synthAll(t, ws, syn)
	if res.Synthesized != 1 || syn.Calls()-before != 1 || !spoke(syn.Texts()[before:], "C") {
		t.Errorf("second run: Result = %+v, texts = %q; want only the failed segment", res, syn.Texts()[before:])
	}
}

func TestSynthEditedParagraphRedoesOnlyItsSegments(t *testing.T) {
	ws := translatedBook(t, twoChapters()...)
	syn := &tts.FakeSynthesizer{}
	synthAll(t, ws, syn)

	// The user fixes one paragraph of the translated chapter 1 by hand.
	path := ws.TranslatedChapterPath(1)
	original := readFile(t, path)
	edited := strings.Replace(original, "Paragraph C opens here.", "Paragraph C opens here, corrected by hand.", 1)
	if edited == original {
		t.Fatal("the test edit did not change the file")
	}
	writeFile(t, path, edited)

	before := syn.Calls()
	res := synthAll(t, ws, syn)
	got := syn.Texts()[before:]
	if len(got) != 1 || !strings.Contains(got[0], "corrected by hand") {
		t.Errorf("after editing one paragraph the engine got %q, want only that paragraph", got)
	}
	if res.Synthesized != 1 {
		t.Errorf("Result = %+v", res)
	}

	// Undoing the edit needs no synthesis: the old segment is in the cache.
	writeFile(t, path, original)
	before = syn.Calls()
	synthAll(t, ws, syn)
	if syn.Calls() != before {
		t.Errorf("undoing the edit called the engine: %q", syn.Texts()[before:])
	}
	if c := ws.Manifest.Counts(StageSynth); c.Total != 12 {
		t.Errorf("Counts = %+v, want 12 units", c)
	}
}

func TestSynthVoiceChangeRedoesEverything(t *testing.T) {
	ws := translatedBook(t, twoChapters()...)
	syn := &tts.FakeSynthesizer{}
	synthAll(t, ws, syn)

	ws.Config.TTS.Args = []string{"--model", "other-voice.onnx", "--output_file", "{out}"}
	res := synthAll(t, ws, syn)
	if res.Synthesized != 12 {
		t.Errorf("Result = %+v, want every segment synthesised with the new voice", res)
	}
	if c := ws.Manifest.Counts(StageSynth); c.Total != 12 {
		t.Errorf("Counts = %+v, want the units of the old voice pruned", c)
	}
}

func TestSynthShorterChapterLeavesNoStaleUnits(t *testing.T) {
	ws := translatedBook(t, twoChapters()...)
	syn := &tts.FakeSynthesizer{}
	synthAll(t, ws, syn)

	// Drop the last two paragraphs of the translated chapter 2.
	path := ws.TranslatedChapterPath(2)
	paragraphs := strings.Split(strings.TrimSpace(readFile(t, path)), "\n\n")
	writeFile(t, path, strings.Join(paragraphs[:len(paragraphs)-2], "\n\n")+"\n")

	res := synthAll(t, ws, syn)
	if res.Segments != 10 || res.Synthesized != 0 {
		t.Errorf("Result = %+v, want 10 segments and nothing to synthesise", res)
	}
	if c := ws.Manifest.Counts(StageSynth); c.Total != 10 {
		t.Errorf("Counts = %+v, want the two stale units pruned", c)
	}
	if refs, _ := LoadSegments(ws, 2); len(refs) != 3 {
		t.Errorf("segments of chapter 2 = %d, want 3", len(refs))
	}
}

func TestSynthLongParagraphIsCutBetweenSentences(t *testing.T) {
	ws := translatedBook(t, chapterText{1, "Chapter 1", paras("A")})
	ws.Config.TTS.SegmentChars = 100 // the minimum; a translated paragraph is longer
	syn := &tts.FakeSynthesizer{}
	synthAll(t, ws, syn)

	refs, err := LoadSegments(ws, 1)
	if err != nil {
		t.Fatal(err)
	}
	var flags []bool
	for _, ref := range refs[1:] {
		flags = append(flags, ref.EndsParagraph)
	}
	if len(flags) < 2 || flags[len(flags)-1] != true || slices.Contains(flags[:len(flags)-1], true) {
		t.Errorf("EndsParagraph of the pieces of one paragraph = %v, want only the last one set", flags)
	}
}

func TestSynthSingleChapter(t *testing.T) {
	ws := translatedBook(t, twoChapters()...)
	syn := &tts.FakeSynthesizer{}

	res, err := Synth(context.Background(), ws, syn, quiet, SynthOptions{Chapter: 2})
	if err != nil {
		t.Fatalf("Synth: %v", err)
	}
	if res != (SynthResult{Chapters: 1, Segments: 5, Synthesized: 5}) {
		t.Errorf("Result = %+v, want only chapter 2", res)
	}
	if spoke(syn.Texts(), "A") {
		t.Error("chapter 1 was synthesised")
	}

	// Working on one chapter must not drop the units of the other.
	synthAll(t, ws, syn)
	if _, err := Synth(context.Background(), ws, syn, quiet, SynthOptions{Chapter: 1}); err != nil {
		t.Fatal(err)
	}
	if c := ws.Manifest.Counts(StageSynth); c.Total != 12 {
		t.Errorf("Counts = %+v, want all 12 units kept", c)
	}

	if _, err := Synth(context.Background(), ws, syn, quiet, SynthOptions{Chapter: 9}); err == nil || !strings.Contains(err.Error(), "главы 9 нет") {
		t.Errorf("unknown chapter: error = %v", err)
	}
}

func TestSynthPreconditions(t *testing.T) {
	t.Run("no engine arguments", func(t *testing.T) {
		ws := translatedBook(t, twoChapters()...)
		ws.Config.TTS.Args = nil
		_, err := Synth(context.Background(), ws, &tts.FakeSynthesizer{}, quiet, SynthOptions{Chapter: AllChapters})
		if err == nil || !strings.Contains(err.Error(), "tts.args") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("chapter is not translated", func(t *testing.T) {
		ws := newBook(t, twoChapters()...)
		_, err := Synth(context.Background(), ws, &tts.FakeSynthesizer{}, quiet, SynthOptions{Chapter: AllChapters})
		if err == nil || !strings.Contains(err.Error(), "translate") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("chapters stage has not run", func(t *testing.T) {
		ws := newBook(t)
		os.Remove(ws.ChaptersJSONPath())
		_, err := Synth(context.Background(), ws, &tts.FakeSynthesizer{}, quiet, SynthOptions{Chapter: AllChapters})
		if err == nil || !strings.Contains(err.Error(), "chapters") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("segments list is missing", func(t *testing.T) {
		ws := translatedBook(t, twoChapters()...)
		if _, err := LoadSegments(ws, 1); err == nil || !strings.Contains(err.Error(), "synth") {
			t.Errorf("error = %v", err)
		}
	})
}

func TestSynthCancellationIsNotAFailure(t *testing.T) {
	ws := translatedBook(t, twoChapters()...)
	ctx, cancel := context.WithCancel(context.Background())
	syn := &tts.FakeSynthesizer{}
	syn.Fail = func(string) error {
		if syn.Calls() >= 3 {
			cancel()
			return context.Canceled
		}
		return nil
	}

	_, err := Synth(ctx, ws, syn, quiet, SynthOptions{Chapter: AllChapters})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if c := ws.Manifest.Counts(StageSynth); c.Failed != 0 {
		t.Errorf("Counts = %+v: an interrupted segment was recorded as failed", c)
	}

	done := ws.Manifest.Counts(StageSynth).Done
	syn.Fail = nil
	res := synthAll(t, ws, syn)
	if res.Synthesized != 12-done {
		t.Errorf("resumed run synthesised %d segments, want %d", res.Synthesized, 12-done)
	}
}
