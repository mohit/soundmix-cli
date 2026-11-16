package soundcloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohit/soundmix-cli/media"
)

var ctx = context.Background()

type rec struct {
	Method, Path, Auth string
	Query              url.Values
	Form               map[string][]string
	Files              map[string][]byte
	JSON               map[string]any
	ContentLength      int64
}

type stub struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []rec
	routes map[string]http.HandlerFunc
}

func newStub(t *testing.T, routes map[string]http.HandlerFunc) (*stub, *Client) {
	t.Helper()
	s := &stub{routes: routes}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := rec{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Query: r.URL.Query(), ContentLength: r.ContentLength}
		ct := r.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ct, "multipart/form-data"):
			mr, _ := r.MultipartReader()
			rc.Form, rc.Files = map[string][]string{}, map[string][]byte{}
			for mr != nil {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				data, _ := io.ReadAll(p)
				if p.FileName() != "" {
					rc.Files[p.FormName()] = data
				} else {
					rc.Form[p.FormName()] = append(rc.Form[p.FormName()], string(data))
				}
			}
		case strings.HasPrefix(ct, "application/json"):
			json.NewDecoder(r.Body).Decode(&rc.JSON)
		case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
			r.ParseForm()
			rc.Form = r.PostForm
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, rc)
		s.mu.Unlock()
		if h, ok := s.routes[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"code":404,"message":"404 - Not Found","status":"404 - Not Found","errors":[{"error_message":"404 - Not Found"}],"error":null}`)
	}))
	t.Cleanup(s.Close)
	c, err := New(WithBaseURL(s.URL), WithAuthURL(s.URL+"/auth/"), WithAccessToken("tok"))
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func (s *stub) last() rec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[len(s.reqs)-1]
}

func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func TestMeAndQuota(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"GET /me": respond(200, `{"urn":"soundcloud:users:1","username":"dj","plan":"Free","private_tracks_count":2,
			"quota":{"unlimited_upload_quota":false,"upload_seconds_used":3600,"upload_seconds_left":7200}}`),
	})
	me, err := c.Me(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if me.Username != "dj" || me.Quota.Left() != 2*time.Hour || me.Quota.Used() != time.Hour || me.PrivateTracksCount != 2 {
		t.Fatalf("me %+v", me)
	}
	if got := s.last().Auth; got != "OAuth tok" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestCheckLength(t *testing.T) {
	q := &Quota{SecondsLeft: 3600}
	if err := CheckLength(59*time.Minute, q); err != nil {
		t.Fatal(err)
	}
	if err := CheckLength(61*time.Minute, q); err == nil || !strings.Contains(err.Error(), "1:00:00 of upload time is left") {
		t.Fatalf("got %v", err)
	}
	if err := CheckLength(25*time.Hour, &Quota{Unlimited: true}); err == nil {
		t.Fatal("expected 24h limit")
	}
	if err := CheckLength(10*time.Hour, &Quota{Unlimited: true}); err != nil {
		t.Fatal(err)
	}
}

func TestPKCEAndExchange(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(p.Verifier))
	if p.Challenge != base64.RawURLEncoding.EncodeToString(sum[:]) || len(p.Verifier) < 43 || p.State == "" {
		t.Fatalf("bad PKCE %+v", p)
	}
	s, c := newStub(t, map[string]http.HandlerFunc{
		"POST /auth/oauth/token": respond(200, `{"access_token":"A1","refresh_token":"R1","expires_in":3600,"scope":"*"}`),
	})
	cfg := OAuthConfig{ClientID: "id", ClientSecret: "sec", RedirectURI: "http://localhost:8765/callback"}
	au, _ := url.Parse(c.AuthorizeURL(cfg, p))
	q := au.Query()
	if au.Path != "/auth/authorize" || q.Get("code_challenge") != p.Challenge || q.Get("code_challenge_method") != "S256" ||
		q.Get("response_type") != "code" || q.Get("state") != p.State || q.Get("redirect_uri") != cfg.RedirectURI {
		t.Fatalf("authorize URL %s", au)
	}
	before := time.Now()
	tok, err := c.ExchangeCode(ctx, cfg, "CODE", p.Verifier)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "A1" || tok.RefreshToken != "R1" || tok.Expiry.Before(before.Add(59*time.Minute)) {
		t.Fatalf("token %+v", tok)
	}
	f := s.last().Form
	if f["grant_type"][0] != "authorization_code" || f["code_verifier"][0] != p.Verifier || f["code"][0] != "CODE" || f["client_secret"][0] != "sec" {
		t.Fatalf("form %v", f)
	}
	if s.last().Auth != "" {
		t.Fatal("token request must not send an OAuth header")
	}
}

func TestExchangeError(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{
		"POST /auth/oauth/token": respond(400, `{"error":"invalid_grant","error_description":"The provided authorization grant is invalid"}`),
	})
	_, err := c.ExchangeCode(ctx, OAuthConfig{ClientID: "id", ClientSecret: "sec", RedirectURI: "x"}, "bad", "v")
	if !IsAuthError(err) || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("got %v", err)
	}
}

func TestRefreshingTokenSourceRotates(t *testing.T) {
	n := 0
	var s *stub
	s, _ = newStub(t, map[string]http.HandlerFunc{
		"POST /auth/oauth/token": func(w http.ResponseWriter, r *http.Request) {
			n++
			fmt.Fprintf(w, `{"access_token":"A%d","refresh_token":"R%d","expires_in":3600}`, n+1, n+1)
		},
		"GET /me": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "OAuth A2" {
				respond(401, `{"code":401,"message":"401 - Unauthorized"}`)(w, r)
				return
			}
			fmt.Fprint(w, `{"username":"dj"}`)
		},
	})
	c, _ := New(WithBaseURL(s.URL), WithAuthURL(s.URL+"/auth/"))
	var saved []Token
	cfg := OAuthConfig{ClientID: "id", ClientSecret: "sec"}
	// Expired token: refreshed before the request is sent.
	ts := NewRefreshingTokenSource(c, cfg, Token{AccessToken: "A1", RefreshToken: "R1", Expiry: time.Now().Add(-time.Minute)},
		func(t Token) error { saved = append(saved, t); return nil })
	c.tokens = ts
	if _, err := c.Me(ctx); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].RefreshToken != "R2" {
		t.Fatalf("saved %+v", saved)
	}
	var refreshForm map[string][]string
	for _, r := range s.reqs {
		if r.Path == "/auth/oauth/token" {
			refreshForm = r.Form
		}
	}
	if refreshForm["grant_type"][0] != "refresh_token" || refreshForm["refresh_token"][0] != "R1" {
		t.Fatalf("refresh form %v", refreshForm)
	}
}

func TestUnauthorizedTriggersOneRefresh(t *testing.T) {
	refreshes := 0
	var s *stub
	s, _ = newStub(t, map[string]http.HandlerFunc{
		"POST /auth/oauth/token": func(w http.ResponseWriter, r *http.Request) {
			refreshes++
			fmt.Fprint(w, `{"access_token":"NEW","refresh_token":"R2","expires_in":3600}`)
		},
		"GET /me": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "OAuth NEW" {
				fmt.Fprint(w, `{"username":"dj"}`)
				return
			}
			respond(401, `{"code":401,"message":"401 - Unauthorized"}`)(w, r)
		},
	})
	c, _ := New(WithBaseURL(s.URL), WithAuthURL(s.URL+"/auth/"))
	c.tokens = NewRefreshingTokenSource(c, OAuthConfig{ClientID: "id", ClientSecret: "sec"},
		Token{AccessToken: "OLD", RefreshToken: "R1", Expiry: time.Now().Add(time.Hour)}, nil)
	if _, err := c.Me(ctx); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 {
		t.Fatalf("refreshes = %d", refreshes)
	}
}

func TestStaticTokenUnauthorized(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{"GET /me": respond(401, `{"code":401,"message":"401 - Unauthorized"}`)})
	if _, err := c.Me(ctx); !IsAuthError(err) {
		t.Fatalf("got %v", err)
	}
	noTok, _ := New()
	if _, err := noTok.Me(ctx); !IsAuthError(err) {
		t.Fatalf("got %v", err)
	}
}

func TestUploadMultipart(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"POST /tracks": respond(201, `{"urn":"soundcloud:tracks:42","title":"Friday Mix","sharing":"private","permalink_url":"https://soundcloud.com/dj/friday-mix"}`),
	})
	audio := bytes.Repeat([]byte("x"), 50_000)
	no := false
	var sent int64
	tr, err := c.Upload(ctx, &UploadRequest{
		Audio:       &media.File{Name: "mix.flac", Body: bytes.NewReader(audio), Size: int64(len(audio))},
		Artwork:     &media.File{Name: "cover.png", Body: strings.NewReader("png"), Size: 3},
		Title:       "Friday Mix",
		Artist:      "DJ Someone",
		Description: "Live",
		Genre:       "House",
		Tags:        []string{"deep house", "live"},
		Sharing:     Private,
		Commentable: &no,
		License:     "cc-by",
		ReleaseDate: "2026-11-01",
		Progress:    func(n, total int64) { sent = n },
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.URN != "soundcloud:tracks:42" {
		t.Fatalf("track %+v", tr)
	}
	r := s.last()
	for k, v := range map[string]string{
		"track[title]": "Friday Mix", "track[artist]": "DJ Someone", "track[description]": "Live",
		"track[genre]": "House", "track[tag_list]": `"deep house" live`, "track[sharing]": "private",
		"track[commentable]": "false", "track[license]": "cc-by", "track[release_date]": "2026-11-01",
	} {
		if got := r.Form[k]; len(got) != 1 || got[0] != v {
			t.Errorf("%s = %v, want %q", k, got, v)
		}
	}
	if _, ok := r.Form["track[downloadable]"]; ok {
		t.Error("unset bools must be omitted")
	}
	if len(r.Files["track[asset_data]"]) != len(audio) || string(r.Files["track[artwork_data]"]) != "png" {
		t.Error("files not sent")
	}
	if r.ContentLength <= int64(len(audio)) || sent != int64(len(audio)) || r.Auth != "OAuth tok" {
		t.Errorf("content-length %d, progress %d, auth %q", r.ContentLength, sent, r.Auth)
	}
}

func TestUploadValidation(t *testing.T) {
	_, c := newStub(t, nil)
	_, err := c.Upload(ctx, &UploadRequest{
		Audio:        &media.File{Name: "x.wav", Body: strings.NewReader(""), Size: MaxAudioSize + 1},
		Artwork:      &media.File{Name: "cover.bmp", Body: strings.NewReader("x"), Size: 1},
		Sharing:      "unlisted",
		License:      "gpl",
		EmbeddableBy: "everyone",
		ReleaseDate:  "01/02/2026",
		Permalink:    "Bad Link",
		Tags:         []string{"ok", " ", `has"quote`},
	})
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Problems) != 10 {
		t.Fatalf("got %v", err)
	}
}

