package mixcloud

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMeRequiresToken(t *testing.T) {
	c, _ := New()
	if _, err := c.Me(ctx); err == nil {
		t.Fatal("expected error without token")
	}
}

func TestMeSendsTokenAsQueryParam(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"GET /me/": jsonHandler(200, `{"username": "dj", "is_pro": false, "cloudcast_count": 3}`),
	})
	me, err := c.Me(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if me.Username != "dj" || me.CloudcastCount != 3 {
		t.Fatalf("unexpected user %+v", me)
	}
	if got := s.last().Query["access_token"]; len(got) != 1 || got[0] != "tok" {
		t.Fatalf("access_token query = %v", got)
	}
}

func TestQuota(t *testing.T) {
	for _, tc := range []struct {
		body      string
		remaining int
		str       string
	}{
		{`{"username":"a","is_pro":false,"cloudcast_count":0}`, 10, "0/10 free published shows used"},
		{`{"username":"a","is_pro":false,"cloudcast_count":10}`, 0, "10/10 free published shows used"},
		{`{"username":"a","is_pro":false,"cloudcast_count":12}`, 0, "12/10 free published shows used"},
		{`{"username":"a","is_pro":true,"cloudcast_count":50}`, -1, "50 published shows (Pro: unlimited uploads)"},
	} {
		_, c := newStub(t, map[string]http.HandlerFunc{"GET /me/": jsonHandler(200, tc.body)})
		q, err := c.Quota(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if q.Remaining() != tc.remaining || q.String() != tc.str {
			t.Errorf("%s: remaining=%d str=%q", tc.body, q.Remaining(), q.String())
		}
	}
}

func TestErrorParsing(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{
		"GET /me/": jsonHandler(401, `{"error": {"type": "OAuthException", "message": "An invalid access token was provided."}}`),
	})
	_, err := c.Me(ctx)
	if !IsAuthError(err) {
		t.Fatalf("expected auth error, got %v", err)
	}
	if _, err := c.Cloudcast(ctx, "dj/missing"); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestErrorsNeverLeakToken(t *testing.T) {
	c, _ := New(WithBaseURL("http://127.0.0.1:1/"), WithAccessToken("supersecret"))
	_, err := c.Me(ctx)
	if err == nil {
		t.Fatal("expected connection error")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("error leaks token: %v", err)
	}
}

func TestRateLimitRetry(t *testing.T) {
	calls := 0
	_, c := newStub(t, map[string]http.HandlerFunc{
		"GET /me/": func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(403)
				fmt.Fprint(w, `{"error": {"message": "You have hit your rate limit. Retry after 7 seconds.", "type": "RateLimitException", "retry_after": 7}}`)
				return
			}
			fmt.Fprint(w, `{"username": "dj"}`)
		},
	})
	var slept time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = d; return nil }
	c.maxRateLimitWait = time.Minute
	if _, err := c.Me(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || slept != 7*time.Second {
		t.Fatalf("calls=%d slept=%v", calls, slept)
	}
}

func TestRateLimitErrorWithoutRetry(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{
		"GET /me/": jsonHandler(403, `{"error": {"message": "Slow down", "type": "RateLimitException", "retry_after": 452}}`),
	})
	_, err := c.Me(ctx)
	var e *Error
	if !errors.As(err, &e) || !e.IsRateLimit() || e.RetryAfter != 452*time.Second {
		t.Fatalf("got %v", err)
	}
}

func TestUploadsIteratorFollowsPaging(t *testing.T) {
	var s *stub
	s, c := newStub(t, map[string]http.HandlerFunc{
		"GET /me/cloudcasts/": func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("offset") {
			case "0":
				fmt.Fprintf(w, `{"data": [{"key": "/dj/a/"}, {"key": "/dj/b/"}], "paging": {"next": "%s/me/cloudcasts/?limit=2&offset=2"}}`, s.URL)
			case "2":
				fmt.Fprintf(w, `{"data": [{"key": "/dj/c/"}], "paging": {"next": "%s/me/cloudcasts/?limit=2&offset=4"}}`, s.URL)
			default:
				fmt.Fprint(w, `{"data": [], "paging": {}}`)
			}
		},
	})
	var keys []string
	for cc, err := range c.Uploads(ctx, "", &ListOptions{Limit: 2}) {
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, cc.Key)
	}
	if strings.Join(keys, ",") != "/dj/a/,/dj/b/,/dj/c/" {
		t.Fatalf("keys = %v", keys)
	}
	// Following the next link must still send the token.
	if s.last().Query["access_token"][0] != "tok" {
		t.Fatal("token not sent on paging request")
	}
}

