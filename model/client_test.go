package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTransport returns canned responses, recording requests for assertions.
type fakeTransport struct {
	mu       sync.Mutex
	status   int
	body     string
	calls    int
	lastPath string
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	f.lastPath = req.URL.Path
	body := f.body
	status := f.status
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprint(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func newTestClient(rt http.RoundTripper) *Client {
	c := NewClient(Config{APIKey: "test-key", BaseURL: "https://provider.test/v1", Model: "m1"})
	c.SetHTTPClient(&http.Client{Transport: rt})
	return c
}

func TestHasAPIKey(t *testing.T) {
	yes := NewClient(Config{APIKey: "k"})
	if !yes.HasAPIKey() {
		t.Fatal("expected HasAPIKey true with key set")
	}
	no := NewClient(Config{})
	if no.HasAPIKey() {
		t.Fatal("expected HasAPIKey false with no key")
	}
}

func TestStreamParsesSSEDeltas(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		`data: {"choices":[{"delta":{"content":"lo "}}]}`,
		`data: {"choices":[{"delta":{"content":"world"}}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	ft := &fakeTransport{body: body}
	c := newTestClient(ft)

	var got strings.Builder
	err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}, 0.9, func(d string) { got.WriteString(d) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got.String() != "Hello world" {
		t.Fatalf("expected 'Hello world', got %q", got.String())
	}
	if !strings.HasSuffix(ft.lastPath, "/chat/completions") {
		t.Fatalf("expected /chat/completions path, got %q", ft.lastPath)
	}
}

func TestStreamNon200ReturnsError(t *testing.T) {
	ft := &fakeTransport{status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`}
	c := newTestClient(ft)
	err := c.Stream(context.Background(), []Message{{Role: "user", Content: "x"}}, 0.9, func(string) {})
	if err == nil {
		t.Fatal("expected error on non-200")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("expected status in error, got %v", err)
	}
}

func TestStreamNoKeyReturnsError(t *testing.T) {
	// fakeTransport should never be hit because the key guard fails first.
	ft := &fakeTransport{body: `data: [DONE]`}
	c := NewClient(Config{BaseURL: "https://x", Model: "m"}) // no key
	c.SetHTTPClient(&http.Client{Transport: ft})
	err := c.Stream(context.Background(), []Message{}, 0.9, func(string) {})
	if err == nil {
		t.Fatal("expected error when no API key configured")
	}
	if ft.calls != 0 {
		t.Fatalf("expected no provider call, got %d", ft.calls)
	}
}

func TestCompleteParsesChoice(t *testing.T) {
	resp, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "moonlight"}}},
	})
	ft := &fakeTransport{body: string(resp)}
	c := newTestClient(ft)

	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "q"}}, 0.9, 100)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "moonlight" {
		t.Fatalf("expected 'moonlight', got %q", got)
	}
}

func TestCompleteEmptyChoices(t *testing.T) {
	ft := &fakeTransport{body: `{"choices":[]}`}
	c := newTestClient(ft)
	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "q"}}, 0.9, 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string for no choices, got %q", got)
	}
}

func TestExtractFactsParsesLines(t *testing.T) {
	resp, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "- loves the sea\n- is a night owl\n- ignored-fact"}}},
	})
	c := newTestClient(&fakeTransport{body: string(resp)})

	facts := c.ExtractFacts(context.Background(), "I love the sea and stay up late")
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts (cap), got %v", facts)
	}
	if facts[0] != "loves the sea" {
		t.Fatalf("expected trimmed bullet fact, got %q", facts[0])
	}
}

func TestExtractFactsProviderErrorYieldsNil(t *testing.T) {
	c := newTestClient(&fakeTransport{status: http.StatusBadGateway, body: "boom"})
	if facts := c.ExtractFacts(context.Background(), "anything"); facts != nil {
		t.Fatalf("expected nil facts on provider error, got %v", facts)
	}
}

func TestExtractFactsEmptyMessage(t *testing.T) {
	c := newTestClient(&fakeTransport{body: "unused"})
	if facts := c.ExtractFacts(context.Background(), "  "); facts != nil {
		t.Fatalf("expected nil facts for empty message, got %v", facts)
	}
}

func TestReadSSEIgnoresMalformedLines(t *testing.T) {
	body := strings.Join([]string{
		`data: not-json`,
		`data: {}`,
		`event: message`,
		`data: {"choices":[{"delta":{"content":"ok"}}]}`,
		"",
	}, "\n\n")
	var got strings.Builder
	err := readSSE(bytes.NewReader([]byte(strings.ReplaceAll(body, "\n\n", "\n")+ "\n\n")), func(d string) { got.WriteString(d) })
	if err != nil {
		t.Fatalf("readSSE: %v", err)
	}
	if got.String() != "ok" {
		t.Fatalf("expected only the valid delta, got %q", got.String())
	}
}

func TestConfigFromEnvDefaultsResolvedByNewClient(t *testing.T) {
	// ConfigFromEnv reads real env; here we just assert NewClient resolves
	// defaults when fields are empty (no key is fine for a HasAPIKey=false).
	c := NewClient(Config{BaseURL: "", Model: ""})
	if c.Model() != DefaultModel {
		t.Fatalf("expected default model, got %q", c.Model())
	}
	if c.baseURL != strings.TrimRight(DefaultBaseURL, "/") {
		t.Fatalf("expected default base URL, got %q", c.baseURL)
	}
}

func TestClientPropagatesContextCancellation(t *testing.T) {
	// Production relies on cancelling the request context when the caller's
	// context ends (e.g. client disconnects). A context-aware transport must
	// observe the cancellation and return promptly.
	blocker := make(chan struct{})
	var transportReturned atomic.Bool
	slow := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		defer transportReturned.Store(true)
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-blocker:
			return nil, nil
		}
	})
	c := newTestClient(slow)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Complete(ctx, []Message{{Role: "user", Content: "x"}}, 0.9, 10)
	}()

	// Give the transport a moment to start, then cancel the caller context.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		if !transportReturned.Load() {
			t.Fatal("transport did not observe the cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("Complete did not return after context cancellation")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }