package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- model table (single source of truth for /v1/models + plugin) ---

func TestModelTableIsNotEmpty(t *testing.T) {
	if len(Models) == 0 {
		t.Fatal("Models is empty")
	}
	seen := make(map[string]bool, len(Models))
	for _, m := range Models {
		if m.ID == "" || m.Ctx <= 0 {
			t.Fatalf("model entry invalid: %+v", m)
		}
		if seen[m.ID] {
			t.Fatalf("duplicate model id: %s", m.ID)
		}
		seen[m.ID] = true
	}
}

// --- trace context (official client identifiers) ---

func TestTraceCtxHeaders(t *testing.T) {
	tc := NewTraceCtx()
	h := http.Header{}
	tc.Apply(h)
	checks := map[string]string{
		"X-Conversation-ID":         tc.ConversationID,
		"X-Conversation-Request-ID": tc.RootID,
		"X-Conversation-Message-ID": tc.RequestID,
		"X-Request-ID":              tc.RequestID,
		"X-Root-Request-ID":         tc.RootID,
		"X-Trace-ID":                tc.RootID,
		"traceparent":               "00-" + tc.RootID + "-" + tc.SpanID + "-01",
		"b3":                        tc.RootID + "-" + tc.SpanID + "-1",
		"X-B3-TraceId":              tc.RootID,
		"X-B3-Sampled":              "1",
		"X-IDE-Version":             IdeVersion,
	}
	for k, want := range checks {
		if got := h.Get(k); got != want {
			t.Fatalf("%s: got %q want %q", k, got, want)
		}
	}
	old := tc.RequestID
	tc.NextMessage()
	if tc.RequestID == old {
		t.Fatalf("NextMessage did not rotate request id")
	}
}

func TestDoChatSendsTraceHeaders(t *testing.T) {
	m := newMockUpstream(t)
	_ = m
	// Use a dedicated mock that records headers.
	sawReqID := false
	var mu = http.NewServeMux()
	mu.HandleFunc("/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Request-ID") != "" && r.Header.Get("X-Conversation-Request-ID") != "" {
			sawReqID = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mu)
	defer srv.Close()
	c := NewChatClient()
	c.Base = srv.URL
	_, err := c.DoChat(context.Background(), &Auth{Auth: StoredTokens{AccessToken: "at"}}, nil, map[string]any{"model": "hy3"})
	if err != nil {
		t.Fatalf("DoChat: %v", err)
	}
	if !sawReqID {
		t.Fatalf("trace headers not sent on chat request")
	}
}

func TestRewriteAddsStreamOptions(t *testing.T) {
	body := map[string]any{"model": "hy3", "messages": []any{}}
	rewriteBody("hy3", body)
	so, ok := body["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Fatalf("stream_options.include_usage not set: %v", body["stream_options"])
	}
}

// --- body rewrite rules ---

func TestForceStream(t *testing.T) {
	body := map[string]any{"model": "hy3-preview", "stream": false}
	rewriteBody("hy3-preview", body)
	if body["stream"] != true {
		t.Fatalf("stream not forced: %v", body["stream"])
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("hy3 reasoning_effort not forced: %v", body["reasoning_effort"])
	}
}

func TestSanitizeBlockedTemplates(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are Claude Code, Anthropic's official CLI for Claude.\nMain branch (you will usually use this for PRs)"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}}},
		},
	}
	rewriteSystemForUpstream(body)
	msgs := body["messages"].([]any)
	sys := msgs[0].(map[string]any)["content"].(string)
	if strings.Contains(sys, "official CLI for Claude") || strings.Contains(sys, "Main branch") {
		t.Fatalf("blocked templates not neutralized: %q", sys)
	}
	parts := msgs[1].(map[string]any)["content"].([]any)
	txt := parts[0].(map[string]any)["text"].(string)
	if !strings.Contains(txt, "official CLI tool for Claude") {
		t.Fatalf("content-array text not neutralized: %q", txt)
	}
}

func TestForceMaxThinkingSkipsNonHy3(t *testing.T) {
	body := map[string]any{"model": "glm-5.2", "reasoning_effort": "low"}
	forceMaxThinking("glm-5.2", body)
	if body["reasoning_effort"] != "low" {
		t.Fatalf("non-hy3 model was overridden: %v", body["reasoning_effort"])
	}
}

// --- mock upstream ---

type mockUpstream struct {
	srv        *httptest.Server
	state      string
	authURL    string
	loginDone  atomic.Bool
	tokens     map[string]string
	chatBody   atomic.Value
	chatEvents []string
	chatErr    bool
}

