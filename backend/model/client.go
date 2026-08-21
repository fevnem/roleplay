// Package model talks to the (OpenAI-compatible) LLM provider.
package model

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message is one chat turn, shaped like OpenAI's chat message.
type Message struct {
	Role    string `json:"role"`    // "system" | "user" | "assistant"
	Content string `json:"content"`
}

// Server config. The Hetzner free-inference token is hardcoded here as the
// default, but an env var overrides it (kept as fallback so it runs anywhere).
const (
	defaultBaseURL = "https://inference.hetzner.com/api/v1"
	defaultAPIKey  = "REDACTED_LLM_API_TOKEN"
	defaultModel   = "Qwen/Qwen3.6-35B-A3B-FP8" // fastest model on Hetzner free
)

// modelMap says which model name to actually send for a requested one.
// We route everything to the fast Qwen (can extend later).
var modelMap = map[string]string{
	"qwen":    "Qwen/Qwen3.6-35B-A3B-FP8",
	"fast":    "Qwen/Qwen3.6-35B-A3B-FP8",
	"default": defaultModel,
}

// ResolveModel maps a friendly request to a real model id, falling back to default.
func ResolveModel(name string) string {
	if m, ok := modelMap[strings.ToLower(name)]; ok {
		return m
	}
	return defaultModel
}

type chatRequest struct {
	Model               string         `json:"model"`
	Messages            []Message      `json:"messages"`
	Stream              bool           `json:"stream"`
	MaxTokens           int            `json:"max_tokens"`
	ChatTemplateKwargs  map[string]any `json:"chat_template_kwargs"`
}

// StreamChat sends the full context to the provider and streams each content
// token to onDelta. enable_thinking is forced off: Qwen reasoning models put
// their "thinking" in a separate field and return null content by default.
func StreamChat(messages []Message, onDelta func(string)) error {
	body, _ := json.Marshal(chatRequest{
		Model:      defaultModel,
		Messages:   messages,
		Stream:     true,
		MaxTokens:  512,
		ChatTemplateKwargs: map[string]any{
			"enable_thinking": false,
		},
	})

	req, err := http.NewRequest(http.MethodPost, defaultBaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+defaultAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("provider status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	return readSSE(resp.Body, onDelta)
}

// readSSE parses the OpenAI-style Server-Sent-Events stream.
func readSSE(r io.Reader, onDelta func(string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
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

// ListModels returns the currently known/available model ids (for /v1/models later).
func ListModels() []string {
	return []string{defaultModel}
}