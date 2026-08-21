// Package database is a tiny in-memory, time-limited "memory" store.
// No SQL, no files at runtime (optional JSON snapshot on shutdown).
// Sessions auto-forget after TTL — the "temporary consistency" you asked for.
package database

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"dreamproject/backend/model"
)

const (
	TTL          = 3 * time.Hour      // a session's memory lasts a few hours
	maxMessages = 20                 // keep only the last N turns per session
	maxSessions  = 5000              // safety cap on concurrent sessions
)

type Session struct {
	ID         string          `json:"id"`
	Messages   []model.Message `json:"messages"`
	LastActive time.Time       `json:"last_active"`
}

// Store is the whole "database": a map + mutex.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	path     string // optional snapshot file
}

func NewStore(snapshotPath string) *Store {
	s := &Store{sessions: make(map[string]*Session), path: snapshotPath}
	s.load()
	return s
}

// GetOrCreate returns the session, creating it if unknown.
func (s *Store) GetOrCreate(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		sess.LastActive = time.Now()
		return sess
	}
	sess := &Session{ID: id, LastActive: time.Now()}
	s.sessions[id] = sess
	return sess
}

// Add pushes a turn into the session, keeping only the last maxMessages.
func (s *Store) Add(id, role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		sess = &Session{ID: id, LastActive: time.Now()}
		s.sessions[id] = sess
	}
	sess.LastActive = time.Now()
	sess.Messages = append(sess.Messages, model.Message{Role: role, Content: content})
	if len(sess.Messages) > maxMessages {
		sess.Messages = sess.Messages[len(sess.Messages)-maxMessages:]
	}
}

// Recent returns the last n turns.
func (s *Store) Recent(id string, n int) []model.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sess, ok := s.sessions[id]; ok {
		if n > len(sess.Messages) {
			n = len(sess.Messages)
		}
		return sess.Messages[len(sess.Messages)-n:]
	}
	return nil
}

// Evict drops sessions idle longer than a few hours. Call it on a ticker.
func (s *Store) Evict() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-TTL)
	for id, sess := range s.sessions {
		if sess.LastActive.Before(cutoff) {
			delete(s.sessions, id)
		}
	}
}

// Save writes the store to the snapshot JSON (survives restart).
func (s *Store) Save() {
	if s.path == "" {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := json.Marshal(s.sessions)
	if err != nil {
		log.Printf("snapshot marshal: %v", err)
		return
	}
	if err := os.WriteFile(s.path, b, 0o600); err != nil {
		log.Printf("snapshot write: %v", err)
	}
}

func (s *Store) load() {
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return // no snapshot yet — fine
	}
	var sess map[string]*Session
	if err := json.Unmarshal(b, &sess); err == nil {
		s.sessions = sess
	}
}