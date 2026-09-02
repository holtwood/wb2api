package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// Upstream endpoints and client fingerprints (confirmed by reading the MIT
// reference implementation; see notes/workbuddy-protocol-notes.md).
const (
	UpstreamBase = "https://copilot.tencent.com"
	// ClientUA matches the current official CodeBuddy CLI version as of
	// 2026-09-02 (captured live). WorkBuddy/2.63.2 was the reference
	// implementation's hardcoded value but is outdated.
	ClientUA = "CLI/2.143.0 CodeBuddy/2.143.0"
	// IdeVersion is the client version advertised via X-IDE-Version.
	IdeVersion = "2.143.0"
	OriginRef  = "https://www.codebuddy.cn"

	endpointAuthState    = "/v2/plugin/auth/state?platform=CLI"
	endpointLoginAcct    = "/v2/plugin/login/account?state="
	endpointAuthToken    = "/v2/plugin/auth/token?state="
	endpointTokenRefresh = "/v2/plugin/auth/token/refresh"
	endpointChat         = "/v2/chat/completions"
)

// Upstream error codes observed in the reference implementation.
const (
	CodeNonStreamRejected = 11101 // upstream rejects non-stream chat requests
	CodeLoginInProgress   = 11217 // auth/token polling while login pending
)

// Common HTTP error types surfaced to callers.
var (
	ErrLoginPending = errors.New("workbuddy: login still pending")
	ErrUpstream     = errors.New("workbuddy: upstream error")
)

// apiEnvelope is the uniform {code,msg,data} upstream envelope.
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type authStateData struct {
	State   string `json:"state"`
	AuthURL string `json:"authUrl"`
}

// tokenData is the wire form of a token bundle (expiresIn is relative seconds).
type tokenData struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresIn        int64  `json:"expiresIn"`
	RefreshExpiresIn int64  `json:"refreshExpiresIn"`
	Domain           string `json:"domain"`
}

type accountData struct {
	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`
}

// OAuth drives the WorkBuddy login/refresh flows.
type OAuth struct {
	Base string
	UA   string
	HTTP *http.Client
}

// NewOAuth returns an OAuth client against the production upstream.
func NewOAuth() *OAuth {
	return &OAuth{
		Base: UpstreamBase,
		UA:   ClientUA,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		},
	}
}

// commonHeaders returns the standard header set shared by all upstream calls.
func (o *OAuth) commonHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("Origin", OriginRef)
	h.Set("Referer", OriginRef+"/")
	h.Set("User-Agent", o.UA)
	return h
}

// doJSON performs a request and decodes the {code,msg,data} envelope.
// It returns the decoded data object on code==0.
func (o *OAuth) doJSON(ctx context.Context, c *http.Client, method, url string, h http.Header, body io.Reader) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if h == nil {
		h = o.commonHeaders()
	}
	for k, vs := range h {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		msg := string(raw)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("%w: http %d: %s", ErrUpstream, resp.StatusCode, msg)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("workbuddy: bad envelope: %w", err)
	}
	if env.Code != 0 {
		if env.Code == CodeLoginInProgress {
			return nil, ErrLoginPending
		}
		return nil, fmt.Errorf("%w: code=%d msg=%s", ErrUpstream, env.Code, env.Msg)
	}
	return env.Data, nil
}

// LoginSession is one in-flight login: the state to poll and the cookie jar
// client associated with it.
type LoginSession struct {
	State   string
	AuthURL string
	Expires time.Time
	client  *http.Client
}

// StartLogin POSTs /v2/plugin/auth/state and returns a session whose AuthURL
// the user opens (browser scan / account login).
func (o *OAuth) StartLogin(ctx context.Context) (*LoginSession, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	data, err := o.doJSON(ctx, client, http.MethodPost, o.Base+endpointAuthState, nil, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	var as authStateData
	if err := json.Unmarshal(data, &as); err != nil {
		return nil, fmt.Errorf("workbuddy: parse auth state: %w", err)
	}
	if as.State == "" {
		return nil, errors.New("workbuddy: empty login state")
	}
	return &LoginSession{
		State:   as.State,
		AuthURL: as.AuthURL,
		Expires: time.Now().Add(5 * time.Minute),
		client:  client,
	}, nil
}

// PollLogin checks the login status. It returns ErrLoginPending while the
// user has not finished authorizing, and a fully populated Auth on success.
func (o *OAuth) PollLogin(ctx context.Context, s *LoginSession) (*Auth, error) {
	data, err := o.doJSON(ctx, s.client, http.MethodGet, o.Base+endpointAuthToken+s.State, nil, nil)
	if err != nil {
		if errors.Is(err, ErrLoginPending) {
			return nil, ErrLoginPending
		}
		return nil, err
	}
	var td tokenData
	if err := json.Unmarshal(data, &td); err != nil {
		return nil, fmt.Errorf("workbuddy: parse token: %w", err)
	}
	if td.AccessToken == "" {
		return nil, ErrLoginPending
	}
	a := &Auth{
		Auth: StoredTokens{
			AccessToken:  td.AccessToken,
			RefreshToken: td.RefreshToken,
			Domain:       td.Domain,
		},
	}
	if td.ExpiresIn > 0 {
		a.Auth.ExpiresAt = time.Now().Unix() + td.ExpiresIn
	}
	// Account info only becomes reachable once the bearer token exists
	// (login/account sits behind an openresty gateway that 401s before that).
	if ad, err := o.doJSON(ctx, s.client, http.MethodGet, o.Base+endpointLoginAcct+s.State, bearerHeader(td.AccessToken), nil); err == nil {
		var acc accountData
		if err := json.Unmarshal(ad, &acc); err == nil {
			a.Account = StoredAccount{UID: acc.UID, EnterpriseID: acc.EnterpriseID, Nickname: acc.Nickname}
		}
	}
	return a, nil
}

// Refresh rotates the access token via /v2/plugin/auth/token/refresh.
func (o *OAuth) Refresh(ctx context.Context, a *Auth) (*Auth, error) {
	h := o.commonHeaders()
	h.Set("X-Refresh-Token", a.Auth.RefreshToken)
	h.Set("X-Auth-Refresh-Source", "workbuddy")
	if a.Account.EnterpriseID != "" {
		h.Set("X-Enterprise-Id", a.Account.EnterpriseID)
	}
	data, err := o.doJSON(ctx, o.HTTP, http.MethodPost, o.Base+endpointTokenRefresh, h, nil)
	if err != nil {
		return nil, err
	}
	var td tokenData
	if err := json.Unmarshal(data, &td); err != nil {
		return nil, fmt.Errorf("workbuddy: parse refresh: %w", err)
	}
	if td.AccessToken == "" {
		return nil, errors.New("workbuddy: refresh returned no access token")
	}
	na := a.Clone()
	na.Auth.AccessToken = td.AccessToken
	if td.RefreshToken != "" {
		na.Auth.RefreshToken = td.RefreshToken
	}
	if td.Domain != "" {
		na.Auth.Domain = td.Domain
	}
	if td.ExpiresIn > 0 {
		na.Auth.ExpiresAt = time.Now().Unix() + td.ExpiresIn
	}
	return na, nil
}

func bearerHeader(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}
