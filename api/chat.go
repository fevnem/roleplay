// Package api exposes the HTTP endpoints for roleplay.
//
// Dependencies are explicit and injectable via the Handler struct (rather than
// package globals), so handlers can be unit-tested with a real in-memory Store
// and a fake model.Client.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"roleplay/contexts"
	"roleplay/database"
	"roleplay/model"
)

// Input validation limits. Session ids come from the client, so bound them to
// keep the in-memory store from being abused by arbitrarily long keys.
const (
	maxSessionLen = 64
	maxTextLen    = 4000
)

// ChatClient is the subset of the model provider the API needs. The concrete
// *model.Client satisfies it; tests inject a fake so handlers run without a
// network call.
type ChatClient interface {
	HasAPIKey() bool
	Stream(ctx context.Context, messages []model.Message, temperature float64, onDelta func(string)) error
	ExtractFacts(ctx context.Context, userMsg string) []string
}

// Handler wires the store, model client, and logger into the HTTP handlers.
// main constructs it; tests construct it with fakes.
type Handler struct {
	Store  *database.Store
	Client ChatClient
	Log    *slog.Logger
}

func (h *Handler) logger() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

type chatIn struct {
	Session   string `json:"session"`   // anonymous client id
	Text      string `json:"text"`
	Character string `json:"character"` // optional persona key
}

// resolveCharacter picks the persona for this session (persisted on it).
func (h *Handler) resolveCharacter(id, requested string) contexts.Character {
	if requested != "" {
		if c, ok := contexts.Get(requested); ok {
			h.Store.SetCharacter(id, strings.ToLower(strings.TrimSpace(c.Name)))
		}
	}
	name := h.Store.Character(id)
	if name == "" {
		if d, ok := contexts.Default(); ok {
			h.Store.SetCharacter(id, strings.ToLower(strings.TrimSpace(d.Name)))
			name = strings.ToLower(strings.TrimSpace(d.Name))
		}
	}
	if c, ok := contexts.Get(name); ok {
		return c
	}
	c, _ := contexts.Default()
	return c
}

func (h *Handler) sessionCharacter(id string) string {
	if n := h.Store.Character(id); n != "" {
		return n
	}
	if d, ok := contexts.Default(); ok {
		return strings.ToLower(strings.TrimSpace(d.Name))
	}
	return ""
}

// Chat handles POST /api/chat and streams the reply back over SSE.
func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	var in chatIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	in.Session = strings.TrimSpace(in.Session)
	in.Text = strings.TrimSpace(in.Text)
	if in.Session == "" || in.Text == "" {
		http.Error(w, "session and text are required", http.StatusBadRequest)
		return
	}
	if len(in.Session) > maxSessionLen {
		http.Error(w, "session id too long", http.StatusBadRequest)
		return
	}
	if len(in.Text) > maxTextLen {
		http.Error(w, "message too long", http.StatusRequestEntityTooLarge)
		return
	}

	h.Store.Add(in.Session, "user", in.Text)
	char := h.resolveCharacter(in.Session, in.Character)

	// context = persona + remembered facts + recent turns + the new message
	messages := []model.Message{{Role: "system", Content: char.SystemPrompt()}}
	if facts := h.Store.Facts(in.Session); len(facts) > 0 {
		messages = append(messages, model.Message{
			Role:    "system",
			Content: "Things you personally remember about this user:\n- " + strings.Join(facts, "\n- "),
		})
	}
	messages = append(messages, h.Store.Recent(in.Session, 10)...)

	if !h.Client.HasAPIKey() {
		http.Error(w, "provider not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	var assistant strings.Builder
	// Stream into a client-disconnect-aware context so the provider call is
	// cancelled when the user navigates away instead of running to completion.
	err := h.Client.Stream(r.Context(), messages, char.Temperature, func(delta string) {
		assistant.WriteString(delta)
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]string{"delta": delta}))
		flusher.Flush()
	})
	if err != nil {
		h.logger().Warn("stream failed", "error", err, "session", in.Session)
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]string{"error": err.Error()}))
		flusher.Flush()
		return
	}

	h.Store.Add(in.Session, "assistant", assistant.String())
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"done": true, "character": h.sessionCharacter(in.Session)}))
	flusher.Flush()

	// learn facts about the user async — never blocks the reply. A provider
	// blip here is harmless (ExtractFacts already swallows errors).
	go func(id, text string) {
		for _, f := range h.Client.ExtractFacts(r.Context(), text) {
			h.Store.Remember(id, f)
		}
	}(in.Session, in.Text)
}

// Characters lists all personas for the UI picker.
func (h *Handler) Characters(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"characters": contexts.List()})
}

// History returns greeting + recent turns so a returning page restores context.
func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("session"))
	if id == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"greeting":  h.sessionCharacterGreeting(id),
		"messages":  h.Store.Recent(id, 20),
		"character": h.sessionCharacter(id),
	})
}

func (h *Handler) sessionCharacterGreeting(id string) string {
	if c, ok := contexts.Get(h.sessionCharacter(id)); ok {
		return c.Greeting
	}
	d, _ := contexts.Default()
	return d.Greeting
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}