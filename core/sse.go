package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// ScanSSE reads an SSE stream line by line and invokes fn with the raw JSON
// payload of each data: event (the "data:" prefix stripped, including nested
// "data:" prefixes). Empty events and the stream terminator [DONE] are
// skipped; event: lines are ignored.
//
// The returned error is either a read error or the first error from fn.
func ScanSSE(r io.Reader, fn func(data []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		data := stripDataPrefix(trimmed)
		payload := bytes.TrimSpace(data)
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if err := fn(payload); err != nil {
			return err
		}
	}
	return sc.Err()
}

// stripDataPrefix removes every leading "data:" occurrence so that payloads
// that themselves begin with "data:" decode correctly.
func stripDataPrefix(b []byte) []byte {
	for {
		t := bytes.TrimLeft(b, " \t")
		if !bytes.HasPrefix(t, []byte("data:")) {
			return t
		}
		b = t[len("data:"):]
	}
}

// sseChunk is the OpenAI-format streaming chunk we care about.
type sseChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int `json:"index"`
		Delta        struct {
			Role             string     `json:"role"`
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

// parseChunk decodes one data: payload into an sseChunk. It is lenient:
// malformed chunks return an error, empty payloads yield a zero chunk.
func parseChunk(payload []byte) (*sseChunk, error) {
	var c sseChunk
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// CleanChunk strips empty-valued fields (null/""/[]/{}) from each choice's
// delta so strict clients don't trip on {"function_call":null,"tool_calls":[]}
// (confirmed present in upstream chunks by capture 2026-09-02). Undecodable
// payloads are returned unchanged.
func CleanChunk(payload []byte) []byte {
	var obj map[string]any
	if json.Unmarshal(payload, &obj) != nil {
		return payload
	}
	if choices, ok := obj["choices"].([]any); ok {
		for _, c := range choices {
			choice, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if delta, ok := choice["delta"].(map[string]any); ok {
				for k, v := range delta {
					if isEmptyValue(v) {
						delete(delta, k)
					}
				}
			}
		}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return payload
	}
	return out
}

func isEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}
