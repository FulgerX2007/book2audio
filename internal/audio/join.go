package audio

import (
	"errors"
	"fmt"
	"time"
)

// Join concatenates clips into one. After clip i a pause of silence is
// inserted when pauseAfter[i] is set, except after the last clip. All clips
// must have the same format: mixing sample rates would change the speed of
// the speech.
func Join(clips []Clip, pauseAfter []bool, pause time.Duration) (Clip, error) {
	if len(clips) == 0 {
		return Clip{}, errors.New("нет аудио для склейки")
	}
	if len(pauseAfter) != len(clips) {
		return Clip{}, fmt.Errorf("пауз задано %d, а фрагментов %d", len(pauseAfter), len(clips))
	}
	format := clips[0].Format
	silence := Silence(format, pause)

	size := 0
	for i, c := range clips {
		if c.Format != format {
			return Clip{}, fmt.Errorf("фрагмент %d имеет другой формат (%s), чем первый (%s): все сегменты должны быть озвучены одним голосом",
				i+1, c.Format, format)
		}
		size += len(c.Data) + len(silence)
	}

	out := Clip{Format: format, Data: make([]byte, 0, size)}
	for i, c := range clips {
		out.Data = append(out.Data, c.Data...)
		if pauseAfter[i] && i < len(clips)-1 {
			out.Data = append(out.Data, silence...)
		}
	}
	return out, nil
}
