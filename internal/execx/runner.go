// Package execx runs the external tools the pipeline depends on (poppler,
// tesseract, ffmpeg, the speech engine) behind an interface that tests can
// replace with a fake.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Cmd describes one invocation of an external tool.
type Cmd struct {
	Name  string
	Args  []string
	Env   []string  // "KEY=value" pairs added to the environment of the process
	Stdin io.Reader // nil: no input
}

// String returns the command line for logs and error messages.
func (c Cmd) String() string {
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

// Runner runs external commands.
type Runner interface {
	// Run executes cmd and returns its standard output. A non-zero exit
	// status is an error that carries the standard error output.
	Run(ctx context.Context, cmd Cmd) (stdout []byte, err error)
}

// ErrToolNotFound is wrapped by the error returned when an external tool is
// not installed.
var ErrToolNotFound = errors.New("tool not found")

// installHints tells which package provides a tool.
var installHints = map[string]string{
	"pdfinfo":   "the poppler-utils package",
	"pdftotext": "the poppler-utils package",
	"pdftoppm":  "the poppler-utils package",
	"tesseract": "the tesseract-ocr package and language data",
	"ffmpeg":    "the ffmpeg package",
	"ffprobe":   "the ffmpeg package",
	"piper":     "Piper and a voice model",
}

// Check reports whether the tool can be found in PATH. The error wraps
// ErrToolNotFound and says what to install.
func Check(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		if hint, ok := installHints[name]; ok {
			return fmt.Errorf("%w: %s, install %s", ErrToolNotFound, name, hint)
		}
		return fmt.Errorf("%w: %s, install it or give its full path", ErrToolNotFound, name)
	}
	return nil
}

// maxStderr limits how much of a tool's error output goes into an error.
const maxStderr = 2000

// OSRunner runs commands as real processes.
type OSRunner struct{}

// Run implements Runner.
func (OSRunner) Run(ctx context.Context, cmd Cmd) ([]byte, error) {
	if err := Check(cmd.Name); err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	if len(cmd.Env) > 0 {
		c.Env = append(os.Environ(), cmd.Env...)
	}
	c.Stdin = cmd.Stdin
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr

	if err := c.Run(); err != nil {
		// A cancelled context kills the process; report the cancellation,
		// not the resulting "signal: killed".
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%s: %w", cmd.Name, ctxErr)
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > maxStderr {
			msg = "…" + msg[len(msg)-maxStderr:]
		}
		if msg == "" {
			return nil, fmt.Errorf("%s: %w", cmd.Name, err)
		}
		return nil, fmt.Errorf("%s: %w: %s", cmd.Name, err, msg)
	}
	return stdout.Bytes(), nil
}
