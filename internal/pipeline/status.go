package pipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/FulgerX2007/book2audio/internal/chapters"
	"github.com/FulgerX2007/book2audio/internal/extract"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// StageStatus is the progress of one stage.
type StageStatus struct {
	Name   string
	What   string // what the units are
	Counts workspace.Counts
}

// Failure is a unit that failed.
type Failure struct {
	Stage   string
	Chapter string // chapter of the unit, when known
	Error   string
	// Out is where the result was expected. A file put there by hand is
	// accepted on the next run.
	Out string
}

// ChapterStatus is the progress of one chapter through translation and
// speech.
type ChapterStatus struct {
	N     int
	Title string

	Chunks       int  // chunks of the chapter; 0 while the translate stage has not planned it
	ChunksDone   int  // chunks translated
	ChunksFailed int  // chunks that failed
	Translated   bool // the chapter translation is assembled

	Segments       int  // audio segments; 0 while the synth stage has not planned the chapter
	SegmentsDone   int  // segments synthesised
	SegmentsFailed int  // segments that failed
	Audio          bool // the chapter audio is joined
}

// Status is the state of a book.
type Status struct {
	Dir      string
	Stages   []StageStatus
	Chapters []ChapterStatus // empty until the chapters stage has run
	Failures []Failure
	Edited   []string // files changed by hand; they are not overwritten without --force
}

// statusStages lists what status reports, in pipeline order.
var statusStages = []struct{ name, what string }{
	{extract.Stage, "pages"},
	{chapters.Stage, "chapters.json and chapters"},
	{StageTranslate, "translation chunks"},
	{StageTranslated, "translated chapters"},
	{StageSynth, "speech segments"},
	{StageBuild, "chapter and book audio"},
}

// editableStages are the stages whose results may be edited by hand.
var editableStages = []string{chapters.Stage, StageTranslated}

// ReadStatus collects the state of the book from its manifest.
func ReadStatus(ws *workspace.Workspace) (Status, error) {
	st := Status{Dir: ws.Dir}
	m := ws.Manifest
	for _, s := range statusStages {
		st.Stages = append(st.Stages, StageStatus{Name: s.name, What: s.what, Counts: m.Counts(s.name)})
		for _, id := range m.UnitIDs(s.name) {
			u, _ := m.Unit(s.name, id)
			if u.Status == workspace.StatusFailed {
				st.Failures = append(st.Failures, Failure{
					Stage:   s.name,
					Chapter: u.Chapter,
					Error:   u.Error,
					Out:     filepath.Join(ws.Dir, filepath.FromSlash(u.Out)),
				})
			}
		}
	}
	st.Chapters = chapterStatuses(ws)
	for _, stage := range editableStages {
		for _, id := range m.UnitIDs(stage) {
			edited, err := m.Edited(stage, id)
			if err != nil {
				return st, err
			}
			if edited {
				u, _ := m.Unit(stage, id)
				st.Edited = append(st.Edited, filepath.Join(ws.Dir, filepath.FromSlash(u.Out)))
			}
		}
	}
	return st, nil
}

// Write prints the status as a table followed by the failures and the
// hand-edited files.
func (st Status) Write(w io.Writer) {
	fmt.Fprintf(w, "Book: %s\n\n", st.Dir)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Stage\tUnits\tTotal\tDone\tFailed")
	for _, s := range st.Stages {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\n", s.Name, s.What, s.Counts.Total, s.Counts.Done, s.Counts.Failed)
	}
	tw.Flush()

	if len(st.Chapters) > 0 {
		fmt.Fprintln(w, "\nChapters:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  No\tTitle\tTranslation\tSpeech\tChapter audio")
		for _, ch := range st.Chapters {
			fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\n", ch.N, shorten(ch.Title, 40),
				progress(ch.ChunksDone, ch.Chunks, ch.ChunksFailed, ch.Translated),
				progress(ch.SegmentsDone, ch.Segments, ch.SegmentsFailed, ch.Segments > 0 && ch.SegmentsDone == ch.Segments),
				yesNo(ch.Audio))
		}
		tw.Flush()
	}

	if len(st.Failures) > 0 {
		fmt.Fprintln(w, "\nFailures:")
		for _, f := range st.Failures {
			where := f.Stage
			if f.Chapter != "" {
				where += " " + f.Chapter
			}
			fmt.Fprintf(w, "  %s: %s\n    a result can be supplied by hand at: %s\n", where, f.Error, f.Out)
		}
	}
	if len(st.Edited) > 0 {
		fmt.Fprintln(w, "\nEdited by hand (not overwritten without --force):")
		for _, path := range st.Edited {
			fmt.Fprintf(w, "  %s\n", path)
		}
	}
}