func newMockUpstream(t *testing.T) *mockUpstream {
	m := &mockUpstream{state: "st-123", authURL: "https://example.com/login?state=st-123"}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/plugin/auth/state", func(w http.ResponseWriter, r *http.Request) {
		m.write(w, map[string]any{"code": 0, "msg": "", "data": map[string]any{"state": m.state, "authUrl": m.authURL}})
	})
	mux.HandleFunc("/v2/plugin/auth/token", func(w http.ResponseWriter, r *http.Request) {
		if !m.loginDone.Load() {
			m.write(w, map[string]any{"code": CodeLoginInProgress, "msg": "login ing", "data": nil})
			return
		}
		m.write(w, map[string]any{"code": 0, "msg": "", "data": map[string]any{
			"accessToken": "at-1", "refreshToken": "rt-1", "expiresIn": 3600, "domain": "d",
		}})
	})
	mux.HandleFunc("/v2/plugin/login/account", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		m.write(w, map[string]any{"code": 0, "msg": "", "data": map[string]any{
			"uid": "u-1", "enterpriseId": "e-1", "nickname": "n-1",
		}})
	})
	mux.HandleFunc("/v2/plugin/auth/token/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Refresh-Source") != "workbuddy" {
			t.Errorf("refresh missing X-Auth-Refresh-Source header")
		}
		if r.Header.Get("X-Refresh-Token") != "rt-1" {
			t.Errorf("refresh missing X-Refresh-Token header")
		}
		m.write(w, map[string]any{"code": 0, "msg": "", "data": map[string]any{
			"accessToken": "at-2", "refreshToken": "rt-2", "expiresIn": 3600, "domain": "d",
		}})
	})
	mux.HandleFunc("/v2/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		m.chatBody.Store(string(raw))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if s, _ := body["stream"].(bool); !s {
			t.Errorf("upstream received stream=false")
		}
		if h := r.Header.Get("X-Product"); h != "SaaS" {
			t.Errorf("missing X-Product: %q", h)
		}
		if m.chatErr {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream exploded"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		for _, ev := range m.chatEvents {
			_, _ = io.WriteString(w, ev)
		}
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockUpstream) write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockUpstream) url() string { return m.srv.URL }

func (m *mockUpstream) withClient(t *testing.T) (*OAuth, *ChatClient) {
	o := NewOAuth()
	o.Base = m.url()
	c := NewChatClient()
	c.Base = m.url()
	return o, c
}

// --- OAuth flows ---

func TestLoginFlow(t *testing.T) {
	m := newMockUpstream(t)
	o, _ := m.withClient(t)
	ctx := context.Background()

	sess, err := o.StartLogin(ctx)
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	if sess.State != "st-123" || sess.AuthURL != "https://example.com/login?state=st-123" {
		t.Fatalf("bad session: %+v", sess)
	}

	if _, err := o.PollLogin(ctx, sess); err != ErrLoginPending {
		t.Fatalf("want ErrLoginPending, got %v", err)
	}

	m.loginDone.Store(true)
	a, err := o.PollLogin(ctx, sess)
	if err != nil {
		t.Fatalf("PollLogin: %v", err)
	}
	if a.Auth.AccessToken != "at-1" || a.Account.UID != "u-1" || a.Account.EnterpriseID != "e-1" {
		t.Fatalf("bad auth: %+v", a)
	}
	if a.Auth.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("expiresAt not set forward: %d", a.Auth.ExpiresAt)
	}

	refreshed, err := o.Refresh(ctx, a)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.Auth.AccessToken != "at-2" || refreshed.Auth.RefreshToken != "rt-2" {
		t.Fatalf("bad refresh: %+v", refreshed.Auth)
	}
}

// --- chat streaming ---

