package mixcloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Error types reported by the Mixcloud API.
const (
	ErrTypeOAuth     = "OAuthException"
	ErrTypeNotFound  = "ResourceNotFoundException"
	ErrTypeRateLimit = "RateLimitException"
)

// Error is an error response from the Mixcloud API.
type Error struct {
	StatusCode int
	Type       string
	Message    string
	// RetryAfter is set for rate-limit errors: how long to wait before retrying.
	RetryAfter time.Duration
	// Details holds per-field validation messages (e.g. for uploads).
	Details map[string][]string
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("mixcloud: ")
	if e.Type != "" {
		b.WriteString(e.Type)
		b.WriteString(": ")
	}
	if e.Message != "" {
		b.WriteString(e.Message)
	} else {
		b.WriteString(http.StatusText(e.StatusCode))
	}
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, " (HTTP %d)", e.StatusCode)
	}
	if len(e.Details) > 0 {
		keys := make([]string, 0, len(e.Details))
		for k := range e.Details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "; %s: %s", k, strings.Join(e.Details[k], " "))
		}
	}
	return b.String()
}

// IsRateLimit reports whether the error is a rate-limit error.
func (e *Error) IsRateLimit() bool { return e.Type == ErrTypeRateLimit }

// IsNotFound reports whether err is a Mixcloud "not found" error.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.StatusCode == http.StatusNotFound || e.Type == ErrTypeNotFound)
}

// IsRateLimited reports whether err is a Mixcloud rate-limit error. Use
// RetryAfter on the *Error to find out how long to wait.
func IsRateLimited(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.IsRateLimit()
}

// IsAuthError reports whether err means the access token is missing,
// invalid or revoked, and the user must authorize again.
func IsAuthError(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Type == ErrTypeOAuth || e.StatusCode == http.StatusUnauthorized)
}

type errorEnvelope struct {
	Error *struct {
		Type       string      `json:"type"`
		Message    string      `json:"message"`
		RetryAfter json.Number `json:"retry_after"`
	} `json:"error"`
	Details json.RawMessage `json:"details"`
	Result  *struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
	} `json:"result"`
}

// parseError returns an *Error if the response represents a failure: a
// non-2xx status, an "error" object, or a result with success=false.
func parseError(resp *http.Response, body []byte) *Error {
	var env errorEnvelope
	jsonErr := json.Unmarshal(body, &env)

	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if ok && (jsonErr != nil || (env.Error == nil && (env.Result == nil || env.Result.Success == nil || *env.Result.Success))) {
		return nil
	}

	e := &Error{StatusCode: resp.StatusCode}
	if env.Error != nil {
		e.Type = env.Error.Type
		e.Message = env.Error.Message
		if s, err := env.Error.RetryAfter.Float64(); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s * float64(time.Second))
		}
	} else if env.Result != nil && env.Result.Message != "" {
		e.Message = env.Result.Message
	} else if jsonErr != nil {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200] + "..."
		}
		e.Message = msg
	}
	if e.RetryAfter == 0 {
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
	}
	e.Details = parseDetails(env.Details)
	return e
}

// parseDetails accepts {"field": ["msg", ...]} or {"field": "msg"}.
func parseDetails(raw json.RawMessage) map[string][]string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var lists map[string][]string
	if json.Unmarshal(raw, &lists) == nil && len(lists) > 0 {
		return lists
	}
	var generic map[string]any
	if json.Unmarshal(raw, &generic) != nil || len(generic) == 0 {
		return nil
	}
	out := make(map[string][]string, len(generic))
	for k, v := range generic {
		out[k] = []string{fmt.Sprint(v)}
	}
	return out
}
