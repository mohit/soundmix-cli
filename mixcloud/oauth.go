package mixcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// OAuthConfig holds the credentials of a Mixcloud application, created at
// https://www.mixcloud.com/developers/create/.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	// RedirectURI is optional. When empty, Mixcloud shows the authorization
	// code on screen for the user to copy back into your app.
	RedirectURI string
}

// AuthorizeURL returns the URL to send the user to so they can grant your
// application access to their account.
func (c *Client) AuthorizeURL(cfg OAuthConfig) string {
	q := url.Values{"client_id": {cfg.ClientID}}
	if cfg.RedirectURI != "" {
		q.Set("redirect_uri", cfg.RedirectURI)
	}
	u := c.oauthURL.ResolveReference(&url.URL{Path: "authorize"})
	u.RawQuery = q.Encode()
	return u.String()
}

// ExchangeCode swaps the code returned by the authorize step for an access
// token. The RedirectURI must match the one used in AuthorizeURL.
func (c *Client) ExchangeCode(ctx context.Context, cfg OAuthConfig, code string) (string, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return "", errors.New("mixcloud: client ID and client secret are required")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", errors.New("mixcloud: authorization code is empty")
	}
	q := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {code},
	}
	if cfg.RedirectURI != "" {
		q.Set("redirect_uri", cfg.RedirectURI)
	}
	u := c.oauthURL.ResolveReference(&url.URL{Path: "access_token"})
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", redactErr(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("mixcloud: reading token response: %w", err)
	}
	if apiErr := parseError(resp, body); apiErr != nil {
		return "", apiErr
	}
	return parseTokenResponse(body)
}

// parseTokenResponse accepts either a JSON body ({"access_token": "..."}) or
// a form-encoded one (access_token=...).
func parseTokenResponse(body []byte) (string, error) {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "{") {
		var v struct {
			AccessToken string `json:"access_token"`
			Error       any    `json:"error"`
		}
		if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
			return "", fmt.Errorf("mixcloud: decoding token response: %w", err)
		}
		if v.AccessToken != "" {
			return v.AccessToken, nil
		}
		return "", fmt.Errorf("mixcloud: token exchange failed: %v", v.Error)
	}
	vals, err := url.ParseQuery(trimmed)
	if err == nil && vals.Get("access_token") != "" {
		return vals.Get("access_token"), nil
	}
	if err == nil && vals.Get("error") != "" {
		return "", fmt.Errorf("mixcloud: token exchange failed: %s", vals.Get("error"))
	}
	return "", errors.New("mixcloud: token response did not contain an access_token")
}