func TestUpdateTrackJSON(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"PUT /tracks/soundcloud:tracks:42": respond(200, `{"urn":"soundcloud:tracks:42","title":"New"}`),
	})
	title, artist, sharing := "New", "Other Artist", Public
	yes := true
	_, err := c.UpdateTrack(ctx, "soundcloud:tracks:42", &UpdateRequest{
		Title: &title, Artist: &artist, Sharing: &sharing, Downloadable: &yes, Tags: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := s.last().JSON["track"].(map[string]any)
	if tr["title"] != "New" || tr["metadata_artist"] != "Other Artist" || tr["sharing"] != "public" ||
		tr["downloadable"] != true || tr["tag_list"] != "" || len(tr) != 5 {
		t.Fatalf("json %v", tr)
	}
	if _, err := c.UpdateTrack(ctx, "soundcloud:tracks:42", &UpdateRequest{}); err == nil {
		t.Fatal("expected error for empty update")
	}
}

func TestDeleteAndNotFound(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"DELETE /tracks/soundcloud:tracks:42": respond(200, ``),
	})
	if err := c.DeleteTrack(ctx, "soundcloud:tracks:42"); err != nil {
		t.Fatal(err)
	}
	if s.last().Method != http.MethodDelete {
		t.Fatal("expected DELETE")
	}
	err := c.DeleteTrack(ctx, "soundcloud:tracks:43")
	if !IsNotFound(err) || !strings.Contains(err.Error(), "404 - Not Found") {
		t.Fatalf("got %v", err)
	}
}

