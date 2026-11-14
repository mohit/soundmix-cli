package mixcloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordedRequest captures what the stub server received.
type recordedRequest struct {
	Method        string
	Path          string
	Query         map[string][]string
	ContentLength int64
	BodyLength    int64
	Form          map[string][]string
	Files         map[string]recordedFile
}

type recordedFile struct {
	Filename    string
	ContentType string
	Data        []byte
}

type stub struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	routes   map[string]http.HandlerFunc
}

// newStub starts a fake Mixcloud API. routes maps "METHOD /path/" to a handler.
func newStub(t *testing.T, routes map[string]http.HandlerFunc) (*stub, *Client) {
	t.Helper()
	s := &stub{routes: routes}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	c, err := New(WithBaseURL(s.URL), WithOAuthBaseURL(s.URL+"/oauth/"), WithAccessToken("tok"))
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func (s *stub) serve(w http.ResponseWriter, r *http.Request) {
	rec := recordedRequest{
		Method:        r.Method,
		Path:          r.URL.Path,
		Query:         r.URL.Query(),
		ContentLength: r.ContentLength,
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err == nil {
			rec.Form = map[string][]string{}
			rec.Files = map[string]recordedFile{}
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				data, _ := io.ReadAll(p)
				rec.BodyLength += int64(len(data))
				if p.FileName() != "" {
					rec.Files[p.FormName()] = recordedFile{p.FileName(), p.Header.Get("Content-Type"), data}
				} else {
					rec.Form[p.FormName()] = append(rec.Form[p.FormName()], string(data))
				}
			}
		}
	}
	s.mu.Lock()
	s.requests = append(s.requests, rec)
	s.mu.Unlock()

	if h, ok := s.routes[r.Method+" "+r.URL.Path]; ok {
		h(w, r)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, `{"error": {"type": "ResourceNotFoundException", "message": "Upload does not exist."}}`)
}

func (s *stub) last() recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[len(s.requests)-1]
}

func jsonHandler(status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		switch b := body.(type) {
		case string:
			io.WriteString(w, b)
		default:
			json.NewEncoder(w).Encode(b)
		}
	}
}

var ctx = context.Background()