func TestChatStream(t *testing.T) {
	m := newMockUpstream(t)
	m.chatEvents = []string{
		"data: {\"id\":\"c1\",\"model\":\"hy3-preview\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking...\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"prompt_cache_hit_tokens\":0,\"prompt_cache_miss_tokens\":10}}\n\n",
		"data: [DONE]\n\n",
	}
	_, c := m.withClient(t)
	a := &Auth{Auth: StoredTokens{AccessToken: "at-1"}}
	ctx := context.Background()

	resp, err := c.DoChat(ctx, a, nil, map[string]any{
		"model":    "hy3-preview",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":   true,
	})
	if err != nil {
		t.Fatalf("DoChat: %v", err)
	}
	defer resp.Body.Close()

	var chunks []string
	usage, model, err := PumpStream(ctx, resp.Body, func(data []byte) error {
		chunks = append(chunks, string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("PumpStream: %v", err)
	}
	if len(chunks) != 4 {
		t.Fatalf("want 4 chunks, got %d: %v", len(chunks), chunks)
	}
	if model != "hy3-preview" {
		t.Fatalf("model not captured: %q", model)
	}
	var u map[string]any
	if err := json.Unmarshal(usage, &u); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if u["prompt_cache_hit_tokens"].(float64) != 0 {
		t.Fatalf("usage passthrough broken: %v", u)
	}
}

func TestChatNonStreamAggregates(t *testing.T) {
	m := newMockUpstream(t)
	m.chatEvents = []string{
		"data: {\"id\":\"c1\",\"model\":\"hy3-preview\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking...\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10}}\n\n",
		"data: [DONE]\n\n",
	}
	_, c := m.withClient(t)
	a := &Auth{Auth: StoredTokens{AccessToken: "at-1"}}
	ctx := context.Background()

	resp, err := c.DoChat(ctx, a, nil, map[string]any{"model": "hy3-preview", "messages": []any{}, "stream": false})
	if err != nil {
		t.Fatalf("DoChat: %v", err)
	}
	defer resp.Body.Close()
	comp, err := AggregateCompletion(resp.Body, "fallback-model", nil)
	if err != nil {
		t.Fatalf("AggregateCompletion: %v", err)
	}
	if comp.Choices[0].Message.Content != "Hello" {
		t.Fatalf("content mismatch: %q", comp.Choices[0].Message.Content)
	}
	if comp.Choices[0].Message.ReasoningContent != "thinking..." {
		t.Fatalf("reasoning mismatch: %q", comp.Choices[0].Message.ReasoningContent)
	}
	if comp.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason mismatch: %q", comp.Choices[0].FinishReason)
	}
	if comp.Model != "hy3-preview" {
		t.Fatalf("model mismatch: %q", comp.Model)
	}
}

func TestChatUpstreamError(t *testing.T) {
	m := newMockUpstream(t)
	m.chatErr = true
	_, c := m.withClient(t)
	a := &Auth{Auth: StoredTokens{AccessToken: "at-1"}}
	_, err := c.DoChat(context.Background(), a, nil, map[string]any{"model": "hy3-preview", "messages": []any{}})
	if err == nil || !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("want upstream error, got %v", err)
	}
}

// --- rotation ---

func TestRoundRobin(t *testing.T) {
	dir := t.TempDir()
	paths := []string{"a.json", "b.json"}
	for i, p := range paths {
		if err := (&Auth{Auth: StoredTokens{AccessToken: fmt.Sprintf("at-%d", i)}}).Save(filepath.Join(dir, p)); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(StrategyRoundRobin)
	if err := m.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	var seen []string
	for i := 0; i < 4; i++ {
		acct, err := m.Pick("")
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, acct.Auth.Auth.AccessToken)
	}
	want := []string{"at-0", "at-1", "at-0", "at-1"}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("rotation mismatch: %v", seen)
		}
	}
}

func TestAffinityKeepsAccount(t *testing.T) {
	dir := t.TempDir()
	for i, p := range []string{"a.json", "b.json"} {
		if err := (&Auth{Auth: StoredTokens{AccessToken: fmt.Sprintf("at-%d", i)}}).Save(filepath.Join(dir, p)); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(StrategyRoundRobin)
	if err := m.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	first, err := m.Pick("session-1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		acct, err := m.Pick("session-1")
		if err != nil {
			t.Fatal(err)
		}
		if acct.Path != first.Path {
			t.Fatalf("affinity broken: got %s want %s", acct.Path, first.Path)
		}
	}
}

func TestSessionKeyFromBody(t *testing.T) {
	body := map[string]any{"metadata": map[string]any{"conversation_id": "conv-9"}}
	if got := SessionKeyFromBody(body); got != "conv-9" {
		t.Fatalf("got %q", got)
	}
	body = map[string]any{"prompt_cache_key": "pck-1"}
	if got := SessionKeyFromBody(body); got != "pck-1" {
		t.Fatalf("got %q", got)
	}
}

// --- auth file round trip ---

func TestAuthFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb", "workbuddy.json")
	a := &Auth{Auth: StoredTokens{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 123, Domain: "d"},
		Account: StoredAccount{UID: "u", EnterpriseID: "e", Nickname: "n"}}
	if err := a.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Auth.AccessToken != "at" || got.Account.EnterpriseID != "e" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

// --- SSE scanner edge cases ---

func TestScanSSEEdgeCases(t *testing.T) {
	input := "event: ping\ndata: data: {\"a\":1}\n\n\ndata:\n\ndata: [DONE]\n\n"
	var got []string
	err := ScanSSE(strings.NewReader(input), func(data []byte) error {
		got = append(got, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "{\"a\":1}" {
		t.Fatalf("nested data: parse failed: %v", got)
	}
}
