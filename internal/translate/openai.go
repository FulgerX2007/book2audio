package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/FulgerX2007/book2audio/internal/config"
)

// ErrEmptyResponse is returned when the model answers with no text.
var ErrEmptyResponse = errors.New("модель вернула пустой ответ")

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
	temperature float64
	maxRetries  int
	client      *http.Client

	// backoff is the pause before retry number attempt (1-based);
	// tests replace it.
	backoff func(attempt int) time.Duration
}

// NewOpenAI returns the translator configured by cfg. The API key is read
// from the environment variable named by cfg.APIKeyEnv; without it requests
// are sent unauthenticated, which is what local servers expect.
func NewOpenAI(cfg config.Translate) (*OpenAI, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("translate.model: не задано")
	}
	var key string
	if cfg.APIKeyEnv != "" {
		key = os.Getenv(cfg.APIKeyEnv)
	}
	return &OpenAI{
		url:         strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions",
		model:       cfg.Model,
		apiKey:      key,
		temperature: cfg.Temperature,
		maxRetries:  cfg.MaxRetries,
		client:      &http.Client{Timeout: requestTimeout},
		backoff: func(attempt int) time.Duration {
			return min(time.Second<<(attempt-1), maxBackoff)
		},
	}, nil
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
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
	body, err := json.Marshal(chatRequest{
		Model:       o.model,
		Messages:    BuildMessages(req),
		Temperature: o.temperature,
	})
	if err != nil {
		return "", err
	}

	for attempt := 0; ; attempt++ {
		text, err := o.send(ctx, body)
		if err == nil {
			return text, nil
		}
		retry, ok := errors.AsType[*retryable](err)
		if !ok || ctx.Err() != nil {
			return "", err
		}
		if attempt >= o.maxRetries {
			return "", fmt.Errorf("перевод не удался после %d попыток: %w", attempt+1, retry.err)
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

	resp, err := o.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &retryable{err: fmt.Errorf("запрос к %s: %w", o.url, err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", &retryable{err: fmt.Errorf("чтение ответа %s: %w", o.url, err)}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		err := fmt.Errorf("сервер перевода ответил %s: %s", resp.Status, errorText(data))
		switch {
		case resp.StatusCode == http.StatusRequestTimeout,
			resp.StatusCode == http.StatusTooManyRequests,
			resp.StatusCode >= 500:
			return "", &retryable{err: err, after: retryAfter(resp.Header.Get("Retry-After"))}
		}
		return "", err
	}

	var parsed chatResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("ответ сервера перевода не разобран: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("сервер перевода сообщил об ошибке: %s", parsed.Error.Message)
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
