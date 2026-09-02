// Package main implements the wb2api CLIProxyAPI (CPA) plugin.
//
// It wraps Tencent CodeBuddy (copilot.tencent.com) as a CPA provider: web
// login flow, token refresh, and OpenAI-compatible chat completions with the
// official client's trace/conversation identifiers injected so the upstream
// shared prompt cache is served (see specs/capture-2026-09-02.md).
//
// All WorkBuddy protocol logic lives in wb2api/core; this file is a thin ABI
// shell with no protocol logic of its own. The C ABI shape follows the
// MIT-licensed reference implementation lovingfish/workbuddy-cliproxy
// (see NOTICE).
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

static int wb_call_host(cliproxy_host_api* api, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return api->call(api->host_ctx, method, request, request_len, response);
}
static void wb_free_host_buffer(cliproxy_host_api* api, void* ptr, size_t len) {
	api->free_buffer(ptr, len);
}

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"wb2api/core"
)

const (
	providerName = "workbuddy"
	authFileName = "workbuddy.json"
	version      = "0.2.0"
	loginTTL     = 5 * time.Minute
)

var (
	hostAPI     *C.cliproxy_host_api
	loginStates sync.Map // state -> *core.LoginSession
	traceCtxs   sync.Map // authStorageHash -> *core.TraceCtx
	oauth       = core.NewOAuth()
	chat        = core.NewChatClient()
)

func main() {}

// -----------------------------------------------------------------------------
// C ABI exports
// -----------------------------------------------------------------------------

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	hostAPI = host
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

// -----------------------------------------------------------------------------
// Host calls (async streaming)
// -----------------------------------------------------------------------------

func hostCall(method string, request []byte) ([]byte, error) {
	if hostAPI == nil || hostAPI.call == nil {
		return nil, fmt.Errorf("host API unavailable")
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var cReq unsafe.Pointer
	var reqLen C.size_t
	if len(request) > 0 {
		cReq = C.CBytes(request)
		defer C.free(cReq)
		reqLen = C.size_t(len(request))
	}
	var resp C.cliproxy_buffer
	rc := C.wb_call_host(hostAPI, cMethod, (*C.uint8_t)(cReq), reqLen, &resp)
	var out []byte
	if resp.ptr != nil && resp.len > 0 {
		out = C.GoBytes(resp.ptr, C.int(resp.len))
	}
	if resp.ptr != nil && hostAPI.free_buffer != nil {
		C.wb_free_host_buffer(hostAPI, resp.ptr, resp.len)
	}
	if rc != 0 {
		return out, fmt.Errorf("host call %s returned %d", method, int(rc))
	}
	return out, nil
}

func streamEmit(streamID string, payload []byte) error {
	if streamID == "" {
		return fmt.Errorf("no stream id")
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "payload": payload})
	_, err := hostCall(pluginabi.MethodHostStreamEmit, body)
	return err
}

func streamEmitError(streamID, message string) {
	if streamID == "" {
		return
	}
	errJSON, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	_ = streamEmit(streamID, errJSON)
}

func streamClose(streamID string) {
	if streamID == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"stream_id": streamID})
	_, _ = hostCall(pluginabi.MethodHostStreamClose, body)
}

// -----------------------------------------------------------------------------
// RPC dispatch
// -----------------------------------------------------------------------------

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodModelStatic, pluginabi.MethodModelForAuth:
		return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: modelList()})
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})
	case pluginabi.MethodAuthParse:
		return handleParseAuth(request)
	case pluginabi.MethodAuthLoginStart:
		return handleStartLogin(request)
	case pluginabi.MethodAuthLoginPoll:
		return handlePollLogin(request)
	case pluginabi.MethodAuthRefresh:
		return handleRefreshAuth(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})
	case pluginabi.MethodExecutorExecute:
		return handleExecExecute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return handleExecStream(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// -----------------------------------------------------------------------------
// Envelopes, registration, models
// -----------------------------------------------------------------------------

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
}

