package translate

import (
	"context"
	"strings"
	"sync"
)

// FakePrefix marks every paragraph produced by FakeTranslator.
const FakePrefix = "[ru] "

// FakeTranslator is a Translator for tests. It "translates" by prefixing
// every paragraph with FakePrefix (a heading keeps its "# "), so the result
// has the same paragraphs and a comparable length. It records every request
// and is safe for concurrent use.
type FakeTranslator struct {
	// Fail, when set, is called before translating; a non-nil error is
	// returned to the caller.
	Fail func(req Request) error
	// Reply, when set, replaces the default translation.
	Reply func(req Request) string

	mu       sync.Mutex
	requests []Request
}

// Translate implements Translator.
func (f *FakeTranslator) Translate(ctx context.Context, req Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	if f.Fail != nil {
		if err := f.Fail(req); err != nil {
			return "", err
		}
	}
	if f.Reply != nil {
		return f.Reply(req), nil
	}
	return FakeTranslation(req.Text), nil
}

// FakeTranslation is what FakeTranslator returns for text by default.
func FakeTranslation(text string) string {
	paragraphs := strings.Split(text, "\n\n")
	for i, p := range paragraphs {
		if rest, ok := strings.CutPrefix(p, "# "); ok {
			paragraphs[i] = "# " + FakePrefix + rest
		} else {
			paragraphs[i] = FakePrefix + p
		}
	}
	return strings.Join(paragraphs, "\n\n")
}

// Requests returns the recorded requests in call order.
func (f *FakeTranslator) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// Calls returns the number of Translate calls.
func (f *FakeTranslator) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}