func TestPagingRefusesForeignHost(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{
		"GET /dj/cloudcasts/": jsonHandler(200, `{"data": [{"key": "/dj/a/"}], "paging": {"next": "https://evil.example/steal"}}`),
	})
	var gotErr error
	for _, err := range c.Uploads(ctx, "dj", nil) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "unexpected host") {
		t.Fatalf("expected refusal, got %v", gotErr)
	}
}

func TestUploadMultipart(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"POST /upload/": jsonHandler(200, `{"result": {"success": true, "message": "Uploaded API Upload", "key": "/dj/api-upload/"}}`),
	})
	audio := bytes.Repeat([]byte("a"), 100_000)
	at := func(s int) *time.Duration { d := time.Duration(s) * time.Second; return &d }
	var progressed int64
	req := &UploadRequest{
		Audio:       &File{Name: "upload.mp3", Body: bytes.NewReader(audio), Size: int64(len(audio))},
		Picture:     &File{Name: "cover.jpg", Body: strings.NewReader("jpg"), Size: 3},
		Name:        "API Upload",
		Description: "My test upload",
		Tags:        []string{"Test", "API"},
		Sections: []Section{
			{Chapter: "Introduction", StartTime: at(0)},
			{Artist: "Artist Name", Song: "Song Title", StartTime: at(10)},
		},
		Hosts:    []string{"friend"},
		Progress: func(sent, total int64) { progressed = sent },
	}
	res, err := c.Upload(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || res.Key != "/dj/api-upload/" {
		t.Fatalf("result %+v", res)
	}
	r := s.last()
	want := map[string]string{
		"name":                  "API Upload",
		"description":           "My test upload",
		"tags-0-tag":            "Test",
		"tags-1-tag":            "API",
		"sections-0-chapter":    "Introduction",
		"sections-0-start_time": "0",
		"sections-1-artist":     "Artist Name",
		"sections-1-song":       "Song Title",
		"sections-1-start_time": "10",
		"hosts-0-username":      "friend",
	}
	for k, v := range want {
		if got := r.Form[k]; len(got) != 1 || got[0] != v {
			t.Errorf("field %s = %v, want %q", k, got, v)
		}
	}
	for _, k := range []string{"unlisted", "publish_date", "disable_comments", "hide_stats"} {
		if _, ok := r.Form[k]; ok {
			t.Errorf("unexpected field %s", k)
		}
	}
	if f := r.Files["mp3"]; f.Filename != "upload.mp3" || f.ContentType != "audio/mpeg" || len(f.Data) != len(audio) {
		t.Errorf("mp3 part: %s %s %d bytes", f.Filename, f.ContentType, len(f.Data))
	}
	if f := r.Files["picture"]; string(f.Data) != "jpg" || f.ContentType != "image/jpeg" {
		t.Errorf("picture part: %+v", f)
	}
	if r.ContentLength <= int64(len(audio)) {
		t.Errorf("expected exact Content-Length, got %d", r.ContentLength)
	}
	if progressed != int64(len(audio)) {
		t.Errorf("progress reported %d of %d", progressed, len(audio))
	}
	if r.Query["access_token"][0] != "tok" {
		t.Error("token missing")
	}
}

