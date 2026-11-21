package tts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/FulgerX2007/book2audio/internal/audio"
	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
)

// Silero synthesises speech with Silero TTS through scripts/silero_tts.py.
// The script loads the model once and then works through all the segments
// it is given, so the engine is started once per batch, not per segment.
//
// Protocol: one line "<out path><TAB><text>" per segment on standard input;
// one line "ok" or "err <message>" per segment on standard output, in the
// same order. The script writes each WAV to a temporary file and renames it.
type Silero struct {
	command string
	args    []string
	runner  execx.Runner
}

// NewSilero returns a Silero synthesizer configured by cfg: tts.command is
// the script (or the interpreter), tts.args its arguments.
func NewSilero(cfg config.TTS, runner execx.Runner) (*Silero, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("tts.command: не задано")
	}
	return &Silero{command: cfg.Command, args: cfg.Args, runner: runner}, nil
}

// Synthesize implements Synthesizer with a batch of one segment.
func (s *Silero) Synthesize(ctx context.Context, text, outPath string) error {
	errs, err := s.SynthesizeBatch(ctx, []Item{{Text: text, OutPath: outPath}})
	if err != nil {
		return err
	}
	return errs[0]
}

// SynthesizeBatch implements BatchSynthesizer.
func (s *Silero) SynthesizeBatch(ctx context.Context, items []Item) ([]error, error) {
	if len(items) == 0 {
		return nil, nil
	}
	var input bytes.Buffer
	for _, item := range items {
		// The protocol is line-based and tab-separated.
		text := strings.Join(strings.Fields(item.Text), " ")
		if text == "" {
			text = " " // keep the line; the script reports the empty text
		}
		fmt.Fprintf(&input, "%s\t%s\n", item.OutPath, text)
	}

	out, err := s.runner.Run(ctx, execx.Cmd{Name: s.command, Args: s.args, Stdin: &input})
	if err != nil {
		if errors.Is(err, execx.ErrToolNotFound) {
			return nil, fmt.Errorf("%w (команда из tts.command)", err)
		}
		return nil, fmt.Errorf("движок озвучки Silero: %w", err)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n"), "\n")
	if len(lines) != len(items) {
		return nil, fmt.Errorf("движок озвучки Silero ответил строк: %d, а сегментов %d", len(lines), len(items))
	}

	errs := make([]error, len(items))
	for i, line := range lines {
		status, msg, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch status {
		case "ok":
			errs[i] = checkWAV(items[i].OutPath)
		case "err":
			errs[i] = fmt.Errorf("Silero: %s", msg)
		default:
			errs[i] = fmt.Errorf("Silero: непонятный ответ %q", line)
		}
	}
	return errs, nil
}

// checkWAV verifies that the engine left a non-empty PCM WAV file.
func checkWAV(path string) error {
	clip, err := audio.ReadWAV(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("движок озвучки сообщил об успехе, но не создал файл")
		}
		return fmt.Errorf("результат движка озвучки: %w", err)
	}
	if clip.Frames() == 0 {
		return errors.New("движок озвучки создал пустой аудиофайл")
	}
	return nil
}
