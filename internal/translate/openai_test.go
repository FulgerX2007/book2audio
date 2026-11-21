package translate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FulgerX2007/book2audio/internal/config"
)

// reply is one canned HTTP response.
type reply struct {
	status int
	body   string
	header map[string]string
}

func ok(content string) reply {
	data, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}}},
	})
	return reply{status: http.StatusOK, body: string(data)}
}

// newServer answers the i-th request with replies[i] (the last one repeats)
// and counts the requests.
func newServer(t *testing.T, replies ...reply) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(calls.Add(1)) - 1
		rep := replies[min(i, len(replies)-1)]
		for k, v := range rep.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rep.status)
		io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// newClient returns an OpenAI translator for the test server that does not
// wait between retries.
func newClient(t *testing.T, baseURL string, maxRetries int) *OpenAI {
	t.Helper()
	cfg := config.Default().Translate
	cfg.BaseURL, cfg.Model, cfg.MaxRetries, cfg.APIKeyEnv = baseURL, "test-model", maxRetries, ""
	o, err := NewOpenAI(cfg)
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	o.backoff = func(int) time.Duration { return time.Millisecond }
	return o
}

var sampleRequest = Request{Text: "The lamp is lit.", SourceLang: "en", TargetLang: "ru"}

func TestOpenAISendsChatCompletionRequest(t *testing.T) {
	var gotPath, gotAuth, gotType string
	var gotBody chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotType = r.Method+" "+r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, ok("Лампа зажжена.").body)
	}))
	defer srv.Close()

	t.Setenv("B2A_TEST_KEY", "secret-key")
	cfg := config.Default().Translate
	cfg.BaseURL, cfg.Model, cfg.APIKeyEnv, cfg.Temperature = srv.URL+"/v1/", "test-model", "B2A_TEST_KEY", 0.3
	o, err := NewOpenAI(cfg)
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}

	got, err := o.Translate(context.Background(), sampleRequest)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got != "Лампа зажжена." {
		t.Errorf("Translate = %q", got)
	}
	if gotPath != "POST /v1/chat/completions" {
		t.Errorf("request = %q, want POST /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer secret-key" || gotType != "application/json" {
		t.Errorf("headers: Authorization = %q, Content-Type = %q", gotAuth, gotType)
	}
	if gotBody.Model != "test-model" || gotBody.Temperature != 0.3 || gotBody.Stream {
		t.Errorf("body = %+v", gotBody)
	}
	if len(gotBody.Messages) != 2 || gotBody.Messages[0].Role != "system" ||
		!strings.Contains(gotBody.Messages[1].Content, "The lamp is lit.") {
		t.Errorf("messages = %+v", gotBody.Messages)
	}
}

func TestOpenAIWithoutKeySendsNoAuthorization(t *testing.T) {
	var gotAuth string
	seen := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, seen = r.Header.Get("Authorization"), true
		io.WriteString(w, ok("ответ").body)
	}))
	defer srv.Close()

	// The variable is named but not set: a local server needs no key.
	cfg := config.Default().Translate
	cfg.BaseURL, cfg.Model, cfg.APIKeyEnv = srv.URL, "m", "B2A_TEST_UNSET_KEY"
	o, _ := NewOpenAI(cfg)
	if _, err := o.Translate(context.Background(), sampleRequest); err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if !seen || gotAuth != "" {
		t.Errorf("Authorization = %q, want none", gotAuth)
	}
}

func TestOpenAIStripsTextWrapper(t *testing.T) {
	srv, _ := newServer(t, ok("\n<text>\nЛампа зажжена.\n</text>\n"))
	got, err := newClient(t, srv.URL, 0).Translate(context.Background(), sampleRequest)
	if err != nil || got != "Лампа зажжена." {
		t.Errorf("Translate = %q, %v", got, err)
	}
}

