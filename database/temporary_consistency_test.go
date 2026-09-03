package database

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAddAndRecent(t *testing.T) {
	s := NewStore("")
	s.Add("s1", "user", "hello")
	s.Add("s1", "assistant", "hi!")

	recent := s.Recent("s1", 10)
	if len(recent) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(recent))
	}
	if recent[0].Content != "hello" || recent[1].Content != "hi!" {
		t.Errorf("unexpected message order: %+v", recent)
	}
}

func TestRecentCapped(t *testing.T) {
	s := NewStore("")
	for i := 0; i < maxMessages+5; i++ {
		s.Add("s1", "user", string(rune('a'+i)))
	}
	if got := len(s.Recent("s1", 100)); got != maxMessages {
		t.Fatalf("expected %d messages kept, got %d", maxMessages, got)
	}
}

func TestRecentUnknownSessionEmpty(t *testing.T) {
	s := NewStore("")
	if got := s.Recent("nope", 10); got != nil {
		t.Fatalf("expected nil for unknown session, got %v", got)
	}
}

func TestCharacterPersistence(t *testing.T) {
	s := NewStore("")
	if got := s.Character("s1"); got != "" {
		t.Fatalf("expected empty character initially, got %q", got)
	}
	s.SetCharacter("s1", "luna")
	if got := s.Character("s1"); got != "luna" {
		t.Fatalf("expected 'luna', got %q", got)
	}
	// character must survive a message add (Add gets the same session)
	s.Add("s1", "user", "hey")
	if got := s.Character("s1"); got != "luna" {
		t.Fatalf("character lost after Add: got %q", got)
	}
}

func TestFactsDedupeAndCap(t *testing.T) {
	s := NewStore("")
	for i := 0; i < 3; i++ {
		s.Remember("s1", "loves stars")
	}
	if got := len(s.Facts("s1")); got != 1 {
		t.Fatalf("expected dedup to one fact, got %d", got)
	}
	for i := 0; i < maxFacts+3; i++ {
		s.Remember("s1", "fact"+string(rune('0'+i)))
	}
	if got := len(s.Facts("s1")); got != maxFacts {
		t.Fatalf("expected %d facts capped, got %d", maxFacts, got)
	}
}

func TestRememberEmptyIgnored(t *testing.T) {
	s := NewStore("")
	s.Remember("s1", "   ")
	s.Remember("s1", "")
	if got := len(s.Facts("s1")); got != 0 {
		t.Fatalf("expected no empty facts stored, got %d", got)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	s := NewStore(path)
	s.SetCharacter("s1", "araav")
	s.Add("s1", "user", "persisted")
	s.Remember("s1", "remembers tea")

	s.Save()
	// reload from the same file
	s2 := NewStore(path)
	if got := s2.Character("s1"); got != "araav" {
		t.Fatalf("expected character persisted, got %q", got)
	}
	if len(s2.Recent("s1", 10)) != 1 {
		t.Fatal("expected message persisted")
	}
	if got := s2.Facts("s1"); len(got) != 1 || got[0] != "remembers tea" {
		t.Fatalf("expected fact persisted, got %v", got)
	}
}

func TestEvictIdleSessions(t *testing.T) {
	// use a tiny TTL-free store: manually age LastActive then Evict
	s := NewStore("")
	s.Add("old", "user", "hi")
	s.Add("fresh", "user", "yo")
	if sess := s.sessions["old"]; sess != nil {
		sess.LastActive = time.Now().Add(-TTL - time.Hour)
	}
	s.Evict()
	if _, ok := s.sessions["old"]; ok {
		t.Fatal("expected idle session evicted")
	}
	if _, ok := s.sessions["fresh"]; !ok {
		t.Fatal("expected fresh session retained")
	}
}

func TestNoSnapshotPathSkipsSave(t *testing.T) {
	s := NewStore("")
	s.Save()
}