package tts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/FulgerX2007/book2audio/internal/audio"
	"github.com/FulgerX2007/book2audio/internal/config"
	"github.com/FulgerX2007/book2audio/internal/execx"
)

// Piper synthesises speech by running the Piper command once per segment.
// The command and its arguments come from the config (tts.command,
// tts.args), because the command line differs between Piper releases; the
// argument that contains "{out}" receives the path of the WAV file.
type Piper struct {
	command string
	args    []string
	runner  execx.Runner
}

// NewPiper returns a Piper synthesizer configured by cfg.
func NewPiper(cfg config.TTS, runner execx.Runner) (*Piper, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("tts.command: не задано")
	}
	hasOut := false
	for _, arg := range cfg.Args {
		hasOut = hasOut || strings.Contains(arg, config.OutPlaceholder)
	}
	if !hasOut {
		return nil, fmt.Errorf("tts.args: нет подстановки %s для пути выходного файла", config.OutPlaceholder)
	}
	return &Piper{command: cfg.Command, args: cfg.Args, runner: runner}, nil
}

// Synthesize implements Synthesizer. The engine writes to a temporary file
// next to outPath; the file is checked to be non-empty PCM WAV and only
// then renamed, so an interrupted run never leaves a partial result.
func (p *Piper) Synthesize(ctx context.Context, text, outPath string) error {
	// Piper reads one utterance per line: the text must be a single line.
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return errors.New("пустой текст для озвучки")
	}

	tmp := outPath + ".part"
	defer os.Remove(tmp) // no-op after a successful rename
	args := make([]string, len(p.args))
	for i, arg := range p.args {
		args[i] = strings.ReplaceAll(arg, config.OutPlaceholder, tmp)
	}

	if _, err := p.runner.Run(ctx, execx.Cmd{
		Name:  p.command,
		Args:  args,
		Stdin: strings.NewReader(text + "\n"),
	}); err != nil {
		if errors.Is(err, execx.ErrToolNotFound) {
			return fmt.Errorf("%w (команда из tts.command)", err)
		}
		return fmt.Errorf("%w (проверьте путь к модели голоса в tts.args)", err)
	}

	clip, err := audio.ReadWAV(tmp)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s не создал выходной файл: проверьте tts.args", p.command)
		}
		return fmt.Errorf("результат %s: %w", p.command, err)
	}
	if clip.Frames() == 0 {
		return fmt.Errorf("%s создал пустой аудиофайл", p.command)
	}
	return os.Rename(tmp, outPath)
}