func TestMyTracksPaging(t *testing.T) {
	var s *stub
	s, c := newStub(t, map[string]http.HandlerFunc{
		"GET /me/tracks": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("cursor") == "" {
				if r.URL.Query().Get("linked_partitioning") != "true" {
					t.Error("linked_partitioning not set")
				}
				fmt.Fprintf(w, `{"collection":[{"urn":"soundcloud:tracks:1"},{"urn":"soundcloud:tracks:2"}],"next_href":"%s/me/tracks?cursor=2&linked_partitioning=true"}`, s.URL)
				return
			}
			fmt.Fprint(w, `{"collection":[{"urn":"soundcloud:tracks:3","sharing":"private"}]}`)
		},
	})
	var urns []string
	for tr, err := range c.MyTracks(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		urns = append(urns, tr.URN)
	}
	if len(urns) != 3 || s.last().Auth != "OAuth tok" {
		t.Fatalf("urns %v", urns)
	}
}

func TestPagingRefusesForeignHost(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{
		"GET /me/tracks": respond(200, `{"collection":[{"urn":"soundcloud:tracks:1"}],"next_href":"https://evil.example/steal"}`),
	})
	var last error
	for _, err := range c.MyTracks(ctx) {
		last = err
	}
	if last == nil || !strings.Contains(last.Error(), "unexpected host") {
		t.Fatalf("got %v", last)
	}
}

