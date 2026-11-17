// Package llm is a minimal client for OpenAI-compatible chat completion APIs with tool calling
// (used with DeepSeek through a gateway; any compatible provider works via BASE_URL).
package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	// Parts, when set, replaces Content with multimodal content (text + images).
	Parts []ContentPart `json:"-"`
}

// ContentPart is one element of a multimodal message (OpenAI vision format).
type ContentPart struct {
	Type     string    `json:"type"` // text | image_url
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"` // https://… or data:<mime>;base64,<data>
}

func TextPart(t string) ContentPart { return ContentPart{Type: "text", Text: t} }

// ImagePart embeds an image as a data URL.
func ImagePart(mime string, data []byte) ContentPart {
	return ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}}
}

func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	if len(m.Parts) == 0 {
		return json.Marshal(plain(m))
	}
	return json.Marshal(struct {
		plain
		Content []ContentPart `json:"content"`
	}{plain(m), m.Parts})
}

// ExtractJSON strips markdown fences and surrounding prose around a JSON object in a model
// reply; some models wrap JSON in ```json even in JSON mode.
func ExtractJSON(content string) string {
	c := strings.TrimSpace(content)
	c = strings.TrimPrefix(c, "```json")
	c = strings.TrimPrefix(c, "```")
	c = strings.TrimSuffix(c, "```")
	if i, j := strings.Index(c, "{"), strings.LastIndex(c, "}"); i >= 0 && j > i {
		c = c[i : j+1]
	}
	return c
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // function
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string
	} `json:"function"`
}

type Tool struct {
	Type     string       `json:"type"` // function
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type Request struct {
	Messages    []Message
	Tools       []Tool
	JSONMode    bool
	MaxTokens   int
	Temperature *float64 // nil = client default (0.3)
}

// Client is the interface the advisor depends on; tests use a scripted fake.
type Client interface {
	Chat(ctx context.Context, req Request) (Message, Usage, error)
	Model() string
}

type HTTPClient struct {
	baseURL, apiKey, model string
	http                   *http.Client
	retries                int
}

func NewHTTPClient(baseURL, apiKey, model string, timeout time.Duration) *HTTPClient {
	return &HTTPClient{baseURL: baseURL, apiKey: apiKey, model: model,
		http: &http.Client{Timeout: timeout}, retries: 2}
}

func (c *HTTPClient) Model() string { return c.model }

type apiRequest struct {
	Model          string            `json:"model"`
	Messages       []Message         `json:"messages"`
	Tools          []Tool            `json:"tools,omitempty"`
	ToolChoice     string            `json:"tool_choice,omitempty"`
	ResponseFormat map[string]string `json:"response_format,omitempty"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
	Temperature    float64           `json:"temperature"`
}

type apiResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var (
	ErrNotConfigured = errors.New("LLM is not configured: set BASE_URL and AI_API_KEY")
	// ErrUpstream wraps any failure of the provider (network, HTTP status, malformed reply).
	ErrUpstream = errors.New("llm upstream error")
)

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return fmt.Sprintf("llm http %d: %s", e.code, e.body) }

func (c *HTTPClient) Chat(ctx context.Context, req Request) (Message, Usage, error) {
	if c.baseURL == "" || c.apiKey == "" {
		return Message{}, Usage{}, ErrNotConfigured
	}
	body := apiRequest{Model: c.model, Messages: req.Messages, Tools: req.Tools,
		MaxTokens: req.MaxTokens, Temperature: 0.3}
	if req.Temperature != nil {
		body.Temperature = *req.Temperature
	}
	if len(req.Tools) > 0 {
		body.ToolChoice = "auto"
	}
	if req.JSONMode {
		body.ResponseFormat = map[string]string{"type": "json_object"}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Message{}, Usage{}, err
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Message{}, Usage{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		msg, usage, err := c.do(ctx, payload)
		if err == nil {
			return msg, usage, nil
		}
		lastErr = err
		var se *statusError
		if errors.As(err, &se) && se.code != http.StatusTooManyRequests && se.code < 500 {
			break // client errors are not retryable
		}
	}
	if ctx.Err() != nil {
		return Message{}, Usage{}, ctx.Err()
	}
	return Message{}, Usage{}, fmt.Errorf("%w: %w", ErrUpstream, lastErr)
}

func (c *HTTPClient) do(ctx context.Context, payload []byte) (Message, Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Message{}, Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Message{}, Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Message{}, Usage{}, &statusError{resp.StatusCode, truncate(string(raw), 500)}
	}
	var out apiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Message{}, Usage{}, fmt.Errorf("llm decode: %w", err)
	}
	if out.Error != nil {
		return Message{}, Usage{}, fmt.Errorf("llm error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return Message{}, Usage{}, errors.New("llm: empty choices")
	}
	return out.Choices[0].Message, out.Usage, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
