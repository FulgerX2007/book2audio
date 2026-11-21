package execx

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// Call is one command recorded by FakeRunner.
type Call struct {
	Cmd
	Input []byte // everything read from Cmd.Stdin
}

// Handler produces the result of a faked command. It may have side effects,
// such as writing the file the real tool would write.
type Handler func(call Call) (stdout []byte, err error)

// FakeRunner is a Runner for tests: it records every command and answers
// with canned results instead of starting processes. It is safe for
// concurrent use.
type FakeRunner struct {
	mu       sync.Mutex
	handlers map[string]Handler
	calls    []Call
}

// NewFakeRunner returns a FakeRunner without any known commands.
func NewFakeRunner() *FakeRunner {
	return &FakeRunner{handlers: make(map[string]Handler)}
}

// Handle makes commands called name answer with h.
func (f *FakeRunner) Handle(name string, h Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[name] = h
}

// Stub makes commands called name return stdout and err.
func (f *FakeRunner) Stub(name string, stdout []byte, err error) {
	f.Handle(name, func(Call) ([]byte, error) { return stdout, err })
}

// Run implements Runner. A command without a handler is an error wrapping
// ErrToolNotFound, like a tool that is not installed.
func (f *FakeRunner) Run(ctx context.Context, cmd Cmd) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", cmd.Name, err)
	}
	call := Call{Cmd: cmd}
	if cmd.Stdin != nil {
		input, err := io.ReadAll(cmd.Stdin)
		if err != nil {
			return nil, fmt.Errorf("%s: reading stdin: %w", cmd.Name, err)
		}
		call.Input = input
	}

	f.mu.Lock()
	f.calls = append(f.calls, call)
	h := f.handlers[cmd.Name]
	f.mu.Unlock()

	if h == nil {
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, cmd.Name)
	}
	return h(call)
}

// Calls returns the recorded commands in call order.
func (f *FakeRunner) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// CallsTo returns the recorded commands called name.
func (f *FakeRunner) CallsTo(name string) []Call {
	var out []Call
	for _, c := range f.Calls() {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}
