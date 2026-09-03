// Package model talks to an OpenAI-compatible chat-completions provider.
//
// The package is deliberately dependency-injected: a *model.Client is built
// from a Config and can be constructed in tests with a mock http.RoundTripper,
// so the streaming and facts logic is exercised against fake providers without
// network access.
package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// providerTimeout bounds any single provider round-trip so a stalled
// inference never hangs a user request.
const providerTimeout = 2 * time.Minute

// Message is one chat turn, shaped like OpenAI's chat message.
type Message struct {
	Role    string `json:"role"`    // "system" | "user" | "assistant"
	Content string `json:"content"`
}

// Config holds provider settings. All fields come from the environment at
// construction time so provider-specific values are never compiled into the
// binary and the same binary can target any OpenAI-compatible endpoint.
type Config struct {
	Name      string        // provider name (label only, e.g. "hetzner", "openai")
	Model     string        // model identifier (e.g. "Qwen/Qwen3.6-35B-A3B-FP8")
	Endpoint  string        // OpenAI-compatible base endpoint (e.g. https://inference.hetzner.com/api/v1)
	APIKey    string        // provider bearer token
	Timeout   time.Duration // optional; 0 → providerTimeout
}

// ConfigFromEnv builds a Config from the process environment. All four provider
// settings are expected; empty values are left empty and surface as validation
// errors when a client is built, rather than silently falling back to a vendor.
//
//	PROVIDER_NAME         provider name (label)
//	MODEL_NAME            model identifier to request
//	PROVIDER_API_ENDPOINT OpenAI-compatible base endpoint (no trailing /chat/completions)
//	PROVIDER_API_KEY      provider bearer token
func ConfigFromEnv() Config {
	return Config{
		Name:     strings.TrimSpace(os.Getenv("PROVIDER_NAME")),
		Model:    strings.TrimSpace(os.Getenv("MODEL_NAME")),
		Endpoint: strings.TrimRight(strings.TrimSpace(os.Getenv("PROVIDER_API_ENDPOINT")), "/"),
		APIKey:   strings.TrimSpace(os.Getenv("PROVIDER_API_KEY")),
	}
}

// ConfigError is returned when a provider Config is incomplete, naming which
// required fields are missing so operators can fix the environment quickly.
type ConfigError struct {
	Missing []string
}

func (e *ConfigError) Error() string {
	return "provider config incomplete, missing: " + strings.Join(e.Missing, ", ")
}

// Client talks to one OpenAI-compatible endpoint.
type Client struct {
	name    string
	apiKey  string
	endpoint string
	model   string
	httpc   *http.Client
}

// NewClient validates and returns a ready-to-use Client. It returns a
// *ConfigError (as an error) if any required field is empty, so a misconfigured
// provider fails loudly instead of calling a default vendor.
func NewClient(cfg Config) (*Client, error) {
	var missing []string
	if cfg.Name == "" {
		missing = append(missing, "PROVIDER_NAME")
	}
	if cfg.Model == "" {
		missing = append(missing, "MODEL_NAME")
	}
	if cfg.Endpoint == "" {
		missing = append(missing, "PROVIDER_API_ENDPOINT")
	}
	if cfg.APIKey == "" {
		missing = append(missing, "PROVIDER_API_KEY")
	}
	if len(missing) > 0 {
		return nil, &ConfigError{Missing: missing}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = providerTimeout
	}
	return &Client{
		name:     cfg.Name,
		apiKey:   cfg.APIKey,
		endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		model:    cfg.Model,
		httpc:    &http.Client{Timeout: timeout},
	}, nil
}

// HasAPIKey reports whether a provider token is configured.
func (c *Client) HasAPIKey() bool { return c != nil && c.apiKey != "" }

// Model returns the configured model id.
func (c *Client) Model() string { return c.model }

// Name returns the configured provider name.
func (c *Client) Name() string { return c.name }

// SetHTTPClient overrides the underlying HTTP client. Tests use this to inject
// a fake RoundTripper; callers may use it to tune connection pooling.
func (c *Client) SetHTTPClient(hc *http.Client) {
	if hc != nil {
		c.httpc = hc
	}
}

// chatRequest is the OpenAI-compatible request body.
type chatRequest struct {
	Model              string         `json:"model"`
	Messages           []Message      `json:"messages"`
	Stream             bool           `json:"stream"`
	MaxTokens          int            `json:"max_tokens"`
	Temperature        *float64       `json:"temperature,omitempty"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs"`
}

// call performs one provider request. It always sets the "enable_thinking":
// false kwarg so reasoning-model output is returned in content rather than a
// separate reasoning field (required for reasoning models such as Qwen).
func (c *Client) call(ctx context.Context, messages []Message, temperature float64, stream bool, maxTokens int) (*http.Response, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("provider API key is not configured (set PROVIDER_API_KEY)")
	}
	req := chatRequest{
		Model:              c.model,
		Messages:           messages,
		Stream:             stream,
		MaxTokens:          maxTokens,
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
	}
	if temperature > 0 {
		req.Temperature = &temperature
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.httpc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, fmt.Errorf("provider status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// Stream streams each content token of a completion to onDelta. The reply is
// cut at maxTokens. Returns nil on a clean, fully-consumed stream.
func (c *Client) Stream(ctx context.Context, messages []Message, temperature float64, onDelta func(string)) error {
	resp, err := c.call(ctx, messages, temperature, true, 512)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return readSSE(resp.Body, onDelta)
}

// Complete returns a single non-streaming completion (used for facts extraction).
func (c *Client) Complete(ctx context.Context, messages []Message, temperature float64, maxTokens int) (string, error) {
	resp, err := c.call(ctx, messages, temperature, false, maxTokens)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode completion: %w", err)
	}
	if len(out.Choices) > 0 {
		return out.Choices[0].Message.Content, nil
	}
	return "", nil
}

var factSystem = "From the user's message, extract up to 2 stable facts about the user " +
	"(name, interests, preferences, things they told you). Return them one per line, " +
	"plain text, short. If there are none, reply exactly: NONE"

// ExtractFacts returns short persistent facts about the user from a message.
// Failures are non-fatal: a provider blip yields no facts rather than an error.
func (c *Client) ExtractFacts(ctx context.Context, userMsg string) []string {
	if strings.TrimSpace(userMsg) == "" {
		return nil
	}
	content, err := c.Complete(ctx, []Message{
		{Role: "system", Content: factSystem},
		{Role: "user", Content: userMsg},
	}, 0.3, 60)
	if err != nil {
		return nil
	}
	var facts []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if line == "" || strings.EqualFold(line, "NONE") {
			continue
		}
		facts = append(facts, line)
		if len(facts) >= 2 {
			break
		}
	}
	return facts
}

// readSSE parses an OpenAI-style SSE stream ("data:" JSON lines, optionally
// terminated by "[DONE]") and forwards each content delta to onDelta.
func readSSE(r io.Reader, onDelta func(string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct{ Content string `json:"content"` } `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			if t := chunk.Choices[0].Delta.Content; t != "" {
				onDelta(t)
			}
		}
	}
	return sc.Err()
}