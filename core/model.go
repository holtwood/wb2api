package core

// Model is one upstream model from the official CodeBuddy CLI model list
// (confirmed against CodeBuddy CLI 2.143.0 on 2026-09-02).
type Model struct {
	ID   string // upstream model id
	Name string // display name
	Ctx  int64  // context window, tokens
}

// MaxCompletionTokens is the completion cap applied to every model.
const MaxCompletionTokens int64 = 8192

// Models is the official upstream model table, kept in core as protocol fact
// so both cmd/server and cmd/plugin advertise the same list.
var Models = []Model{
	{"hy4-preview", "Hy4 Preview", 262144},
	{"hy3", "Hy3", 262144},
	{"hy3-x", "Hy3 X", 262144},
	{"hy3-preview", "Hy3 Preview", 262144},
	{"hy3-preview-agent", "Hy3 Preview Agent", 262144},
	{"glm-5.3", "GLM-5.3", 1000000},
	{"glm-5.3-flash", "GLM-5.3 Flash", 1000000},
	{"glm-5.2", "GLM-5.2", 1000000},
	{"glm-5.1", "GLM-5.1", 131072},
	{"glm-5v-turbo", "GLM-5V Turbo", 131072},
	{"minimax-m3", "MiniMax M3", 204800},
	{"minimax-m2.7", "MiniMax M2.7", 204800},
	{"kimi-k3-1", "Kimi K3.1", 262144},
	{"kimi-k2.7", "Kimi K2.7", 262144},
	{"kimi-k2.6", "Kimi K2.6", 262144},
	{"deepseek-v4-pro", "DeepSeek V4 Pro", 1000000},
	{"deepseek-v4-flash", "DeepSeek V4 Flash", 1000000},
}
