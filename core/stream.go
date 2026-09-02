package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// StreamHandler receives each raw upstream data: payload (OpenAI chunk JSON).
// Returning an error stops the pump and is propagated to the caller.
type StreamHandler func(data []byte) error

// PumpStream reads the SSE stream from body and calls fn for every data
// payload until EOF, a read error, or an fn error. It also returns the last
// usage object seen (raw JSON) and the upstream-reported model, if any.
func PumpStream(ctx context.Context, body io.Reader, fn StreamHandler) (usage json.RawMessage, model string, err error) {
	err = ScanSSE(body, func(data []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c, perr := parseChunk(data)
		if perr != nil {
			return nil // lenient: forward undecodable chunks verbatim to fn
		}
		if len(c.Usage) > 0 && string(c.Usage) != "null" {
			usage = append(usage[:0], c.Usage...)
		}
		if c.Model != "" {
			model = c.Model
		}
		return fn(data)
	})
	if err != nil && errors.Is(err, context.Canceled) {
		return usage, model, nil
	}
	return usage, model, err
}

// Completion is the aggregated non-stream result.
type Completion struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []CompletionChoice `json:"choices"`
	Usage   json.RawMessage    `json:"usage,omitempty"`
}

// CompletionChoice mirrors one aggregated choice.
type CompletionChoice struct {
	Index        int               `json:"index"`
	Message      CompletionMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

// CompletionMessage is the aggregated assistant message.
type CompletionMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// AggregateCompletion folds a full SSE stream into a single chat.completion
// object (used for non-stream upstream callers, which the upstream refuses).
// fallbackModel is used when the upstream never reports a model.
func AggregateCompletion(body io.Reader, fallbackModel string, now func() time.Time) (*Completion, error) {
	if now == nil {
		now = time.Now
	}
	out := &Completion{
		ID:      "chatcmpl-workbuddy",
		Object:  "chat.completion",
		Created: now().Unix(),
	}
	var content, reasoning strings.Builder
	var toolCalls []ToolCall
	var finish *string
	var upstreamModel string
	err := ScanSSE(body, func(data []byte) error {
		c, perr := parseChunk(data)
		if perr != nil {
			return perr
		}
		if c.ID != "" && out.ID == "chatcmpl-workbuddy" {
			out.ID = c.ID
		}
		if c.Model != "" {
			upstreamModel = c.Model
		}
		if len(c.Usage) > 0 && string(c.Usage) != "null" {
			out.Usage = append(out.Usage[:0], c.Usage...)
		}
		for _, ch := range c.Choices {
			content.WriteString(ch.Delta.Content)
			reasoning.WriteString(ch.Delta.ReasoningContent)
			toolCalls = append(toolCalls, ch.Delta.ToolCalls...)
			if ch.FinishReason != nil {
				finish = ch.FinishReason
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.Model = firstNonEmpty(upstreamModel, fallbackModel)
	msg := CompletionMessage{Role: "assistant", Content: content.String()}
	if reasoning.Len() > 0 {
		msg.ReasoningContent = reasoning.String()
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	fr := "stop"
	if finish != nil && *finish != "" {
		fr = *finish
	}
	out.Choices = []CompletionChoice{{Index: 0, Message: msg, FinishReason: fr}}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