func TestUploadUnknownSizeIsChunked(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"POST /upload/": jsonHandler(200, `{"result": {"success": true, "key": "/dj/x/"}}`),
	})
	_, err := c.Upload(ctx, &UploadRequest{
		Audio: &File{Name: "x.mp3", Body: strings.NewReader("abc"), Size: -1},
		Name:  "x", Unlisted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := s.last()
	if r.ContentLength != -1 || string(r.Files["mp3"].Data) != "abc" || r.Form["unlisted"][0] != "true" {
		t.Fatalf("got %+v", r)
	}
}

func TestUploadShortFileFails(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{"POST /upload/": jsonHandler(200, `{}`)})
	_, err := c.Upload(ctx, &UploadRequest{
		Audio: &File{Name: "x.mp3", Body: strings.NewReader("abc"), Size: 10},
		Name:  "x",
	})
	if err == nil {
		t.Fatal("expected error when body is shorter than Size")
	}
}

func TestUploadValidationError(t *testing.T) {
	_, c := newStub(t, nil)
	_, err := c.Upload(ctx, &UploadRequest{
		Audio:       &File{Name: "x.mp3", Body: strings.NewReader(""), Size: MaxAudioSize + 1},
		Description: strings.Repeat("x", 1001),
		Tags:        []string{"1", "2", "3", "4", "5", "6"},
		Hosts:       []string{"a", "b", "c"},
		Sections:    []Section{{Artist: "only artist"}},
		Unlisted:    true,
		PublishDate: time.Now().Add(-time.Hour),
	})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected validation error, got %v", err)
	}
	if len(ve.Problems) != 8 {
		t.Fatalf("expected 8 problems, got %d: %v", len(ve.Problems), ve.Problems)
	}
}

func TestUploadServerValidationDetails(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{
		"POST /upload/": jsonHandler(400, `{"error": {"type": "UploadException", "message": "Validation failed"}, "details": {"mp3": ["Shows must be longer than 15 minutes."]}}`),
	})
	_, err := c.Upload(ctx, &UploadRequest{
		Audio: &File{Name: "x.mp3", Body: strings.NewReader("abc"), Size: 3}, Name: "x",
	})
	var e *Error
	if !errors.As(err, &e) || e.Details["mp3"][0] != "Shows must be longer than 15 minutes." {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "15 minutes") {
		t.Fatalf("details missing from message: %v", err)
	}
}

func TestSuccessFalseIsError(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{
		"POST /upload/dj/show/edit/": jsonHandler(200, `{"result": {"success": false, "message": "Nope"}}`),
	})
	name := "n"
	_, err := c.Edit(ctx, "dj/show", &EditRequest{Name: &name})
	if err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("got %v", err)
	}
}

func TestEditFields(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"POST /upload/dj/show/edit/": jsonHandler(200, `{"result": {"success": true, "key": "/dj/show/"}}`),
	})
	name := "A new name"
	off := false
	_, err := c.Edit(ctx, "https://www.mixcloud.com/dj/show/", &EditRequest{
		Name:            &name,
		Tags:            []string{"Test", "API"},
		Hosts:           []string{},
		Visibility:      VisibilityPublish,
		DisableComments: &off,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := s.last()
	if r.Form["name"][0] != name || r.Form["tags-1-tag"][0] != "API" || r.Form["publish"][0] != "true" {
		t.Fatalf("form %v", r.Form)
	}
	if v, ok := r.Form["hosts-0-username"]; !ok || v[0] != "" {
		t.Fatalf("expected empty host to clear hosts, got %v", r.Form)
	}
	if r.Form["disable_comments"][0] != "false" {
		t.Fatalf("disable_comments = %v", r.Form["disable_comments"])
	}
	if _, ok := r.Form["description"]; ok {
		t.Fatal("description should be omitted when nil")
	}
}

func TestEditRejectsEmpty(t *testing.T) {
	_, c := newStub(t, nil)
	if _, err := c.Edit(ctx, "dj/show", &EditRequest{}); err == nil {
		t.Fatal("expected error for empty edit")
	}
	if _, err := c.Edit(ctx, "dj/show", &EditRequest{Tags: []string{}}); err == nil {
		t.Fatal("expected error for empty tag list")
	}
}

func TestVisibilityHelpers(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"POST /upload/dj/show/edit/": jsonHandler(200, `{"result": {"success": true}}`),
	})
	for name, fn := range map[string]func() (*UploadResult, error){
		"publish":   func() (*UploadResult, error) { return c.Publish(ctx, "dj/show") },
		"unpublish": func() (*UploadResult, error) { return c.Unpublish(ctx, "dj/show") },
		"unlisted":  func() (*UploadResult, error) { return c.Unlist(ctx, "dj/show") },
	} {
		if _, err := fn(); err != nil {
			t.Fatal(err)
		}
		if f := s.last().Form; len(f) != 1 || f[name][0] != "true" {
			t.Errorf("%s: form %v", name, f)
		}
	}
}

