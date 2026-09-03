package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"roleplay/database"
	"roleplay/model"
)

// fakeClient is a scriptable ChatClient that returns canned streaming deltas
// and facts without touching the network.
type fakeClient struct {
	hasKey    bool
	deltas    []string
	streamErr error
	facts     []string
	factErr   bool

	// if set, facts extraction blocks/returns cancelled when ctx is done —
	// mimics a context-aware provider so cancellation bugs surface.
	respectCtxCancel bool

	mu          sync.Mutex
	streamCalls int
	messages    []model.Message
	temp        float64
	factCtx     context.Context
}

func (f *fakeClient) HasAPIKey() bool { return f.hasKey }

func (f *fakeClient) Stream(_ context.Context, messages []model.Message, temperature float64, onDelta func(string)) error {
	f.mu.Lock()
	f.streamCalls++
	f.messages = messages
	f.temp = temperature
	f.mu.Unlock()
	if f.streamErr != nil {
		return f.streamErr
	}
	for _, d := range f.deltas {
		onDelta(d)
	}
	return nil
}

func (f *fakeClient) ExtractFacts(ctx context.Context, _ string) []string {
	if f.respectCtxCancel {
		// Return facts only if the context is actually live; if it's already
		// cancelled (the request-scoped bug), behave like a real provider and
		// return nothing.
		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
	if f.factErr {
		return nil
	}
	f.mu.Lock()
	f.factCtx = ctx
	f.mu.Unlock()
	return f.facts
}

func newTestHandler(f *fakeClient) *Handler {
	return &Handler{Store: database.NewStore(""), Client: f}
}

func doChat(h *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	return rec
}

func TestChatStreamsReply(t *testing.T) {
	f := &fakeClient{hasKey: true, deltas: []string{"Hi ", "there"}}
	h := newTestHandler(f)

	rec := doChat(h, `{"session":"s1","text":"hello","character":"luna"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected SSE content type, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"delta":"Hi "`) || !strings.Contains(body, `"delta":"there"`) {
		t.Fatalf("expected streamed deltas in body, got %q", body)
	}
	if !strings.Contains(body, `"done":true`) {
		t.Fatalf("expected done marker, got %q", body)
	}

	// reply must be persisted as an assistant turn
	turns := h.Store.Recent("s1", 10)
	if len(turns) != 2 || turns[1].Role != "assistant" || turns[1].Content != "Hi there" {
		t.Fatalf("expected assistant reply persisted, got %+v", turns)
	}
	// character should be resolved + persisted
	if got := h.Store.Character("s1"); !strings.EqualFold(got, "luna") {
		t.Fatalf("expected luna character, got %q", got)
	}
}

func TestChatUsesFactsAsContext(t *testing.T) {
	f := &fakeClient{hasKey: true, deltas: []string{"ok"}}
	h := newTestHandler(f)
	h.Store.Remember("s1", "loves the sea")

	doChat(h, `{"session":"s1","text":"tell me more","character":"max"}`)

	f.mu.Lock()
	msgs := f.messages
	f.mu.Unlock()
	// system persona + system facts + user message = 3 messages
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (persona, facts, user), got %d", len(msgs))
	}
	if !strings.Contains(msgs[1].Content, "loves the sea") {
		t.Fatalf("expected facts injected as system context, got %+v", msgs)
	}
}