type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

func okEnvelope(result any) ([]byte, error) {
	raw, _ := json.Marshal(result)
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, data []byte) {
	if response == nil || len(data) == 0 {
		return
	}
	ptr := C.CBytes(data)
	response.ptr = ptr
	response.len = C.size_t(len(data))
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             providerName,
			Version:          version,
			Author:           "wb2api (protocol reference: lovingfish/workbuddy-cliproxy, MIT)",
			GitHubRepository: "https://github.com/holtwood/wb2api",
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats:  []string{"chat-completions"},
			ExecutorOutputFormats: []string{"chat-completions"},
		},
	}
}

func modelList() []pluginapi.ModelInfo {
	// Official model table lives in wb2api/core (protocol fact); adapt it to
	// the CPA model info shape here.
	models := make([]pluginapi.ModelInfo, 0, len(core.Models))
	for _, m := range core.Models {
		models = append(models, pluginapi.ModelInfo{
			ID:                         m.ID,
			Object:                     "model",
			OwnedBy:                    providerName,
			DisplayName:                m.Name,
			Name:                       m.ID,
			SupportedGenerationMethods: []string{"chat"},
			ContextLength:              m.Ctx,
			MaxCompletionTokens:        core.MaxCompletionTokens,
			UserDefined:                true,
		})
	}
	return models
}

// -----------------------------------------------------------------------------
// Auth handlers
// -----------------------------------------------------------------------------

func parseStored(raw []byte) (*core.Auth, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty auth storage")
	}
	var a core.Auth
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("storage_parse_error: %w", err)
	}
	if a.Auth.AccessToken == "" {
		return nil, fmt.Errorf("parse_error: missing accessToken")
	}
	return &a, nil
}

func toAuthData(a *core.Auth) pluginapi.AuthData {
	storage, _ := json.Marshal(a)
	return pluginapi.AuthData{
		Provider:    providerName,
		ID:          providerName,
		FileName:    authFileName,
		Label:       "WorkBuddy",
		StorageJSON: storage,
		Metadata:    map[string]any{"type": providerName},
	}
}

func handleParseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	a, err := parseStored(req.RawJSON)
	if err != nil {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: toAuthData(a)})
}

func handleStartLogin(raw []byte) ([]byte, error) {
	sess, err := oauth.StartLogin(context.Background())
	if err != nil {
		return nil, fmt.Errorf("auth state failed: %w", err)
	}
	loginStates.Store(sess.State, sess)
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerName,
		URL:       sess.AuthURL,
		State:     sess.State,
		ExpiresAt: sess.Expires,
	})
}

func handlePollLogin(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	state := strings.TrimSpace(req.State)
	if state == "" {
		return nil, fmt.Errorf("poll: empty state")
	}
	v, ok := loginStates.Load(state)
	if !ok {
		return nil, fmt.Errorf("poll: unknown state (restart login)")
	}
	sess := v.(*core.LoginSession)
	if time.Now().After(sess.Expires) {
		loginStates.Delete(state)
		return nil, fmt.Errorf("poll: login expired")
	}
	a, err := oauth.PollLogin(context.Background(), sess)
	if err != nil {
		loginStates.Delete(state)
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "waiting for login",
		})
	}
	loginStates.Delete(state)
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status: pluginapi.AuthLoginStatusSuccess,
		Auth:   toAuthData(a),
	})
}

func handleRefreshAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	a, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	na, err := oauth.Refresh(context.Background(), a)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: toAuthData(na)})
}

// -----------------------------------------------------------------------------
// Executor handlers
// -----------------------------------------------------------------------------