func TestRateLimitRetry(t *testing.T) {
	calls := 0
	_, c := newStub(t, map[string]http.HandlerFunc{
		"GET /me": func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("Retry-After", "3")
				respond(429, `{"code":429,"message":"429 - Too Many Requests"}`)(w, r)
				return
			}
			fmt.Fprint(w, `{"username":"dj"}`)
		},
	})
	var slept time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = d; return nil }
	c.maxRateLimitWait = time.Minute
	if _, err := c.Me(ctx); err != nil || slept != 3*time.Second || calls != 2 {
		t.Fatalf("err %v slept %v calls %d", err, slept, calls)
	}
	c.maxRateLimitWait = 0
	calls = 0
	_, err := c.Me(ctx)
	var e *Error
	if !errors.As(err, &e) || !e.IsRateLimit() || e.RetryAfter != 3*time.Second {
		t.Fatalf("got %v", err)
	}
}

func TestResolveAndURNs(t *testing.T) {
	_, c := newStub(t, map[string]http.HandlerFunc{
		"GET /resolve": func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Query().Get("url"), "/sets/") {
				fmt.Fprint(w, `{"urn":"soundcloud:playlists:9","kind":"playlist"}`)
				return
			}
			fmt.Fprint(w, `{"urn":"soundcloud:tracks:7","kind":"track"}`)
		},
	})
	for in, want := range map[string]string{
		"123":                                "soundcloud:tracks:123",
		"soundcloud:tracks:5":                "soundcloud:tracks:5",
		"https://soundcloud.com/dj/some-mix": "soundcloud:tracks:7",
	} {
		got, err := c.TrackURN(ctx, in)
		if err != nil || got != want {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
	if _, err := c.TrackURN(ctx, "https://soundcloud.com/dj/sets/series"); err == nil {
		t.Error("a playlist URL is not a track")
	}
	if got, err := c.PlaylistURN(ctx, "https://soundcloud.com/dj/sets/series"); err != nil || got != "soundcloud:playlists:9" {
		t.Errorf("playlist: %s %v", got, err)
	}
	if _, err := c.TrackURN(ctx, "not a ref"); err == nil {
		t.Error("expected error")
	}
}

func TestPlaylists(t *testing.T) {
	s, c := newStub(t, map[string]http.HandlerFunc{
		"POST /playlists": respond(201, `{"urn":"soundcloud:playlists:9","title":"Series"}`),
		"GET /playlists/soundcloud:playlists:9/tracks": respond(200,
			`{"collection":[{"urn":"soundcloud:tracks:1"},{"urn":"soundcloud:tracks:2"}]}`),
		"PUT /playlists/soundcloud:playlists:9": respond(200, `{"urn":"soundcloud:playlists:9","track_count":3}`),
		"GET /me/playlists":                     respond(200, `{"collection":[{"urn":"soundcloud:playlists:8","title":"Other"},{"urn":"soundcloud:playlists:9","title":"My Series"}]}`),
	})
	if _, err := c.CreatePlaylist(ctx, PlaylistRequest{Title: "Series", Sharing: Private, TrackURNs: []string{"soundcloud:tracks:1"}}); err != nil {
		t.Fatal(err)
	}
	pl := s.last().JSON["playlist"].(map[string]any)
	if pl["title"] != "Series" || pl["sharing"] != "private" || pl["tracks"].([]any)[0].(map[string]any)["urn"] != "soundcloud:tracks:1" {
		t.Fatalf("create json %v", pl)
	}
	if _, err := c.AddToPlaylist(ctx, "soundcloud:playlists:9", "soundcloud:tracks:2", "soundcloud:tracks:3"); err != nil {
		t.Fatal(err)
	}
	tracks := s.last().JSON["playlist"].(map[string]any)["tracks"].([]any)
	if len(tracks) != 3 || tracks[2].(map[string]any)["urn"] != "soundcloud:tracks:3" {
		t.Fatalf("add json %v", tracks)
	}
	if _, err := c.RemoveFromPlaylist(ctx, "soundcloud:playlists:9", "soundcloud:tracks:1"); err != nil {
		t.Fatal(err)
	}
	tracks = s.last().JSON["playlist"].(map[string]any)["tracks"].([]any)
	if len(tracks) != 1 || tracks[0].(map[string]any)["urn"] != "soundcloud:tracks:2" {
		t.Fatalf("remove json %v", tracks)
	}
	found, err := c.FindMyPlaylist(ctx, "my series")
	if err != nil || found == nil || found.URN != "soundcloud:playlists:9" {
		t.Fatalf("find %v %v", found, err)
	}
	if missing, _ := c.FindMyPlaylist(ctx, "nope"); missing != nil {
		t.Fatal("expected nil")
	}
}

func TestTags(t *testing.T) {
	enc := EncodeTags([]string{"deep house", "live", " ", "uk garage"})
	if enc != `"deep house" live "uk garage"` {
		t.Fatalf("encoded %q", enc)
	}
	dec := DecodeTags(enc + ` geo:lat=1`)
	if strings.Join(dec, "|") != "deep house|live|uk garage|geo:lat=1" {
		t.Fatalf("decoded %q", dec)
	}
}

func TestDescriptionWithTracklist(t *testing.T) {
	at := func(s int) *time.Duration { d := time.Duration(s) * time.Second; return &d }
	secs := []media.Section{{Chapter: "Intro", StartTime: at(0)}, {Artist: "A", Song: "B", StartTime: at(150)}}
	d := DescriptionWithTracklist("Recorded live.", secs)
	want := "Recorded live.\n\nTracklist:\n0:00 Intro\n2:30 A - B"
	if d != want {
		t.Fatalf("got %q", d)
	}
	// Re-applying replaces the old tracklist rather than appending another.
	d2 := DescriptionWithTracklist(d, secs[:1])
	if d2 != "Recorded live.\n\nTracklist:\n0:00 Intro" {
		t.Fatalf("got %q", d2)
	}
	if StripTracklist(d) != "Recorded live." || DescriptionWithTracklist("", secs[:1]) != "Tracklist:\n0:00 Intro" {
		t.Fatal("strip failed")
	}
}
