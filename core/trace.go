package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
)

// TraceCtx carries the official client's conversation / trace identifier
// family. MITM capture (2026-09-02) proved the upstream only serves its
// shared prompt cache when these headers are present: identical requests go
// from prompt_cache_hit_tokens=0 to >0 once the identifiers are attached.
//
// See notes/workbuddy-protocol-notes.md §12-13 and specs/capture-2026-09-02.md.
type TraceCtx struct {
	ConversationID string // X-Conversation-ID, stable per conversation (uuid)
	RootID         string // X-Conversation-Request-ID / X-Root-Request-ID / X-Trace-ID, 32-hex
	SpanID         string // per-request span id, 32-hex
	RequestID      string // X-Request-ID / X-Conversation-Message-ID, 32-hex, per message
}

// NewTraceCtx creates a fresh trace context for a conversation.
func NewTraceCtx() *TraceCtx {
	tc := &TraceCtx{
		ConversationID: newUUID(),
		RootID:         newHex32(),
		SpanID:         newHex32(),
	}
	tc.NextMessage()
	return tc
}

// NextMessage rotates the per-message request id (each chat call gets a new one).
func (tc *TraceCtx) NextMessage() {
	tc.RequestID = newHex32()
}

// Apply sets the full official header family onto h.
func (tc *TraceCtx) Apply(h http.Header) {
	h.Set("X-Conversation-ID", tc.ConversationID)
	h.Set("X-Conversation-Request-ID", tc.RootID)
	h.Set("X-Conversation-Message-ID", tc.RequestID)
	h.Set("X-Request-ID", tc.RequestID)
	h.Set("X-Root-Request-ID", tc.RootID)
	h.Set("X-Trace-ID", tc.RootID)
	h.Set("X-Agent-Intent", "craft")
	h.Set("X-Agent-Purpose", "conversation")
	h.Set("X-Agent-Type", "main")
	h.Set("X-IDE-Type", "CLI")
	h.Set("X-IDE-Name", "CLI")
	h.Set("X-IDE-Version", IdeVersion)
	h.Set("X-Private-Data", "false")
	h.Set("x-codebuddy-request", "1")
	h.Set("traceparent", "00-"+tc.RootID+"-"+tc.SpanID+"-01")
	h.Set("b3", tc.RootID+"-"+tc.SpanID+"-1")
	h.Set("X-B3-TraceId", tc.RootID)
	h.Set("X-B3-SpanId", tc.SpanID)
	h.Set("X-B3-Sampled", "1")
	h.Set("x-stainless-arch", "x64")
	h.Set("x-stainless-lang", "js")
	h.Set("x-stainless-os", "linux")
	h.Set("x-stainless-package-version", "6.25.0")
	h.Set("x-stainless-retry-count", "0")
	h.Set("x-stainless-runtime", "node")
	h.Set("x-stainless-runtime-version", "v24.18.0")
}

func newHex32() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("wb2api: rand: %v", err))
	}
	return hex.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("wb2api: rand: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
