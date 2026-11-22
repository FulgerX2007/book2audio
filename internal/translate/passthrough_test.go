package translate

import (
	"context"
	"errors"
	"testing"
)

func TestPassthrough(t *testing.T) {
	got, err := Passthrough{}.Translate(context.Background(), Request{Text: "Same text.", SourceLang: "en", TargetLang: "en"})
	if err != nil || got != "Same text." {
		t.Errorf("Translate = %q, %v; want the text unchanged", got, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Passthrough{}).Translate(ctx, Request{Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Translate with a cancelled context = %v, want context.Canceled", err)
	}
}
