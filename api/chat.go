// Package api exposes the HTTP endpoints for the chatbot.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"roleplay/contexts"
	"roleplay/database"
	"roleplay/model"
)

// Store is shared with main; keeps the handler thin.
var Store *database.Store

type chatIn struct {
	Session   string `json:"session"`   // anonymous client id
	Text      string `json:"text"`
	Character string `json:"character"` // optional persona key
}

// resolveCharacter picks the persona for this session (persisted on it).
func resolveCharacter(id, requested string) contexts.Character {
	if requested != "" {
		if c, ok := contexts.Get(requested); ok {
			Store.SetCharacter(id, strings.ToLower(strings.TrimSpace(c.Name)))
		}
	}
	name := Store.Character(id)
	if name == "" {
		if d, ok := contexts.Default(); ok {
			Store.SetCharacter(id, strings.ToLower(strings.TrimSpace(d.Name)))
			name = strings.ToLower(strings.TrimSpace(d.Name))
		}
	}
	if c, ok := contexts.Get(name); ok {
		return c
	}
	c, _ := contexts.Default()
	return c
}

func sessionCharacter(id string) string {
	if n := Store.Character(id); n != "" {
		return n
	}
	if d, ok := contexts.Default(); ok {
		return strings.ToLower(strings.TrimSpace(d.Name))
	}
	return ""
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
	char := resolveCharacter(in.Session, in.Character)

	// context = persona + remembered facts + recent turns + the new message
	messages := []model.Message{{Role: "system", Content: char.SystemPrompt()}}
	if facts := Store.Facts(in.Session); len(facts) > 0 {
		messages = append(messages, model.Message{
			Role:    "system",
			Content: "Things you personally remember about this user:\n- " + strings.Join(facts, "\n- "),
		})
	}
	messages = append(messages, Store.Recent(in.Session, 10)...)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	var assistant strings.Builder
	err := model.StreamChat(messages, char.Temperature, func(delta string) {
		assistant.WriteString(delta)
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]string{"delta": delta}))
		flusher.Flush()
	})
	if err != nil {
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]string{"error": err.Error()}))
		flusher.Flush()
		return
	}

	Store.Add(in.Session, "assistant", assistant.String())
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"done": true, "character": sessionCharacter(in.Session)}))
	flusher.Flush()

	// learn facts about the user async — never blocks the reply
	go func(id, text string) {
		for _, f := range model.ExtractFacts(text) {
			Store.Remember(id, f)
		}
	}(in.Session, in.Text)
}

// Characters lists all personas for the UI picker.
func Characters(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"characters": contexts.List()})
}

// History returns greeting + recent turns so a returning page restores context.
func History(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	if id == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"greeting":  sessionCharacterGreeting(id),
		"messages":  Store.Recent(id, 20),
		"character": sessionCharacter(id),
	})
}

func sessionCharacterGreeting(id string) string {
	if c, ok := contexts.Get(sessionCharacter(id)); ok {
		return c.Greeting
	}
	d, _ := contexts.Default()
	return d.Greeting
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}