// Command server exposes wb2api core as a standalone OpenAI-compatible HTTP
// service: chat completions (streaming + non-stream), auth login/poll/refresh
// and a model list. It is the development face of the project; the CPA plugin
// form (cmd/plugin) is the primary release target.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"wb2api/core"
)

const provider = "workbuddy"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := env("WB2API_ADDR", "127.0.0.1:8787")
	authDir := env("WB2API_AUTH_DIR", core.AuthDir())
	strategy := core.StrategyRoundRobin
	if env("WB2API_STRATEGY", "roundrobin") == "fillfirst" {
		strategy = core.StrategyFillFirst
	}

	chat := core.NewChatClient()
	oauth := core.NewOAuth()
	mgr := core.NewManager(strategy)
	if err := mgr.LoadDir(authDir); err != nil {
		log.Fatalf("load auth dir: %v", err)
	}

	s := &server{
		chat:    chat,
		oauth:   oauth,
		mgr:     mgr,
		authDir: authDir,
		sessions: map[string]*core.LoginSession{},
		traces:  map[string]*core.TraceCtx{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("POST /v1/auth/login", s.handleLogin)
	mux.HandleFunc("GET /v1/auth/poll", s.handlePoll)
	mux.HandleFunc("POST /v1/auth/refresh", s.handleRefresh)
	mux.HandleFunc("GET /v1/models", s.handleModels)

	log.Printf("wb2api listening on %s (auth dir: %s, %d account(s))", addr, authDir, len(mgr.Accounts()))
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

type server struct {
	chat     *core.ChatClient
	oauth    *core.OAuth
	mgr      *core.Manager
	authDir  string
	sessions map[string]*core.LoginSession
	traces   map[string]*core.TraceCtx
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg}})
}

// handleChat serves OpenAI-format /v1/chat/completions.
func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	model, _ := body["model"].(string)
	stream, _ := body["stream"].(bool)

	acct, err := s.mgr.Pick(core.SessionKeyFromBody(body))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "no account available; run /v1/auth/login first")
		return
	}

	// Stable per-conversation trace context (kept across turns of the same
	// conversation) so the upstream prompt cache stays usable; nil-safe.
	skey := core.SessionKeyFromBody(body)
	if skey == "" {
		skey = model
	}
	tc := s.traces[skey]
	if tc == nil {
		tc = core.NewTraceCtx()
		s.traces[skey] = tc
	}

	resp, err := s.chat.DoChat(r.Context(), acct.Auth, tc, body)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	if stream {
		s.streamChat(w, r.Context(), resp, model)
		return
	}
	comp, err := core.AggregateCompletion(resp.Body, model, nil)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "aggregate: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, comp)
}

func (s *server) streamChat(w http.ResponseWriter, ctx context.Context, resp *http.Response, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fl, _ := w.(http.Flusher)
	write := func(data []byte) error {
		if _, err := w.Write(append([]byte("data: "), append(data, '\n', '\n')...)); err != nil {
			return err
		}
		if fl != nil {
			fl.Flush()
		}
		return nil
	}
	_, _, err := core.PumpStream(ctx, resp.Body, write)
	if err != nil {
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"" + err.Error() + "\"}}\n\n"))
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if fl != nil {
		fl.Flush()
	}
}

// handleLogin starts a WorkBuddy login and returns the URL for the user.
func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	sess, err := s.oauth.StartLogin(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "start login: "+err.Error())
		return
	}
	s.sessions[sess.State] = sess
	writeJSON(w, http.StatusOK, map[string]any{"authUrl": sess.AuthURL, "state": sess.State})
}

// handlePoll checks a login's status and persists the auth on success.
func (s *server) handlePoll(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	sess, ok := s.sessions[state]
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown state")
		return
	}
	a, err := s.oauth.PollLogin(r.Context(), sess)
	if err != nil {
		if errors.Is(err, core.ErrLoginPending) {
			writeJSON(w, http.StatusOK, map[string]any{"pending": true})
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	delete(s.sessions, state)
	path := filepath.Join(s.authDir, provider+".json")
	if err := a.Save(path); err != nil {
		writeErr(w, http.StatusInternalServerError, "save auth: "+err.Error())
		return
	}
	s.mgr.Add(a, path)
	writeJSON(w, http.StatusOK, map[string]any{"pending": false, "nickname": a.Account.Nickname})
}

// handleRefresh rotates tokens for all loaded accounts.
func (s *server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	for _, acct := range s.mgr.Accounts() {
		na, err := s.oauth.Refresh(ctx, acct.Auth)
		if err != nil {
			writeErr(w, http.StatusBadGateway, fmt.Sprintf("refresh %s: %v", acct.Path, err))
			return
		}
		if err := na.Save(acct.Path); err != nil {
			writeErr(w, http.StatusInternalServerError, "save: "+err.Error())
			return
		}
		acct.Auth = na
	}
	writeJSON(w, http.StatusOK, map[string]any{"refreshed": len(s.mgr.Accounts())})
}

func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	// Confirmed against the official CodeBuddy CLI 2.143.0 (2026-09-02):
	//   hy4-preview, hy3, hy3-x, glm-5.3, glm-5.3-flash, glm-5.2, glm-5.1,
	//   glm-5v-turbo, minimax-m3, minimax-m2.7, kimi-k3-1, kimi-k2.7,
	//   kimi-k2.6, deepseek-v4-pro, deepseek-v4-flash
	models := []map[string]any{
		{"id": "hy4-preview", "object": "model", "owned_by": "tencent"},
		{"id": "hy3", "object": "model", "owned_by": "tencent"},
		{"id": "hy3-x", "object": "model", "owned_by": "tencent"},
		{"id": "hy3-preview", "object": "model", "owned_by": "tencent"},
		{"id": "hy3-preview-agent", "object": "model", "owned_by": "tencent"},
		{"id": "glm-5.3", "object": "model", "owned_by": "tencent"},
		{"id": "glm-5.3-flash", "object": "model", "owned_by": "tencent"},
		{"id": "glm-5.2", "object": "model", "owned_by": "tencent"},
		{"id": "glm-5.1", "object": "model", "owned_by": "tencent"},
		{"id": "glm-5v-turbo", "object": "model", "owned_by": "tencent"},
		{"id": "minimax-m3", "object": "model", "owned_by": "tencent"},
		{"id": "minimax-m2.7", "object": "model", "owned_by": "tencent"},
		{"id": "kimi-k3-1", "object": "model", "owned_by": "tencent"},
		{"id": "kimi-k2.7", "object": "model", "owned_by": "tencent"},
		{"id": "kimi-k2.6", "object": "model", "owned_by": "tencent"},
		{"id": "deepseek-v4-pro", "object": "model", "owned_by": "tencent"},
		{"id": "deepseek-v4-flash", "object": "model", "owned_by": "tencent"},
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
}
