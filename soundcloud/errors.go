package soundcloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Error is an error response from the SoundCloud API or token endpoint.
type Error struct {
	StatusCode int
	Message    string
	// Errors holds individual error messages, when the API lists them.
	Errors []string
	// OAuthError is the OAuth error code from the token endpoint, e.g.
	// "invalid_grant".
	OAuthError string
	// RetryAfter is set for 429 responses that say how long to wait.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("soundcloud: ")
	msg := e.Message
	if msg == "" && len(e.Errors) > 0 {
		msg = strings.Join(e.Errors, "; ")
	} else if len(e.Errors) > 0 && !strings.Contains(msg, e.Errors[0]) {
		msg += ": " + strings.Join(e.Errors, "; ")
	}
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	if e.OAuthError != "" && !strings.Contains(msg, e.OAuthError) {
		msg = e.OAuthError + ": " + msg
	}
	b.WriteString(msg)
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, " (HTTP %d)", e.StatusCode)
	}
	return b.String()
}

// IsRateLimit reports whether the error is a 429.
func (e *Error) IsRateLimit() bool { return e.StatusCode == http.StatusTooManyRequests }

// IsNotFound reports whether err is a SoundCloud 404.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == http.StatusNotFound
}

// IsRateLimited reports whether err is a SoundCloud 429.
func IsRateLimited(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.IsRateLimit()
}

// IsAuthError reports whether err means the user must (re)authorize: a
// missing/expired token or a rejected refresh token.
func IsAuthError(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.StatusCode == http.StatusUnauthorized || e.OAuthError == "invalid_grant"
}

func parseError(resp *http.Response, data []byte) *Error {
	e := &Error{StatusCode: resp.StatusCode}
	var env struct {
		Message          string          `json:"message"`
		Status           string          `json:"status"`
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
		Errors           []struct {
			ErrorMessage string `json:"error_message"`
		} `json:"errors"`
	}
	if json.Unmarshal(data, &env) == nil {
		e.Message = env.Message
		for _, x := range env.Errors {
			if x.ErrorMessage != "" && x.ErrorMessage != e.Message {
				e.Errors = append(e.Errors, x.ErrorMessage)
			}
		}
		var code string
		if json.Unmarshal(env.Error, &code) == nil && code != "" {
			e.OAuthError = code
		}
		if env.ErrorDescription != "" && e.Message == "" {
			e.Message = env.ErrorDescription
		}
		if e.Message == "" && env.Status != "" {
			e.Message = env.Status
		}
	} else if msg := strings.TrimSpace(string(data)); msg != "" {
		if len(msg) > 200 {
			msg = msg[:200] + "..."
		}
		e.Message = msg
	}
	if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	} else if e.IsRateLimit() {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
			if d := time.Until(time.Unix(reset, 0)); d > 0 {
				e.RetryAfter = d.Round(time.Second)
			}
		}
	}
	return e
}

// ValidationError lists every problem found with a request before it was
// sent.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "soundcloud: invalid request: " + strings.Join(e.Problems, "; ")
}

type problems []string

func (p *problems) addf(format string, a ...any) { *p = append(*p, fmt.Sprintf(format, a...)) }

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return &ValidationError{Problems: p}
}
