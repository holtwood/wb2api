// Package core implements the WorkBuddy/CodeBuddy upstream protocol:
// OAuth login & refresh, multi-account rotation and OpenAI-format chat
// completions (SSE streaming + non-stream aggregation).
//
// It must stay free of any CLIProxyAPI dependency and remain testable
// offline against a mocked upstream.
package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// StoredTokens is the token half of a WorkBuddy auth JSON file.
type StoredTokens struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"` // unix seconds; 0 = unknown
	Domain       string `json:"domain"`
}

// StoredAccount is the account half of a WorkBuddy auth JSON file.
type StoredAccount struct {
	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
}

// Auth is one account's persisted credentials, matching the on-disk layout
// used by the reference client (workbuddy.json).
type Auth struct {
	Auth    StoredTokens  `json:"auth"`
	Account StoredAccount `json:"account"`
}

// Expired reports whether the access token is known and past expiry.
func (a *Auth) Expired() bool {
	return a.Auth.ExpiresAt > 0 && time.Now().Unix() >= a.Auth.ExpiresAt
}

// Clone returns a deep copy safe to mutate.
func (a *Auth) Clone() *Auth {
	if a == nil {
		return nil
	}
	c := *a
	return &c
}

// Save writes the auth JSON with restrictive permissions.
func (a *Auth) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// LoadAuth reads an auth JSON file.
func LoadAuth(path string) (*Auth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a Auth
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// AuthDir returns the default auth directory (~/.wb2api).
func AuthDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".wb2api"
	}
	return filepath.Join(home, ".wb2api")
}

// DefaultAuthPath returns the default auth file for the given provider.
func DefaultAuthPath(provider string) string {
	return filepath.Join(AuthDir(), provider+".json")
}
