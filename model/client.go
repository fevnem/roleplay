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

// Defaults for the provider connection.
const (
	DefaultBaseURL = "https://inference.hetzner.com/api/v1"
	DefaultModel   = "Qwen/Qwen3.6-35B-A3B-FP8"

	// defaultTimeout bounds any single provider round-trip so a stalled
	// inference ever hangs a user request.
	defaultTimeout = 2 * time.Minute
)

// Message is one chat turn, shaped like OpenAI's chat message.
type Message struct {
	Role    string `json:"role"`    // "system" | "user" | "assistant"
	Content string `json:"content"`
}

// Config holds provider settings. The API key is read from the environment at
// construction time so it is never compiled into the binary.
type Config struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration // optional; 0 → defaultTimeout
}

// ConfigFromEnv builds a Config from the process environment.
//
//	MODEL_API_KEY    (required) provider bearer token
//	MODEL_BASE_URL   (optional) default: https://inference.hetzner.com/api/v1
//	MODEL_NAME       (optional) default: Qwen/Qwen3.6-35B-A3B-FP8
func ConfigFromEnv() Config {
	return Config{
		APIKey:  strings.TrimSpace(os.Getenv("MODEL_API_KEY")),
		BaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("MODEL_BASE_URL")), "/"),
		Model:   strings.TrimSpace(os.Getenv("MODEL_NAME")),
	}
}

// Client talks to one OpenAI-compatible endpoint. Zero-value Client is safe to
// use after resolving defaults via its methods, but prefer NewClient.
type Client struct {
	apiKey  string
	baseURL string
	model   string
	httpc   *http.Client
}

// NewClient resolves config defaults and returns a ready-to-use Client.
func NewClient(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   cfg.Model,
		httpc:   &http.Client{Timeout: timeout},
	}
}

// HasAPIKey reports whether a provider token is configured.
func (c *Client) HasAPIKey() bool { return c.apiKey != "" }

// Model returns the configured model id.
func (c *Client) Model() string { return c.model }

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
// separate reasoning field (required for e.g. Hetzner's Qwen reasoning models).
func (c *Client) call(ctx context.Context, messages []Message, temperature float64, stream bool, maxTokens int) (*http.Response, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("provider API key is not configured (set MODEL_API_KEY)")
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
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