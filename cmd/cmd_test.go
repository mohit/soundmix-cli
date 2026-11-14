package cmd

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMP3 writes a tiny MP3 whose Xing header claims the given duration, so
// length checks can be tested without large files.
func fakeMP3(t *testing.T, dir string, d time.Duration) string {
	t.Helper()
	frameLen := 144 * 128000 / 44100
	frame := make([]byte, frameLen)
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00})
	copy(frame[36:], "Xing")
	binary.BigEndian.PutUint32(frame[40:], 1)
	binary.BigEndian.PutUint32(frame[44:], uint32(d.Seconds()*44100/1152)+1)
	plain := make([]byte, frameLen)
	copy(plain, []byte{0xFF, 0xFB, 0x90, 0x00})
	data := append(frame, bytes.Repeat(plain, 5)...)
	path := filepath.Join(dir, fmt.Sprintf("show-%d.mp3", int(d.Minutes())))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type fakeAPI struct {
	*httptest.Server
	mu        sync.Mutex
	published int
	isPro     bool
	uploads   []map[string][]string
	edits     []map[string][]string
	deletes   []string
	// audioLength of /dj/friday-mix/; hidden makes anonymous requests 404.
	audioLength int
	hidden      bool
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{audioLength: 1800}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /me/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "test-token" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error": {"type": "OAuthException", "message": "An invalid access token was provided."}}`)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		fmt.Fprintf(w, `{"username": "dj", "name": "DJ", "is_pro": %v, "cloudcast_count": %d}`, f.isPro, f.published)
	})
	mux.HandleFunc("POST /upload/{$}", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		f.uploads = append(f.uploads, r.MultipartForm.Value)
		f.mu.Unlock()
		fmt.Fprint(w, `{"result": {"success": true, "message": "Uploaded", "key": "/dj/friday-mix/"}}`)
	})
	mux.HandleFunc("GET /dj/friday-mix/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.hidden && r.URL.Query().Get("access_token") == "" {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error": {"type": "ResourceNotFoundException", "message": "Upload does not exist."}}`)
			return
		}
		fmt.Fprintf(w, `{"key": "/dj/friday-mix/", "name": "Friday Mix", "audio_length": %d, "tags": [{"name": "House"}, {"name": "Disco"}]}`, f.audioLength)
	})
	mux.HandleFunc("GET /me/cloudcasts/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data": [], "paging": {}}`)
	})
	mux.HandleFunc("POST /upload/dj/friday-mix/edit/", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		f.edits = append(f.edits, r.MultipartForm.Value)
		f.mu.Unlock()
		fmt.Fprint(w, `{"result": {"success": true, "message": "Edited", "key": "/dj/friday-mix/"}}`)
	})
	mux.HandleFunc("DELETE /upload/dj/friday-mix/delete/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deletes = append(f.deletes, r.URL.Path)
		f.mu.Unlock()
		fmt.Fprint(w, `{"result": {"success": true, "message": "Deleted"}}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error": {"type": "ResourceNotFoundException", "message": "Upload does not exist."}}`)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// runCLI runs the CLI in dir with a .env pointing at api.
func runCLI(t *testing.T, dir string, api *fakeAPI, stdin string, args ...string) (int, string, string) {
	t.Helper()
	for _, k := range []string{envToken, envAPIURL, envClientID, envSecret, envRedirectURI, envOAuthURL, envEnvFile} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	env := filepath.Join(dir, ".env")
	if _, err := os.Stat(env); os.IsNotExist(err) {
		content := fmt.Sprintf("%s=test-token\n%s=%s\n", envToken, envAPIURL, api.URL)
		if err := os.WriteFile(env, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	args = append([]string{"--env-file", env}, args...)
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCreateRejectsShortShow(t *testing.T) {
	api := newFakeAPI(t)
	dir := t.TempDir()
	mp3 := fakeMP3(t, dir, 10*time.Minute)
	code, _, stderr := runCLI(t, dir, api, "", "--json", "uploads", "create", mp3, "--name", "Short")
	if code != exitInvalid {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if !strings.Contains(stderr, "at least 15:00") {
		t.Fatalf("stderr %s", stderr)
	}
	var e map[string]any
	if err := json.Unmarshal([]byte(stderr), &e); err != nil {
		t.Fatalf("stderr is not JSON: %v\n%s", err, stderr)
	}
	if len(api.uploads) != 0 {
		t.Fatal("upload should not have been sent")
	}
}

func TestCreateRejectsWhenQuotaFull(t *testing.T) {
	api := newFakeAPI(t)
	api.published = 10
	dir := t.TempDir()
	mp3 := fakeMP3(t, dir, 20*time.Minute)
	code, _, stderr := runCLI(t, dir, api, "", "uploads", "create", mp3, "--name", "Full")
	if code != exitInvalid || !strings.Contains(stderr, "10/10 free published shows used") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	// Unlisted uploads skip the published-show check.
	code, _, stderr = runCLI(t, dir, api, "", "uploads", "create", mp3, "--name", "Full", "--unlisted", "-q")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if api.uploads[0]["unlisted"][0] != "true" {
		t.Fatalf("form %v", api.uploads[0])
	}
}

func TestCreateDryRun(t *testing.T) {
	api := newFakeAPI(t)
	dir := t.TempDir()
	mp3 := fakeMP3(t, dir, 60*time.Minute)
	code, stdout, stderr := runCLI(t, dir, api, "", "--json", "uploads", "create", mp3, "--name", "Dry", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	var plan map[string]any
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatal(err)
	}
	if plan["ok"] != true || plan["duration_seconds"].(float64) < 3599 {
		t.Fatalf("plan %v", plan)
	}
	if len(api.uploads) != 0 {
		t.Fatal("dry run uploaded")
	}
}

func TestCreateUploads(t *testing.T) {
	api := newFakeAPI(t)
	dir := t.TempDir()
	mp3 := fakeMP3(t, dir, 30*time.Minute)
	tl := filepath.Join(dir, "tracklist.txt")
	os.WriteFile(tl, []byte("0:00 | Intro\n5:00 Artist - Song\n"), 0o644)
	code, stdout, stderr := runCLI(t, dir, api, "", "--json", "uploads", "create", mp3,
		"--name", "Friday Mix", "-t", "House", "-t", "Disco", "--tracklist", tl,
		"--section", "10:00 | Other | Tune", "--description", "Live")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	var res map[string]any
	json.Unmarshal([]byte(stdout), &res)
	if res["url"] != "https://www.mixcloud.com/dj/friday-mix/" {
		t.Fatalf("result %v", res)
	}
	form := api.uploads[0]
	for k, v := range map[string]string{
		"name": "Friday Mix", "tags-1-tag": "Disco", "sections-0-chapter": "Intro",
		"sections-1-artist": "Artist", "sections-1-start_time": "300", "sections-2-song": "Tune",
		"description": "Live",
	} {
		if form[k] == nil || form[k][0] != v {
			t.Errorf("%s = %v, want %s", k, form[k], v)
		}
	}
	// Non-terminal progress goes to stderr as lines, never to stdout.
	if !strings.Contains(stderr, "100%") {
		t.Errorf("expected progress on stderr, got %q", stderr)
	}
}

func TestEditAddRemoveTags(t *testing.T) {
	api := newFakeAPI(t)
	dir := t.TempDir()
	code, _, stderr := runCLI(t, dir, api, "", "uploads", "edit", "dj/friday-mix", "--add-tag", "Techno", "--remove-tag", "disco")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	form := api.edits[0]
	if form["tags-0-tag"][0] != "House" || form["tags-1-tag"][0] != "Techno" || form["tags-2-tag"] != nil {
		t.Fatalf("form %v", form)
	}
	if _, ok := form["name"]; ok {
		t.Fatal("name should not be sent")
	}
}

func TestEditPublishChecksQuota(t *testing.T) {
	api := newFakeAPI(t)
	api.published = 10
	dir := t.TempDir()
	code, _, _ := runCLI(t, dir, api, "", "uploads", "publish", "dj/friday-mix")
	if code != exitInvalid {
		t.Fatalf("exit %d", code)
	}
	api.isPro = true
	code, _, stderr := runCLI(t, dir, api, "", "uploads", "publish", "dj/friday-mix")
	if code != 0 || api.edits[0]["publish"][0] != "true" {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
}

func init() { publishVerifyDelays = []time.Duration{time.Millisecond} }

func TestPublishRejectsShortShow(t *testing.T) {
	api := newFakeAPI(t)
	api.audioLength = 60
	dir := t.TempDir()
	code, _, stderr := runCLI(t, dir, api, "", "uploads", "publish", "dj/friday-mix")
	if code != exitInvalid || len(api.edits) != 0 || !strings.Contains(stderr, "15:00") {
		t.Fatalf("exit %d, edits %d, stderr %s", code, len(api.edits), stderr)
	}
}

func TestPublishThatDoesNotTakeEffectFails(t *testing.T) {
	api := newFakeAPI(t)
	api.hidden = true
	dir := t.TempDir()
	code, _, stderr := runCLI(t, dir, api, "", "uploads", "publish", "dj/friday-mix")
	if code != exitError || !strings.Contains(stderr, "still not public") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
}

func TestLedgerTracksUploadsAndQuota(t *testing.T) {
	api := newFakeAPI(t)
	api.published = 9
	dir := t.TempDir()
	mp3 := fakeMP3(t, dir, 30*time.Minute)
	if code, _, stderr := runCLI(t, dir, api, "", "uploads", "create", mp3, "--name", "Friday Mix", "-q"); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	l, err := (&globalOptions{envFile: filepath.Join(dir, ".env")}).loadLedger()
	if err != nil || len(l.Entries) != 1 || l.Entries[0].Key != "/dj/friday-mix/" || l.Entries[0].Visibility != stPublic {
		t.Fatalf("ledger %+v %v", l, err)
	}
	// cloudcast_count still says 9 (it lags), but the ledger knows about the
	// new public show, so the quota is full.
	code, stdout, _ := runCLI(t, dir, api, "", "--json", "uploads", "quota")
	var q map[string]any
	json.Unmarshal([]byte(stdout), &q)
	if code != 0 || q["published"].(float64) != 10 || q["remaining"].(float64) != 0 {
		t.Fatalf("quota %v", q)
	}
	// The show isn't in the API listing yet, but --include-private finds it.
	code, stdout, _ = runCLI(t, dir, api, "", "--json", "uploads", "list", "--include-private")
	var shows []map[string]any
	json.Unmarshal([]byte(stdout), &shows)
	if code != 0 || len(shows) != 1 || shows[0]["status"] != stPublic || shows[0]["in_profile_listing"] != nil {
		t.Fatalf("list %s", stdout)
	}
	// Unpublishing records a draft; deleting forgets it.
	api.hidden = true
	runCLI(t, dir, api, "", "uploads", "unpublish", "dj/friday-mix")
	code, stdout, _ = runCLI(t, dir, api, "", "--json", "uploads", "get", "dj/friday-mix")
	if code != 0 || !strings.Contains(stdout, `"status": "draft"`) {
		t.Fatalf("get %s", stdout)
	}
	runCLI(t, dir, api, "", "uploads", "delete", "dj/friday-mix", "--yes")
	l, _ = (&globalOptions{envFile: filepath.Join(dir, ".env")}).loadLedger()
	if len(l.Entries) != 0 {
		t.Fatalf("ledger not cleaned: %+v", l.Entries)
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	api := newFakeAPI(t)
	dir := t.TempDir()
	code, _, _ := runCLI(t, dir, api, "", "uploads", "delete", "dj/friday-mix")
	if code != exitInvalid || len(api.deletes) != 0 {
		t.Fatalf("exit %d, deletes %v", code, api.deletes)
	}
	code, _, stderr := runCLI(t, dir, api, "", "uploads", "delete", "dj/friday-mix", "--yes")
	if code != 0 || len(api.deletes) != 1 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
}

func TestNotFoundExitCode(t *testing.T) {
	api := newFakeAPI(t)
	dir := t.TempDir()
	code, _, _ := runCLI(t, dir, api, "", "uploads", "delete", "dj/nope", "--yes")
	if code != exitNotFound {
		t.Fatalf("exit %d", code)
	}
}

func TestAuthErrorExitCode(t *testing.T) {
	api := newFakeAPI(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte(envAPIURL+"="+api.URL+"\n"+envToken+"=bad\n"), 0o600)
	code, _, stderr := runCLI(t, dir, api, "", "uploads", "quota")
	if code != exitAuth {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	os.WriteFile(filepath.Join(dir, ".env"), []byte(envAPIURL+"="+api.URL+"\n"), 0o600)
	code, _, stderr = runCLI(t, dir, api, "", "uploads", "list")
	if code != exitAuth || !strings.Contains(stderr, "auth login") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
}

func TestLoginWithCodeSavesToken(t *testing.T) {
	api := newFakeAPI(t)
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/access_token" || r.URL.Query().Get("code") != "the-code" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error": {"type": "OAuthException", "message": "bad code"}}`)
			return
		}
		io.WriteString(w, `{"access_token": "test-token"}`)
	}))
	defer oauth.Close()
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	os.WriteFile(env, []byte("# creds\n"+envClientID+"=id\n"+envSecret+"=secret\n"+envAPIURL+"="+api.URL+"\n"+envOAuthURL+"="+oauth.URL+"/oauth/\n"), 0o600)
	code, stdout, stderr := runCLI(t, dir, api, "", "auth", "login", "--code", "https://example.com/cb?code=the-code")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if !strings.Contains(stdout, "Logged in as dj") {
		t.Fatalf("stdout %s", stdout)
	}
	vals, err := readDotEnv(env)
	if err != nil || vals[envToken] != "test-token" || vals[envClientID] != "id" {
		t.Fatalf("env %v %v", vals, err)
	}
	if st, _ := os.Stat(env); st.Mode().Perm() != 0o600 {
		t.Fatalf("env perms %v", st.Mode())
	}
	code, _, _ = runCLI(t, dir, api, "", "auth", "logout")
	vals, _ = readDotEnv(env)
	if code != 0 || vals[envToken] != "" || vals[envSecret] != "secret" {
		t.Fatalf("after logout: %v", vals)
	}
}

func TestDotEnvParsing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	os.WriteFile(p, []byte("# c\nexport A=1\nB=\"two words\"\nC='x y'\nD=plain # note\n\n"), 0o600)
	v, err := readDotEnv(p)
	if err != nil {
		t.Fatal(err)
	}
	if v["A"] != "1" || v["B"] != "two words" || v["C"] != "x y" || v["D"] != "plain" {
		t.Fatalf("%v", v)
	}
}

func TestEditTags(t *testing.T) {
	got := editTags([]string{"House", "Disco"}, []string{"house", "Techno"}, []string{"DISCO"})
	if strings.Join(got, ",") != "House,Techno" {
		t.Fatalf("%v", got)
	}
}

func TestParsePublishDate(t *testing.T) {
	got, err := parsePublishDate("2030-11-21T15:05:00+01:00")
	if err != nil || got.Format(time.RFC3339) != "2030-11-21T14:05:00Z" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := parsePublishDate("tomorrow"); err == nil {
		t.Fatal("expected error")
	}
}
