package contexts

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed personas/*.yml
var personasFS embed.FS

// Character is one persona the chatbot can embody.
type Character struct {
	Name        string  `yaml:"name"`
	Language    string  `yaml:"language"`
	Personality string  `yaml:"personality"`
	Backstory   string  `yaml:"backstory"`
	Style       string  `yaml:"style"`
	Greeting    string  `yaml:"greeting"`
	Avatar      string  `yaml:"avatar"`     // emoji avatar
	Accent      string  `yaml:"accent"`     // hex color for theming the UI
	Temperature float64 `yaml:"temperature"` // lower = consistent, higher = fun
}

// SystemPrompt builds the stable system message that enforces the persona.
func (c Character) SystemPrompt() string {
	return fmt.Sprintf(
		"You are %s.\n\n"+
			"Language: %s\nYou MUST reply only in %s.\n\n"+
			"Personality: %s\n"+
			"Backstory: %s\n"+
			"Speaking style: %s\n\n"+
			"You always stay in character. Never mention you are an AI model.",
		c.Name, c.Language, c.Language, c.Personality, c.Backstory, c.Style,
	)
}

// Public fields exposed by the API/UI (no backstory leaked to keep it light).
type Public struct {
	Name        string  `json:"name"`
	Language    string  `json:"language"`
	Personality string  `json:"personality"`
	Greeting    string  `json:"greeting"`
	Avatar      string  `json:"avatar,omitempty"`
	Accent      string  `json:"accent,omitempty"`
	Temperature float64 `json:"temperature"`
}

func (c Character) Public() Public {
	return Public{c.Name, c.Language, c.Personality, c.Greeting, c.Avatar, c.Accent, c.Temperature}
}

var characters = map[string]Character{}
var defaultName = "aarav"

func init() {
	entries, err := fs.Glob(personasFS, "personas/*.yml")
	if err != nil {
		return
	}
	for _, e := range entries {
		b, err := fs.ReadFile(personasFS, e)
		if err != nil {
			continue
		}
		var c Character
		if err := yaml.Unmarshal(b, &c); err != nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(c.Name))
		if key == "" {
			key = strings.TrimSuffix(filepath.Base(e), ".yml")
		}
		if c.Temperature <= 0 {
			c.Temperature = 0.9
		}
		if c.Avatar == "" {
			c.Avatar = "✨"
		}
		if c.Accent == "" {
			c.Accent = "#7c8bff"
		}
		characters[key] = c
	}
	if _, ok := characters[defaultName]; !ok && len(characters) > 0 {
		for k := range characters {
			defaultName = k
			break
		}
	}
}

// Default returns the fallback persona.
func Default() (Character, bool) {
	c, ok := characters[defaultName]
	return c, ok
}

// Get looks up a persona by name (case-insensitive).
func Get(name string) (Character, bool) {
	c, ok := characters[strings.ToLower(strings.TrimSpace(name))]
	return c, ok
}

// Names returns sorted persona keys.
func Names() []string {
	out := make([]string, 0, len(characters))
	for k := range characters {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// List returns the public catalog for the UI picker.
func List() []Public {
	names := Names()
	out := make([]Public, 0, len(names))
	for _, n := range names {
		out = append(out, characters[n].Public())
	}
	return out
}