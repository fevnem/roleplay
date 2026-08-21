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

// Server config. The Hetzner token is hardcoded as a default; env overrides it.
const (
	defaultBaseURL = "https://inference.hetzner.com/api/v1"
	defaultAPIKey  = "REDACTED_LLM_API_TOKEN"
	defaultModel   = "Qwen/Qwen3.6-35B-A3B-FP8" // fastest model on Hetzner free
)

type chatRequest struct {
	Model              string         `json:"model"`
	Messages           []Message      `json:"messages"`
	Stream             bool           `json:"stream"`
	MaxTokens          int            `json:"max_tokens"`
	Temperature        *float64       `json:"temperature,omitempty"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs"`
}

func callEndpoint(messages []Message, temperature float64, stream bool, maxTokens int) (*http.Response, error) {
	req := chatRequest{
		Model:     defaultModel,
		Messages:  messages,
		Stream:    stream,
		MaxTokens: maxTokens,
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
	}
	if temperature > 0 {
		req.Temperature = &temperature
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequest(http.MethodPost, defaultBaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+defaultAPIKey)
	return http.DefaultClient.Do(httpReq)
}

// StreamChat sends the context and streams each content token to onDelta.
func StreamChat(messages []Message, temperature float64, onDelta func(string)) error {
	resp, err := callEndpoint(messages, temperature, true, 512)
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

// chatOnce is a single non-streaming completion (used for facts extraction).
func chatOnce(messages []Message, temperature float64, maxTokens int) (string, error) {
	resp, err := callEndpoint(messages, temperature, false, maxTokens)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("provider status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
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
func ExtractFacts(userMsg string) []string {
	if strings.TrimSpace(userMsg) == "" {
		return nil
	}
	content, err := chatOnce([]Message{
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

// ListModels returns known available model ids.
func ListModels() []string {
	return []string{defaultModel}
}