func TestOpenAIRetriesTransportFailures(t *testing.T) {
	tests := []struct {
		name      string
		replies   []reply
		retries   int
		wantCalls int32
		wantErr   string
	}{
		{"429 then success", []reply{{status: 429, body: "slow down"}, ok("ок")}, 3, 2, ""},
		{"500 and 503 then success", []reply{{status: 500}, {status: 503}, ok("ок")}, 3, 3, ""},
		{"408 then success", []reply{{status: 408}, ok("ок")}, 1, 2, ""},
		{"500 until the retries run out", []reply{{status: 500, body: `{"error":{"message":"overloaded"}}`}}, 2, 3, "после 3 попыток"},
		{"no retries configured", []reply{{status: 502}}, 0, 1, "после 1 попыток"},
		{"400 is not retried", []reply{{status: 400, body: `{"error":{"message":"bad model"}}`}}, 5, 1, "bad model"},
		{"401 is not retried", []reply{{status: 401, body: "unauthorized"}}, 5, 1, "401"},
		{"404 is not retried", []reply{{status: 404, body: "no such route"}}, 5, 1, "no such route"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, calls := newServer(t, tt.replies...)
			got, err := newClient(t, srv.URL, tt.retries).Translate(context.Background(), sampleRequest)
			if tt.wantErr == "" {
				if err != nil || got != "ок" {
					t.Errorf("Translate = %q, %v; want success", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want one containing %q", err, tt.wantErr)
			}
			if calls.Load() != tt.wantCalls {
				t.Errorf("the server got %d requests, want %d", calls.Load(), tt.wantCalls)
			}
		})
	}
}

func TestOpenAIErrorCarriesServerMessage(t *testing.T) {
	srv, _ := newServer(t, reply{status: 500, body: `{"error":{"message":"overloaded"}}`})
	_, err := newClient(t, srv.URL, 1).Translate(context.Background(), sampleRequest)
	if err == nil || !strings.Contains(err.Error(), "overloaded") || !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want the status and the server message", err)
	}

	long, _ := newServer(t, reply{status: 400, body: strings.Repeat("x", 5000)})
	_, err = newClient(t, long.URL, 0).Translate(context.Background(), sampleRequest)
	if err == nil || len(err.Error()) > maxErrorBody+200 {
		t.Errorf("a long error body was not truncated: %d bytes", len(err.Error()))
	}
}

func TestOpenAIHonoursRetryAfter(t *testing.T) {
	srv, calls := newServer(t, reply{status: 429, header: map[string]string{"Retry-After": "1"}}, ok("ок"))
	o := newClient(t, srv.URL, 1)
	o.backoff = func(int) time.Duration {
		t.Error("the backoff was used although the server sent Retry-After")
		return 0
	}

	start := time.Now()
	if _, err := o.Translate(context.Background(), sampleRequest); err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("retried after %v, want about 1s as asked by the server", elapsed)
	}
	if calls.Load() != 2 {
		t.Errorf("requests = %d, want 2", calls.Load())
	}
}

func TestRetryAfter(t *testing.T) {
	for header, want := range map[string]time.Duration{
		"3": 3 * time.Second, " 10 ": 10 * time.Second, "9999": maxRetryAfter,
		"": 0, "0": 0, "-5": 0, "soon": 0, "Wed, 21 Oct 2026 07:28:00 GMT": 0,
	} {
		if got := retryAfter(header); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestOpenAIBadResponses(t *testing.T) {
	tests := []struct {
		name string
		rep  reply
		want error
		text string
	}{
		{"empty content", ok("   "), ErrEmptyResponse, ""},
		{"no choices", reply{status: 200, body: `{"choices":[]}`}, ErrEmptyResponse, ""},
		{"not JSON", reply{status: 200, body: "<html>proxy error</html>"}, nil, "не разобран"},
		{"error object with status 200", reply{status: 200, body: `{"error":{"message":"model is loading"}}`}, nil, "model is loading"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, calls := newServer(t, tt.rep)
			_, err := newClient(t, srv.URL, 3).Translate(context.Background(), sampleRequest)
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
			if tt.text != "" && (err == nil || !strings.Contains(err.Error(), tt.text)) {
				t.Errorf("error = %v, want one containing %q", err, tt.text)
			}
			if calls.Load() != 1 {
				t.Errorf("a bad response was retried: %d requests", calls.Load())
			}
		})
	}
}

func TestOpenAINetworkErrorIsRetried(t *testing.T) {
	srv, _ := newServer(t, ok("ок"))
	url := srv.URL
	srv.Close() // nothing listens there any more

	_, err := newClient(t, url, 2).Translate(context.Background(), sampleRequest)
	if err == nil || !strings.Contains(err.Error(), "после 3 попыток") {
		t.Errorf("error = %v, want a failure after 3 attempts", err)
	}
}

func TestOpenAIContextCancellation(t *testing.T) {
	t.Run("while waiting for the server", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(release)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := newClient(t, srv.URL, 5).Translate(ctx, sampleRequest)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want context.DeadlineExceeded", err)
		}
	})
	t.Run("while waiting to retry", func(t *testing.T) {
		srv, calls := newServer(t, reply{status: 503})
		o := newClient(t, srv.URL, 5)
		o.backoff = func(int) time.Duration { return time.Hour }

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := o.Translate(ctx, sampleRequest)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want context.DeadlineExceeded", err)
		}
		if calls.Load() != 1 {
			t.Errorf("requests = %d, want 1", calls.Load())
		}
	})
}

func TestDefaultBackoffGrowsAndIsCapped(t *testing.T) {
	cfg := config.Default().Translate
	cfg.Model = "m"
	o, _ := NewOpenAI(cfg)
	if o.backoff(1) != time.Second || o.backoff(2) != 2*time.Second || o.backoff(3) != 4*time.Second {
		t.Errorf("backoff = %v, %v, %v; want 1s, 2s, 4s", o.backoff(1), o.backoff(2), o.backoff(3))
	}
	if o.backoff(20) != maxBackoff {
		t.Errorf("backoff(20) = %v, want the cap %v", o.backoff(20), maxBackoff)
	}
}
