package translate

import "context"

// Passthrough is the Translator of a book that is read in its own language
// (source_lang equals target_lang): it returns the text unchanged and calls
// no model.
type Passthrough struct{}

// Translate implements Translator.
func (Passthrough) Translate(ctx context.Context, req Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return req.Text, nil
}
