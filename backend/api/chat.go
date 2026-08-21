// Package api exposes the HTTP endpoints for the chatbot.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"dreamproject/backend/contexts"
	"dreamproject/backend/database"
	"dreamproject/backend/model"
)

// Store is shared with main; keeps the handler thin.
var Store *database.Store
var Char = contexts.Default

type chatIn struct {
	Session string `json:"session"` // the anonymous client id (a cookie value)
	Text    string `json:"text"`
}

// Chat handles POST /api/chat and streams the reply back over SSE.
func Chat(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var in chatIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Session) == "" || strings.TrimSpace(in.Text) == "" {
		http.Error(w, "session and text required", http.StatusBadRequest)
		return
	}

	Store.Add(in.Session, "user", in.Text)

	// build context: stable persona + recent turns
	messages := []model.Message{{Role: "system", Content: Char.SystemPrompt()}}
	messages = append(messages, Store.Recent(in.Session, 10)...)

	// stream out as SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	var assistant strings.Builder
	err := model.StreamChat(messages, func(delta string) {
		assistant.WriteString(delta)
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]string{"delta": delta}))
		flusher.Flush()
	})

	if err != nil {
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]string{"error": err.Error()}))
		flusher.Flush()
	} else {
		Store.Add(in.Session, "assistant", assistant.String())
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"done": true}))
		flusher.Flush()
	}
}

// History returns the last turns for a session so a returning page can reload it.
func History(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	if id == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"greeting":  Char.Greeting,
		"messages":  Store.Recent(id, 20),
		"character": map[string]string{"name": Char.Name, "language": Char.Language},
	})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}