func TestChatPersistsFactsAsync(t *testing.T) {
	f := &fakeClient{hasKey: true, deltas: []string{"hi"}, facts: []string{"lives in mumbai"}}
	h := newTestHandler(f)

	doChat(h, `{"session":"s1","text":"I live in Mumbai","character":"araav"}`)
	// facts are learned in a goroutine — poll briefly for it to land
	var facts []string
	for i := 0; i < 50; i++ {
		facts = h.Store.Facts("s1")
		if len(facts) > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(facts) != 1 || facts[0] != "lives in mumbai" {
		t.Fatalf("expected fact learned, got %v", facts)
	}
}

func TestChatFactsSurviveRequestContextCancellation(t *testing.T) {
	// Regression: the facts goroutine must NOT be tied to the request-scoped
	// context, which Go's http server cancels the moment the handler returns
	// (ServeHTTP). A context-aware provider would refuse that cancelled call,
	// silently dropping facts forever. We reproduce production exactly: a real
	// request context that we cancel after the handler completes.
	f := &fakeClient{hasKey: true, deltas: []string{"hi"}, facts: []string{"remembers tea"},
		respectCtxCancel: true}
	h := newTestHandler(f)

	// Build the request with a cancellable context, like a real inbound request.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"session":"s1","text":"I love tea","character":"luna"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(reqCtx)
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Simulate the request ending: cancel the request context exactly as the
	// http server does when ServeHTTP returns.
	cancelReq()

	// The detached facts goroutine must still persist facts despite that.
	var facts []string
	for i := 0; i < 80; i++ {
		facts = h.Store.Facts("s1")
		if len(facts) > 0 {
			break
		}
		time.Sleep(3 * time.Millisecond)
	}
	if len(facts) != 1 || facts[0] != "remembers tea" {
		t.Fatalf("expected facts persisted despite cancelled request ctx, got %v", facts)
	}
}

func TestChatRejectEmptyBody(t *testing.T) {
	f := &fakeClient{hasKey: true}
	h := newTestHandler(f)
	rec := doChat(h, `{"session":"","text":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty session+text, got %d", rec.Code)
	}
}

func TestChatRejectEmptySession(t *testing.T) {
	f := &fakeClient{hasKey: true}
	h := newTestHandler(f)
	rec := doChat(h, `{"session":"  ","text":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty session, got %d", rec.Code)
	}
}

func TestChatRejectLongSession(t *testing.T) {
	f := &fakeClient{hasKey: true}
	h := newTestHandler(f)
	rec := doChat(h, `{"session":"`+strings.Repeat("x", maxSessionLen+1)+`","text":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for over-long session, got %d", rec.Code)
	}
}

func TestChatRejectLongText(t *testing.T) {
	f := &fakeClient{hasKey: true}
	h := newTestHandler(f)
	rec := doChat(h, `{"session":"s1","text":"`+strings.Repeat("y", maxTextLen+1)+`"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for over-long text, got %d", rec.Code)
	}
}

func TestChatRejectBadJSON(t *testing.T) {
	f := &fakeClient{hasKey: true}
	h := newTestHandler(f)
	rec := doChat(h, `{not-json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad json, got %d", rec.Code)
	}
}

func TestChatRejectNonPost(t *testing.T) {
	f := &fakeClient{hasKey: true}
	h := newTestHandler(f)
	req := httptest.NewRequest(http.MethodGet, "/api/chat", nil)
	rec := httptest.NewRecorder()
	h.Chat(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET, got %d", rec.Code)
	}
}

func TestChatStreamErrorPropagated(t *testing.T) {
	f := &fakeClient{hasKey: true, streamErr: context.DeadlineExceeded}
	h := newTestHandler(f)
	rec := doChat(h, `{"session":"s1","text":"hi","character":"luna"}`)
	if !strings.Contains(rec.Body.String(), `"error":`) {
		t.Fatalf("expected error event on stream failure, got %q", rec.Body.String())
	}
}

func TestChatNoKeyReturns503(t *testing.T) {
	f := &fakeClient{hasKey: false}
	h := newTestHandler(f)
	rec := doChat(h, `{"session":"s1","text":"hi","character":"luna"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when provider not configured, got %d", rec.Code)
	}
}

func TestCharactersReturnsCatalog(t *testing.T) {
	f := &fakeClient{hasKey: true}
	h := newTestHandler(f)
	req := httptest.NewRequest(http.MethodGet, "/api/characters", nil)
	rec := httptest.NewRecorder()
	h.Characters(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out struct {
		Characters []map[string]any `json:"characters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Characters) == 0 {
		t.Fatal("expected at least one character")
	}
	// Public must not leak backstory
	if _, has := out.Characters[0]["backstory"]; has {
		t.Fatal("characters API must not leak backstory")
	}
}

func TestHistoryReturnsSession(t *testing.T) {
	f := &fakeClient{hasKey: true}
	h := newTestHandler(f)
	h.Store.SetCharacter("s1", "max")
	h.Store.Add("s1", "user", "yo")

	req := httptest.NewRequest(http.MethodGet, "/api/history?session=s1", nil)
	rec := httptest.NewRecorder()
	h.History(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out struct {
		Character string `json:"character"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Character != "max" {
		t.Fatalf("expected character max, got %q", out.Character)
	}
	if len(out.Messages) != 1 || out.Messages[0].Content != "yo" {
		t.Fatalf("unexpected history messages: %+v", out.Messages)
	}
}

func TestHistoryRejectMissingSession(t *testing.T) {
	f := &fakeClient{hasKey: true}
	h := newTestHandler(f)
	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec := httptest.NewRecorder()
	h.History(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing session, got %d", rec.Code)
	}
}