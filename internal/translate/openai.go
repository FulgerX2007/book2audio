package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/FulgerX2007/book2audio/internal/config"
)

// ErrEmptyResponse is returned when the model answers with no text.
var ErrEmptyResponse = errors.New("the model returned an empty answer")

const (
	requestTimeout = 5 * time.Minute
	maxBackoff     = 30 * time.Second
	maxRetryAfter  = 60 * time.Second
	maxErrorBody   = 500
)

// OpenAI translates through an OpenAI-compatible chat completions API:
// a local server (Ollama, llama.cpp, vLLM) or a cloud service, chosen by
// translate.base_url.
type OpenAI struct {
	url         string
	model       string
	apiKey      string
	apiKeyEnv   string // name of the variable the key was read from
	temperature float64
	maxRetries  int
	headers     map[string]string // extra request headers from translate.headers
	log         *slog.Logger

	// noTemperature is set once the server rejects the temperature parameter.
	noTemperature atomic.Bool
	client        *http.Client

	// backoff is the pause before retry number attempt (1-based);
	// tests replace it.
	backoff func(attempt int) time.Duration
}

// NewOpenAI returns the translator configured by cfg. The API key is read
// from the environment variable named by cfg.APIKeyEnv; without it requests
// are sent unauthenticated, which is what local servers expect.
func NewOpenAI(cfg config.Translate) (*OpenAI, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("translate.model: is not set")
	}
	var key string
	if cfg.APIKeyEnv != "" {
		key = os.Getenv(cfg.APIKeyEnv)
	}
	return &OpenAI{
		url:         strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions",
		model:       cfg.Model,
		apiKey:      key,
		apiKeyEnv:   cfg.APIKeyEnv,
		temperature: cfg.Temperature,
		maxRetries:  cfg.MaxRetries,
		headers:     cfg.Headers,
		log:         slog.Default(),
		client:      &http.Client{Timeout: requestTimeout},
		backoff: func(attempt int) time.Duration {
			return min(time.Second<<(attempt-1), maxBackoff)
		},
	}, nil
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"` // nil: the model does not accept it
	Stream      bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// retryable is an error worth retrying, with the pause the server asked for.
type retryable struct {
	err   error
	after time.Duration // 0: use the backoff
}

func (e *retryable) Error() string { return e.err.Error() }
func (e *retryable) Unwrap() error { return e.err }

// Translate implements Translator. Only transport failures are retried:
// network errors, timeouts, 408, 429 and 5xx, up to translate.max_retries
// times. Any other failure is returned at once.
func (o *OpenAI) Translate(ctx context.Context, req Request) (string, error) {
	body, err := o.body(req)
	if err != nil {
		return "", err
	}

	for attempt := 0; ; attempt++ {
		text, err := o.send(ctx, body)
		if err == nil {
			return text, nil
		}
		// Some models reject the temperature parameter: drop it for this and
		// every later request instead of failing the whole book.
		if _, rejected := errors.AsType[*temperatureRejected](err); rejected && !o.noTemperature.Swap(true) {
			o.log.Warn("the model does not accept translate.temperature; translating without it")
		}
		if o.noTemperature.Load() && bytes.Contains(body, []byte(`"temperature"`)) {
			if body, err = o.body(req); err != nil {
				return "", err
			}
			attempt--
			continue
		}
		retry, ok := errors.AsType[*retryable](err)
		if !ok || ctx.Err() != nil {
			return "", err
		}
		if attempt >= o.maxRetries {
			return "", fmt.Errorf("translation failed after %d attempts: %w", attempt+1, retry.err)
		}
		pause := retry.after
		if pause == 0 {
			pause = o.backoff(attempt + 1)
		}
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// body encodes the request, with the temperature unless the server has
// rejected that parameter.
func (o *OpenAI) body(req Request) ([]byte, error) {
	chat := chatRequest{Model: o.model, Messages: BuildMessages(req)}
	if !o.noTemperature.Load() {
		chat.Temperature = &o.temperature
	}
	return json.Marshal(chat)
}

// temperatureRejected is a 400 answer that blames the temperature parameter.
type temperatureRejected struct{ err error }

func (e *temperatureRejected) Error() string { return e.err.Error() }
func (e *temperatureRejected) Unwrap() error { return e.err }

// send performs one request.
func (o *OpenAI) send(ctx context.Context, body []byte) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	for name, value := range o.headers {
		httpReq.Header.Set(name, value)
	}

	resp, err := o.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &retryable{err: fmt.Errorf("request to %s: %w", o.url, err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", &retryable{err: fmt.Errorf("reading the response of %s: %w", o.url, err)}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		err := fmt.Errorf("the translation server answered %s: %s", resp.Status, errorText(data))
		switch {
		case resp.StatusCode == http.StatusRequestTimeout,
			resp.StatusCode == http.StatusTooManyRequests,
			resp.StatusCode >= 500:
			return "", &retryable{err: err, after: retryAfter(resp.Header.Get("Retry-After"))}
		case resp.StatusCode == http.StatusBadRequest && bytes.Contains(body, []byte(`"temperature"`)) &&
			strings.Contains(strings.ToLower(errorText(data)), "temperature"):
			return "", &temperatureRejected{err: err}
		case resp.StatusCode == http.StatusUnauthorized && o.apiKey == "" && o.apiKeyEnv != "":
			// The usual cause: the variable is set in the shell but not exported.
			return "", fmt.Errorf("%w (no key was sent: the environment variable %s is empty; set it with: export %s=...)",
				err, o.apiKeyEnv, o.apiKeyEnv)
		}
		return "", err
	}

	var parsed chatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("cannot parse the translation server response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("the translation server reported an error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", ErrEmptyResponse
	}
	text := cleanReply(parsed.Choices[0].Message.Content)
	if text == "" {
		return "", ErrEmptyResponse
	}
	return text, nil
}

// cleanReply removes the <text> wrapper some models copy from the prompt.
func cleanReply(s string) string {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "<text>"); ok {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "</text>"))
	}
	return s
}

// errorText extracts the message of an API error body, or returns the
// start of the body.
func errorText(data []byte) string {
	var parsed chatResponse
	if json.Unmarshal(data, &parsed) == nil && parsed.Error != nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	text := strings.TrimSpace(string(data))
	if len(text) > maxErrorBody {
		text = text[:maxErrorBody] + "…"
	}
	return text
}

// retryAfter parses a Retry-After header given in seconds.
func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds <= 0 {
		return 0
	}
	return min(time.Duration(seconds)*time.Second, maxRetryAfter)
}
