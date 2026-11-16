// Package soundcloud is a client for the SoundCloud HTTP API
// (https://developers.soundcloud.com/docs/api/guide), focused on uploading
// and managing a user's tracks and playlists.
//
// Authentication is OAuth 2.1 with PKCE (see NewPKCE, AuthorizeURL,
// ExchangeCode). Access tokens last about an hour; give the client a
// TokenSource that refreshes them (NewRefreshingTokenSource) for anything
// longer-lived than a single command.
package soundcloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the root of the SoundCloud API.
	DefaultBaseURL = "https://api.soundcloud.com/"
	// DefaultAuthURL is the root of SoundCloud's OAuth endpoints.
	DefaultAuthURL = "https://secure.soundcloud.com/"

	defaultUserAgent = "soundmix-cli-go/2"
	maxRetries       = 3
)

// TokenSource supplies access tokens. force is true after the API rejected
// the previous token, asking for a refreshed one.
type TokenSource interface {
	Token(ctx context.Context, force bool) (string, error)
}

type staticToken string

func (s staticToken) Token(context.Context, bool) (string, error) { return string(s), nil }

// Client talks to the SoundCloud API. Create one with New.
type Client struct {
	baseURL          *url.URL
	authURL          *url.URL
	httpClient       *http.Client
	tokens           TokenSource
	userAgent        string
	maxRateLimitWait time.Duration
	sleep            func(context.Context, time.Duration) error
	now              func() time.Time
}

// Option configures a Client.
type Option func(*Client) error

// WithAccessToken uses a fixed access token (no refreshing).
func WithAccessToken(token string) Option {
	return func(c *Client) error {
		if token != "" {
			c.tokens = staticToken(token)
		}
		return nil
	}
}

// WithTokenSource supplies access tokens from ts, e.g. one made by
// NewRefreshingTokenSource.
func WithTokenSource(ts TokenSource) Option {
	return func(c *Client) error { c.tokens = ts; return nil }
}

// WithHTTPClient replaces the HTTP client. Don't set a short overall Timeout
// on a client used for uploads.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) error {
		if h == nil {
			return errors.New("soundcloud: nil http client")
		}
		c.httpClient = h
		return nil
	}
}

// WithBaseURL overrides the API root (useful for tests).
func WithBaseURL(raw string) Option {
	return func(c *Client) error {
		u, err := parseRoot(raw)
		c.baseURL = u
		return err
	}
}

// WithAuthURL overrides the OAuth root (useful for tests).
func WithAuthURL(raw string) Option {
	return func(c *Client) error {
		u, err := parseRoot(raw)
		c.authURL = u
		return err
	}
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) error { c.userAgent = ua; return nil }
}

// WithRateLimitRetry makes the client wait and retry (up to 3 times) after a
// 429 whose Retry-After is no longer than maxWait. Uploads are never retried.
func WithRateLimitRetry(maxWait time.Duration) Option {
	return func(c *Client) error { c.maxRateLimitWait = maxWait; return nil }
}

// New returns a configured Client.
func New(opts ...Option) (*Client, error) {
	base, _ := url.Parse(DefaultBaseURL)
	auth, _ := url.Parse(DefaultAuthURL)
	c := &Client{
		baseURL:    base,
		authURL:    auth,
		httpClient: &http.Client{},
		userAgent:  defaultUserAgent,
		sleep:      sleepCtx,
		now:        time.Now,
	}
	for _, o := range opts {
		if err := o(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// HasToken reports whether the client can make authenticated requests.
func (c *Client) HasToken() bool { return c.tokens != nil }

func parseRoot(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("soundcloud: invalid URL %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("soundcloud: URL %q must be absolute", raw)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u, nil
}

func (c *Client) endpoint(path string, q url.Values) *url.URL {
	u := c.baseURL.ResolveReference(&url.URL{Path: strings.TrimPrefix(path, "/")})
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u
}

// body is a request body. JSON bodies (data) can be re-sent; streamed bodies
// cannot.
type body struct {
	data        []byte
	stream      io.ReadCloser
	length      int64 // for streams; -1 if unknown
	contentType string
}

func jsonBody(v any) (*body, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &body{data: data, contentType: "application/json"}, nil
}

func (b *body) replayable() bool { return b == nil || b.stream == nil }

// do sends an authenticated request and decodes a JSON response into v.
func (c *Client) do(ctx context.Context, method string, u *url.URL, b *body, v any) error {
	if c.tokens == nil {
		return errNoToken
	}
	token, err := c.tokens.Token(ctx, false)
	if err != nil {
		return err
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "OAuth "+token)
		req.Header.Set("Accept", "application/json; charset=utf-8")
		req.Header.Set("User-Agent", c.userAgent)
		if b != nil {
			req.Header.Set("Content-Type", b.contentType)
			if b.stream != nil {
				req.Body = b.stream
				req.ContentLength = b.length
			} else {
				req.Body = io.NopCloser(bytes.NewReader(b.data))
				req.ContentLength = int64(len(b.data))
			}
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("soundcloud: reading response: %w", readErr)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if v == nil || len(bytes.TrimSpace(data)) == 0 {
				return nil
			}
			if err := json.Unmarshal(data, v); err != nil {
				return fmt.Errorf("soundcloud: decoding %s %s response: %w", method, u.Path, err)
			}
			return nil
		}

		apiErr := parseError(resp, data)
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized && !refreshed && b.replayable():
			refreshed = true
			fresh, err := c.tokens.Token(ctx, true)
			if err != nil || fresh == token {
				return apiErr
			}
			token = fresh
			continue
		case apiErr.IsRateLimit() && b.replayable() && attempt < maxRetries &&
			c.maxRateLimitWait > 0 && apiErr.RetryAfter <= c.maxRateLimitWait:
			wait := apiErr.RetryAfter
			if wait == 0 {
				wait = time.Duration(1<<attempt) * time.Second
			}
			if err := c.sleep(ctx, wait); err != nil {
				return err
			}
			continue
		}
		return apiErr
	}
}

// getURL follows a next_href link. It is only followed if it points at the
// API host, so a tampered response can't redirect the token elsewhere.
func (c *Client) getURL(ctx context.Context, raw string, v any) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("soundcloud: invalid next_href: %w", err)
	}
	if u.Scheme != c.baseURL.Scheme || u.Host != c.baseURL.Host {
		return fmt.Errorf("soundcloud: refusing to follow next_href on unexpected host %q", u.Host)
	}
	return c.do(ctx, http.MethodGet, u, nil, v)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var errNoToken = &Error{StatusCode: http.StatusUnauthorized, Message: "not logged in: an access token is required"}
