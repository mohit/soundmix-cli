package soundcloud

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuthConfig holds a SoundCloud app's credentials. Creating an app requires
// SoundCloud Artist Pro; the users who authorize it can be on any plan.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	// RedirectURI must exactly match one registered on the app.
	RedirectURI string
}

// Token is an OAuth token pair.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
	Scope        string    `json:"scope,omitempty"`
}

// Fresh reports whether the access token is set and won't expire within
// the next two minutes. A zero Expiry counts as fresh.
func (t Token) Fresh(now time.Time) bool {
	return t.AccessToken != "" && (t.Expiry.IsZero() || now.Add(2*time.Minute).Before(t.Expiry))
}

// PKCE holds the per-login secrets for the authorization code flow.
type PKCE struct {
	Verifier  string `json:"verifier"`
	Challenge string `json:"challenge"`
	State     string `json:"state"`
}

// NewPKCE generates a code verifier, its S256 challenge and a CSRF state.
func NewPKCE() (PKCE, error) {
	v, err := randomString(32)
	if err != nil {
		return PKCE{}, err
	}
	s, err := randomString(16)
	if err != nil {
		return PKCE{}, err
	}
	sum := sha256.Sum256([]byte(v))
	return PKCE{Verifier: v, Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), State: s}, nil
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthorizeURL returns the URL where the user approves access.
func (c *Client) AuthorizeURL(cfg OAuthConfig, p PKCE) string {
	q := url.Values{
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {cfg.RedirectURI},
		"response_type":         {"code"},
		"code_challenge":        {p.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {p.State},
	}
	u := c.authURL.ResolveReference(&url.URL{Path: "authorize"})
	u.RawQuery = q.Encode()
	return u.String()
}

// ExchangeCode swaps an authorization code for tokens.
func (c *Client) ExchangeCode(ctx context.Context, cfg OAuthConfig, code, verifier string) (Token, error) {
	if err := cfg.check(); err != nil {
		return Token{}, err
	}
	if strings.TrimSpace(code) == "" {
		return Token{}, errors.New("soundcloud: authorization code is empty")
	}
	return c.postToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"redirect_uri":  {cfg.RedirectURI},
		"code_verifier": {verifier},
		"code":          {strings.TrimSpace(code)},
	})
}

// Refresh exchanges a refresh token for a new token pair. Refresh tokens are
// single-use: persist the returned token before doing anything else.
func (c *Client) Refresh(ctx context.Context, cfg OAuthConfig, refreshToken string) (Token, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return Token{}, errors.New("soundcloud: client ID and client secret are required to refresh the token")
	}
	if refreshToken == "" {
		return Token{}, &Error{StatusCode: http.StatusUnauthorized, Message: "access token expired and there is no refresh token; log in again"}
	}
	return c.postToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"refresh_token": {refreshToken},
	})
}

// Disconnect revokes this app's tokens for the authorized user.
func (c *Client) Disconnect(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, c.endpoint("disconnect", nil), nil, nil)
}

func (cfg OAuthConfig) check() error {
	var missing []string
	if cfg.ClientID == "" {
		missing = append(missing, "client ID")
	}
	if cfg.ClientSecret == "" {
		missing = append(missing, "client secret")
	}
	if cfg.RedirectURI == "" {
		missing = append(missing, "redirect URI")
	}
	if len(missing) > 0 {
		return fmt.Errorf("soundcloud: %s required", strings.Join(missing, ", "))
	}
	return nil
}

func (c *Client) postToken(ctx context.Context, form url.Values) (Token, error) {
	u := c.authURL.ResolveReference(&url.URL{Path: "oauth/token"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, fmt.Errorf("soundcloud: reading token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Token{}, parseError(resp, data)
	}
	var v struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return Token{}, fmt.Errorf("soundcloud: decoding token response: %w", err)
	}
	if v.AccessToken == "" {
		return Token{}, errors.New("soundcloud: token response did not contain an access_token")
	}
	t := Token{AccessToken: v.AccessToken, RefreshToken: v.RefreshToken, Scope: v.Scope}
	if v.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(v.ExpiresIn) * time.Second).UTC()
	}
	return t, nil
}

// NewRefreshingTokenSource returns an in-memory TokenSource that refreshes
// tok when it is about to expire (or after a 401), calling onRefresh with
// each new token so it can be persisted. refresher is usually the same
// Client the source is used with.
func NewRefreshingTokenSource(refresher *Client, cfg OAuthConfig, tok Token, onRefresh func(Token) error) TokenSource {
	return &refreshingSource{c: refresher, cfg: cfg, tok: tok, onRefresh: onRefresh}
}

type refreshingSource struct {
	mu        sync.Mutex
	c         *Client
	cfg       OAuthConfig
	tok       Token
	onRefresh func(Token) error
}

func (s *refreshingSource) Token(ctx context.Context, force bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && s.tok.Fresh(s.c.now()) {
		return s.tok.AccessToken, nil
	}
	t, err := s.c.Refresh(ctx, s.cfg, s.tok.RefreshToken)
	if err != nil {
		return "", err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = s.tok.RefreshToken
	}
	s.tok = t
	if s.onRefresh != nil {
		if err := s.onRefresh(t); err != nil {
			return "", fmt.Errorf("soundcloud: saving refreshed token: %w", err)
		}
	}
	return t.AccessToken, nil
}
