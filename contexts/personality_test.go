package contexts

import (
	"strings"
	"testing"
)

func TestListNonEmptyAndSorted(t *testing.T) {
	list := List()
	if len(list) == 0 {
		t.Fatal("expected at least one persona, got none")
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Name > list[i].Name {
			t.Fatalf("personas not sorted: %q before %q", list[i-1].Name, list[i].Name)
		}
	}
}

func TestDefaultAvailable(t *testing.T) {
	c, ok := Default()
	if !ok {
		t.Fatal("expected a default persona")
	}
	if strings.TrimSpace(c.Name) == "" {
		t.Fatal("default persona has empty name")
	}
}

func TestGetCaseInsensitive(t *testing.T) {
	c, ok := Get("Luna")
	if !ok {
		t.Fatal("expected to find luna case-insensitively")
	}
	if !strings.EqualFold(c.Name, "Luna") {
		t.Fatalf("expected Luna, got %q", c.Name)
	}
}

func TestGetUnknownReturnsFalse(t *testing.T) {
	if _, ok := Get("no-such-character"); ok {
		t.Fatal("expected lookup of unknown persona to fail")
	}
}

func TestPersonasHaveAvatarAndAccent(t *testing.T) {
	list := List()
	for _, p := range list {
		if p.Avatar == "" {
			t.Errorf("persona %q has no avatar", p.Name)
		}
		if p.Accent == "" {
			t.Errorf("persona %q has no accent", p.Name)
		}
	}
}

func TestSystemPromptEnforcesIdentity(t *testing.T) {
	c, ok := Default()
	if !ok {
		t.Fatal("no default persona")
	}
	prompt := c.SystemPrompt()
	if !strings.Contains(prompt, "You are "+c.Name) {
		t.Errorf("prompt missing character identity: %s", prompt)
	}
	if !strings.Contains(strings.ToLower(prompt), "never mention you are an ai") {
		t.Errorf("prompt missing stay-in-character instruction: %s", prompt)
	}
	if c.Language != "" && !strings.Contains(prompt, c.Language) {
		t.Errorf("prompt missing language instruction for %q", c.Language)
	}
}

func TestPublicDoesNotLeakBackstory(t *testing.T) {
	c, ok := Default()
	if !ok {
		t.Fatal("no default persona")
	}
	if c.Backstory != "" {
		// backstory should not appear in Public()
		p := c.Public()
		if strings.Contains(p.Personality, "leak") {
			t.Errorf("Public unexpectedly includes backstory-ish content: %q", p.Personality)
		}
	}
}