func requestBody(req pluginapi.ExecutorRequest) (map[string]any, error) {
	var body map[string]any
	raw := req.Payload
	if len(raw) == 0 {
		raw = req.OriginalRequest
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// traceForAuth returns the account-stable trace context (keyed by storage
// hash) so conversation identifiers stay constant across turns.
func traceForAuth(a *core.Auth) *core.TraceCtx {
	h := sha256.Sum256([]byte(a.Auth.AccessToken + a.Account.UID))
	key := hex.EncodeToString(h[:8])
	if v, ok := traceCtxs.Load(key); ok {
		return v.(*core.TraceCtx)
	}
	tc := core.NewTraceCtx()
	traceCtxs.Store(key, tc)
	return tc
}

func handleExecExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	a, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	body, err := requestBody(req)
	if err != nil {
		return nil, err
	}
	// Upstream rejects non-stream (code 11101): always stream and aggregate.
	resp, err := chat.DoChat(context.Background(), a, traceForAuth(a), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	completion, err := core.AggregateCompletion(resp.Body, req.Model, nil)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(completion)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: payload})
}

// executorStreamRequest wraps the host's executor.execute_stream RPC.
type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func handleExecStream(raw []byte) ([]byte, error) {
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	a, err := parseStored(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	body, err := requestBody(req.ExecutorRequest)
	if err != nil {
		return nil, err
	}
	headers := streamHeaders()
	sseFramed := clientNeedsSSEFrame(req.Metadata)
	tc := traceForAuth(a)

	if req.StreamID == "" {
		chunks, err := collectStream(body, a, tc, sseFramed)
		if err != nil {
			return nil, err
		}
		return okEnvelope(streamResponse{Headers: headers, Chunks: chunks})
	}

	// Async: build the request now, pump in a goroutine via host.stream.emit.
	httpReq, err := chat.BuildRequest(context.Background(), a, tc, body)
	if err != nil {
		streamEmitError(req.StreamID, err.Error())
		streamClose(req.StreamID)
		return okEnvelope(streamResponse{Headers: headers})
	}
	go pumpStream(httpReq, req.StreamID, sseFramed)
	return okEnvelope(streamResponse{Headers: headers})
}

func streamHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	return h
}

func pumpStream(httpReq *http.Request, streamID string, sseFramed bool) {
	resp, err := chat.HTTP.Do(httpReq)
	if err != nil {
		streamEmitError(streamID, fmt.Sprintf("http_error: %v", err))
		streamClose(streamID)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		errPayload, _ := io.ReadAll(resp.Body)
		streamEmitError(streamID, fmt.Sprintf("upstream %d: %s", resp.StatusCode, truncate(string(errPayload), 200)))
		streamClose(streamID)
		return
	}
	_, _, _ = core.PumpStream(context.Background(), resp.Body, func(data []byte) error {
		cleaned := core.CleanChunk(data)
		if sseFramed {
			cleaned = append([]byte("data: "), cleaned...)
		}
		if err := streamEmit(streamID, cleaned); err != nil {
			return err
		}
		return nil
	})
	streamClose(streamID)
}

func collectStream(body map[string]any, a *core.Auth, tc *core.TraceCtx, sseFramed bool) ([]pluginapi.ExecutorStreamChunk, error) {
	httpReq, err := chat.BuildRequest(context.Background(), a, tc, body)
	if err != nil {
		return nil, err
	}
	resp, err := chat.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http_error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		errPayload, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, truncate(string(errPayload), 200))
	}
	var chunks []pluginapi.ExecutorStreamChunk
	_, _, err = core.PumpStream(context.Background(), resp.Body, func(data []byte) error {
		cleaned := core.CleanChunk(data)
		if sseFramed {
			cleaned = append([]byte("data: "), cleaned...)
		}
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: cleaned})
		return nil
	})
	return chunks, err
}

// clientNeedsSSEFrame reports whether chunk payloads must carry their own
// "data: " framing (cross-format translators consume framed lines only).
func clientNeedsSSEFrame(metadata map[string]any) bool {
	path, _ := metadata["request_path"].(string)
	switch strings.ToLower(strings.TrimSpace(path)) {
	case "/v1/chat/completions", "/v1/completions":
		return false
	default:
		return true
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
