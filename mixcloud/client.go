// Package mixcloud is a client for the Mixcloud REST API
// (https://www.mixcloud.com/developers/), focused on managing a user's
// uploads: listing, uploading, editing, publishing and deleting shows.
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
	"time"
)

const (
	// DefaultBaseURL is the root of the Mixcloud REST API.
	DefaultBaseURL = "https://api.mixcloud.com/"
	// DefaultOAuthURL is the root of the Mixcloud OAuth endpoints.
	DefaultOAuthURL = "https://www.mixcloud.com/oauth/"

	defaultUserAgent = "mixcloudclient-go/2"
	maxRetries       = 3
)

// Client talks to the Mixcloud API. The zero value is not usable; create one
// with New.
type Client struct {
	baseURL          *url.URL
	oauthURL         *url.URL
	httpClient       *http.Client
	accessToken      string
	userAgent        string
	maxRateLimitWait time.Duration
	sleep            func(context.Context, time.Duration) error
}

// Option configures a Client.
type Option func(*Client) error

// WithAccessToken sets the OAuth access token sent with every request. It is
// required for /me/ and for every write (upload, edit, delete).
func WithAccessToken(token string) Option {
	return func(c *Client) error { c.accessToken = token; return nil }
}

// WithHTTPClient replaces the HTTP client. Note that uploads can take a long
// time; do not set a short overall Timeout on a client used for uploads.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) error {
		if h == nil {
			return errors.New("mixcloud: nil http client")
		}
		c.httpClient = h
		return nil
	}
}

// WithBaseURL overrides the API root (useful for tests).
func WithBaseURL(raw string) Option {
	return func(c *Client) error {
		u, err := parseRoot(raw)
		if err != nil {
			return err
		}
		c.baseURL = u
		return nil
	}
}

// WithOAuthBaseURL overrides the OAuth root (useful for tests).
func WithOAuthBaseURL(raw string) Option {
	return func(c *Client) error {
		u, err := parseRoot(raw)
		if err != nil {
			return err
		}
		c.oauthURL = u
		return nil
	}
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) error { c.userAgent = ua; return nil }
}

// WithRateLimitRetry makes the client wait and retry (up to 3 times) when the
// API reports a rate limit whose Retry-After is no longer than maxWait.
// Only requests without a streamed body (reads and deletes) are retried;
// uploads and edits always return the rate-limit error to the caller.
func WithRateLimitRetry(maxWait time.Duration) Option {
	return func(c *Client) error { c.maxRateLimitWait = maxWait; return nil }
}

// New returns a configured Client.
func New(opts ...Option) (*Client, error) {
	base, _ := url.Parse(DefaultBaseURL)
	oauth, _ := url.Parse(DefaultOAuthURL)
	c := &Client{
		baseURL:    base,
		oauthURL:   oauth,
		httpClient: &http.Client{},
		userAgent:  defaultUserAgent,
		sleep:      sleepCtx,
	}
	for _, o := range opts {
		if err := o(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// HasAccessToken reports whether the client was given an access token.
func (c *Client) HasAccessToken() bool { return c.accessToken != "" }

func parseRoot(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("mixcloud: invalid URL %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("mixcloud: URL %q must be absolute", raw)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u, nil
}

// endpoint resolves an API path (e.g. "me/cloudcasts/") against the base URL
// and adds the query and access token.
func (c *Client) endpoint(path string, q url.Values) *url.URL {
	u := c.baseURL.ResolveReference(&url.URL{Path: strings.TrimPrefix(path, "/")})
	c.addQuery(u, q)
	return u
}

func (c *Client) addQuery(u *url.URL, q url.Values) {
	merged := u.Query()
	for k, vs := range q {
		merged.Del(k)
		for _, v := range vs {
			merged.Add(k, v)
		}
	}
	if c.accessToken != "" {
		merged.Set("access_token", c.accessToken)
	}
	u.RawQuery = merged.Encode()
}

// get performs a GET against an API path and decodes the JSON response.
func (c *Client) get(ctx context.Context, path string, q url.Values, v any) error {
	return c.doJSON(ctx, http.MethodGet, c.endpoint(path, q), nil, "", v)
}

// getURL follows an absolute URL returned by the API (e.g. paging.next). The
// access token is only attached if the URL points at the API host, so a
// tampered response cannot leak the token elsewhere.
func (c *Client) getURL(ctx context.Context, raw string, v any) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("mixcloud: invalid paging URL: %w", err)
	}
	if u.Scheme != c.baseURL.Scheme || u.Host != c.baseURL.Host {
		return fmt.Errorf("mixcloud: refusing to follow paging URL on unexpected host %q", u.Host)
	}
	c.addQuery(u, nil)
	return c.doJSON(ctx, http.MethodGet, u, nil, "", v)
}

// doJSON sends a request and decodes a JSON response into v (if non-nil).
// body, when non-nil, is called to produce the request body; streaming
// bodies are never retried.
func (c *Client) doJSON(ctx context.Context, method string, u *url.URL, body *requestBody, contentType string, v any) error {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Body = body.reader
			req.ContentLength = body.length
			if body.length == 0 {
				req.ContentLength = -1
			}
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return redactErr(err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("mixcloud: reading response: %w", readErr)
		}

		if apiErr := parseError(resp, data); apiErr != nil {
			if body == nil && apiErr.IsRateLimit() && attempt < maxRetries &&
				c.maxRateLimitWait > 0 && apiErr.RetryAfter <= c.maxRateLimitWait {
				if err := c.sleep(ctx, apiErr.RetryAfter); err != nil {
					return err
				}
				continue
			}
			return apiErr
		}
		if v == nil || len(strings.TrimSpace(string(data))) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, v); err != nil {
			return fmt.Errorf("mixcloud: decoding %s response: %w", redactURL(u.String()), err)
		}
		return nil
	}
}

type requestBody struct {
	reader io.ReadCloser
	length int64 // 0 means unknown (chunked)
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

// sensitiveParams are query parameters that must never appear in errors.
var sensitiveParams = []string{"access_token", "client_secret", "code"}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable URL>"
	}
	q := u.Query()
	changed := false
	for _, p := range sensitiveParams {
		if q.Has(p) {
			q.Set(p, "REDACTED")
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = redactURL(ue.URL)
	}
	return err
}