func TestDelete(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"DELETE /upload/dj/show/delete/": jsonHandler(200, `{"result": {"success": true, "message": "Deleted"}}`),
	})
	res, err := c.Delete(ctx, "/dj/show/")
	if err != nil || !res.Success {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if s.last().Method != http.MethodDelete {
		t.Fatal("expected DELETE")
	}
	if _, err := c.Delete(ctx, "dj/other"); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestDeleteEmptyBody(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{
		"DELETE /upload/dj/show/delete/": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) },
	})
	res, err := c.Delete(ctx, "dj/show")
	if err != nil || !res.Success {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestOAuth(t *testing.T) {
	for _, body := range []string{`access_token=abc123`, `{"access_token": "abc123"}`} {
		s, c := newStub(t, map[string]http.HandlerFunc{
			"GET /oauth/access_token": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) },
		})
		cfg := OAuthConfig{ClientID: "id", ClientSecret: "sec", RedirectURI: "https://app/cb"}
		tok, err := c.ExchangeCode(ctx, cfg, " code1 ")
		if err != nil || tok != "abc123" {
			t.Fatalf("%s: tok=%q err=%v", body, tok, err)
		}
		q := s.last().Query
		if q["code"][0] != "code1" || q["client_secret"][0] != "sec" || q["redirect_uri"][0] != "https://app/cb" {
			t.Fatalf("query %v", q)
		}
	}
	c, _ := New()
	got := c.AuthorizeURL(OAuthConfig{ClientID: "id", RedirectURI: "http://localhost:8765/callback"})
	if got != "https://www.mixcloud.com/oauth/authorize?client_id=id&redirect_uri=http%3A%2F%2Flocalhost%3A8765%2Fcallback" {
		t.Fatalf("authorize URL %s", got)
	}
}

func TestOAuthError(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{
		"GET /oauth/access_token": jsonHandler(400, `{"error": {"type": "OAuthException", "message": "Invalid code"}}`),
	})
	_, err := c.ExchangeCode(ctx, OAuthConfig{ClientID: "id", ClientSecret: "sec"}, "bad")
	if !IsAuthError(err) || strings.Contains(err.Error(), "sec") {
		t.Fatalf("got %v", err)
	}
}

func TestParseShowKey(t *testing.T) {
	for in, want := range map[string]string{
		"dj/show":                            "/dj/show/",
		"/dj/show/":                          "/dj/show/",
		"https://www.mixcloud.com/dj/show/":  "/dj/show/",
		"https://api.mixcloud.com/dj/show/":  "/dj/show/",
		"https://mixcloud.com/dj/show?utm=x": "/dj/show/",
	} {
		k, err := ParseShowKey(in)
		if err != nil || k.String() != want {
			t.Errorf("%s: got %v %v", in, k, err)
		}
	}
	for _, bad := range []string{"dj", "/dj/show/extra/", "https://example.com/dj/show/", ""} {
		if _, err := ParseShowKey(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestSearch(t *testing.T) {
	var s *stub
	s, c := newStub(t, map[string]http.HandlerFunc{
		"GET /search/": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("offset") == "" {
				fmt.Fprintf(w, `{"data": [{"key": "/a/x/", "name": "X"}], "paging": {"next": "%s/search/?q=x&type=cloudcast&offset=1"}}`, s.URL)
				return
			}
			fmt.Fprint(w, `{"data": [{"key": "/b/y/", "name": "Y"}], "paging": {}}`)
		},
	})
	var names []string
	for r, err := range c.Search(ctx, "party time", SearchCloudcasts, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, r.Name)
	}
	if strings.Join(names, "") != "XY" {
		t.Fatalf("names %v", names)
	}
}
