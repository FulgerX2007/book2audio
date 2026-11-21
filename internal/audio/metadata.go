package audio

import (
	"fmt"
	"strings"
	"time"
)

// Tags are the book-level tags of an audiobook.
type Tags struct {
	Title  string
	Author string
}

// Chapter is a chapter mark of an audiobook.
type Chapter struct {
	Title string
	Start time.Duration
	End   time.Duration
}

// Chapters lays chapters of the given lengths one after another, starting
// at zero. Times are computed from frame counts, so marks do not drift.
func Chapters(titles []string, frames []int, sampleRate int) []Chapter {
	out := make([]Chapter, len(titles))
	total := 0
	for i, title := range titles {
		start := FramesDuration(total, sampleRate)
		total += frames[i]
		out[i] = Chapter{Title: title, Start: start, End: FramesDuration(total, sampleRate)}
	}
	return out
}

var metadataEscaper = strings.NewReplacer(
	`\`, `\\`, "=", `\=`, ";", `\;`, "#", `\#`, "\n", "\\\n",
)

// FFMetadata renders the tags and chapter marks in the metadata file format
// of ffmpeg (";FFMETADATA1"). Chapter times are in milliseconds.
func FFMetadata(tags Tags, chapters []Chapter) string {
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")
	if tags.Title != "" {
		// "album" makes players group the chapters of an audiobook.
		fmt.Fprintf(&b, "title=%s\nalbum=%s\n", metadataEscaper.Replace(tags.Title), metadataEscaper.Replace(tags.Title))
	}
	if tags.Author != "" {
		fmt.Fprintf(&b, "artist=%s\n", metadataEscaper.Replace(tags.Author))
	}
	b.WriteString("genre=Audiobook\n")
	for _, ch := range chapters {
		fmt.Fprintf(&b, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n",
			ch.Start.Milliseconds(), ch.End.Milliseconds(), metadataEscaper.Replace(ch.Title))
	}
	return b.String()
}