// chapterStatuses reports the progress of every chapter. It only reads the
// manifest and the lists written by the stages, so it also works while
// another process is translating or synthesising the book: then the totals
// of the stage in progress are not known yet and only the finished units
// are counted.
func chapterStatuses(ws *workspace.Workspace) []ChapterStatus {
	starts, err := chapters.Load(ws)
	if err != nil {
		return nil // the chapters stage has not run
	}
	m := ws.Manifest

	// Units of the stage in progress are counted by the chapter recorded
	// in them.
	type counts struct{ done, failed int }
	byChapter := func(stage string) map[string]counts {
		out := make(map[string]counts)
		for _, id := range m.UnitIDs(stage) {
			u, _ := m.Unit(stage, id)
			c := out[u.Chapter]
			switch u.Status {
			case workspace.StatusDone:
				c.done++
			case workspace.StatusFailed:
				c.failed++
			}
			out[u.Chapter] = c
		}
		return out
	}
	translated, spoken := byChapter(StageTranslate), byChapter(StageSynth)
	isDone := func(stage, id string) bool {
		u, ok := m.Unit(stage, id)
		return ok && u.Status == workspace.StatusDone
	}

	out := make([]ChapterStatus, 0, len(starts))
	for _, s := range starts {
		id := chapters.UnitID(s.N)
		ch := ChapterStatus{
			N: s.N, Title: s.Title,
			ChunksDone: translated[id].done, ChunksFailed: translated[id].failed,
			SegmentsDone: spoken[id].done, SegmentsFailed: spoken[id].failed,
			Translated: isDone(StageTranslated, id),
			Audio:      isDone(StageBuild, id),
		}
		// The exact lists exist once a stage has finished the chapter.
		var chunks []chunkRef
		if readJSON(ws.ChunksJSONPath(s.N), &chunks) {
			ch.Chunks, ch.ChunksDone, ch.ChunksFailed = len(chunks), 0, 0
			for _, ref := range chunks {
				u, _ := m.Unit(StageTranslate, ref.Hash)
				switch u.Status {
				case workspace.StatusDone:
					ch.ChunksDone++
				case workspace.StatusFailed:
					ch.ChunksFailed++
				}
			}
		}
		var segments []SegmentRef
		if readJSON(ws.SegmentsJSONPath(s.N), &segments) {
			ch.Segments, ch.SegmentsDone, ch.SegmentsFailed = len(segments), 0, 0
			for _, ref := range segments {
				u, _ := m.Unit(StageSynth, ref.Hash)
				switch u.Status {
				case workspace.StatusDone:
					ch.SegmentsDone++
				case workspace.StatusFailed:
					ch.SegmentsFailed++
				}
			}
		}
		out = append(out, ch)
	}
	return out
}

// readJSON decodes the file into v and reports whether it could.
func readJSON(path string, v any) bool {
	data, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(data, v) == nil
}

// progress renders "done/total", with the failures and a mark when the
// chapter is complete. An unknown total is shown as "?".
func progress(done, total, failed int, complete bool) string {
	if done == 0 && total == 0 && failed == 0 {
		return "—"
	}
	s := fmt.Sprintf("%d/?", done)
	if total > 0 {
		s = fmt.Sprintf("%d/%d", done, total)
	}
	if failed > 0 {
		s += fmt.Sprintf(", failed %d", failed)
	}
	if complete {
		s += " ✓"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "✓"
	}
	return "—"
}

// shorten cuts a title for the table.
func shorten(s string, maxRunes int) string {
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes-1]) + "…"
	}
	return s
}
