package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/mohit/soundmix-cli/soundcloud"
)

// Environment variables for SoundCloud.
const (
	envSCClientID    = "SOUNDCLOUD_CLIENT_ID"
	envSCSecret      = "SOUNDCLOUD_CLIENT_SECRET"
	envSCRedirectURI = "SOUNDCLOUD_REDIRECT_URI"
	envSCToken       = "SOUNDCLOUD_ACCESS_TOKEN"
	envSCRefresh     = "SOUNDCLOUD_REFRESH_TOKEN"
	envSCExpiresAt   = "SOUNDCLOUD_TOKEN_EXPIRES_AT"
	envSCAPIURL      = "SOUNDCLOUD_API_URL"
	envSCAuthURL     = "SOUNDCLOUD_AUTH_URL"
)

func newSoundCloudCmd(g *globalOptions) *cobra.Command {
	c := &cobra.Command{
		Use:     "soundcloud",
		Aliases: []string{"sc"},
		Short:   "Upload and manage tracks, shows and playlists on SoundCloud",
		Long: `Upload and manage tracks, shows and playlists on SoundCloud using the
features of a free SoundCloud account.

SoundCloud has no separate "show" type: a show (mix, radio show, podcast
episode) is a track. "shows" is an alias of "tracks"; use --tracklist to
add a timestamped tracklist to the description and --playlist to collect
episodes in a playlist.

Setup: put SOUNDCLOUD_CLIENT_ID, SOUNDCLOUD_CLIENT_SECRET and
SOUNDCLOUD_REDIRECT_URI in .env (see .env.example), then run
"soundmix-cli soundcloud auth login". Registering a SoundCloud API app
requires Artist Pro; the account you log in with can be free.`,
	}
	c.AddCommand(newSCAuthCmd(g), newSCQuotaCmd(g), newSCTracksCmd(g), newSCPlaylistsCmd(g))
	return c
}

func scOAuthConfig(clientID, secret, redirect string) soundcloud.OAuthConfig {
	return soundcloud.OAuthConfig{
		ClientID:     firstNonEmpty(clientID, os.Getenv(envSCClientID)),
		ClientSecret: firstNonEmpty(secret, os.Getenv(envSCSecret)),
		RedirectURI:  firstNonEmpty(redirect, os.Getenv(envSCRedirectURI)),
	}
}

func scBaseOptions() []soundcloud.Option {
	opts := []soundcloud.Option{
		soundcloud.WithUserAgent("soundmix-cli/" + Version),
		soundcloud.WithRateLimitRetry(2 * time.Minute),
	}
	if u := os.Getenv(envSCAPIURL); u != "" {
		opts = append(opts, soundcloud.WithBaseURL(u))
	}
	if u := os.Getenv(envSCAuthURL); u != "" {
		opts = append(opts, soundcloud.WithAuthURL(u))
	}
	return opts
}

// scClient builds a SoundCloud client whose tokens come from the
// environment / .env and are refreshed (and saved back) automatically.
func (g *globalOptions) scClient() (*soundcloud.Client, error) {
	tok := tokenFromEnv(os.Getenv)
	if g.token != "" {
		tok = soundcloud.Token{AccessToken: g.token}
	}
	if tok.AccessToken == "" && tok.RefreshToken == "" {
		return nil, authError{errors.New(`not logged in to SoundCloud: run "soundmix-cli soundcloud auth login"`)}
	}
	refresher, err := soundcloud.New(scBaseOptions()...)
	if err != nil {
		return nil, err
	}
	ts := &envTokenSource{g: g, refresher: refresher, cfg: scOAuthConfig("", "", ""), tok: tok, fromFlag: g.token != ""}
	return soundcloud.New(append(scBaseOptions(), soundcloud.WithTokenSource(ts))...)
}

// scAnonClient builds a client for the OAuth endpoints only.
func scAnonClient() (*soundcloud.Client, error) { return soundcloud.New(scBaseOptions()...) }

func tokenFromEnv(get func(string) string) soundcloud.Token {
	t := soundcloud.Token{AccessToken: get(envSCToken), RefreshToken: get(envSCRefresh)}
	if exp, err := time.Parse(time.RFC3339, get(envSCExpiresAt)); err == nil {
		t.Expiry = exp
	}
	return t
}

func saveSCToken(envFile string, t soundcloud.Token) error {
	exp := ""
	if !t.Expiry.IsZero() {
		exp = t.Expiry.UTC().Format(time.RFC3339)
	}
	for _, kv := range [][2]string{{envSCToken, t.AccessToken}, {envSCRefresh, t.RefreshToken}, {envSCExpiresAt, exp}} {
		if err := setDotEnv(envFile, kv[0], kv[1]); err != nil {
			return err
		}
		os.Setenv(kv[0], kv[1])
	}
	return nil
}

// envTokenSource refreshes SoundCloud tokens and writes them back to .env.
// Refresh tokens are single-use, so refreshing happens under a lock file and
// first re-reads .env: if another soundmix-cli process already refreshed,
// its token is used instead of spending the refresh token twice.
type envTokenSource struct {
	g         *globalOptions
	refresher *soundcloud.Client
	cfg       soundcloud.OAuthConfig
	tok       soundcloud.Token
	fromFlag  bool
}

func (s *envTokenSource) Token(ctx context.Context, force bool) (string, error) {
	now := time.Now()
	if !force && s.tok.Fresh(now) {
		return s.tok.AccessToken, nil
	}
	if s.fromFlag {
		return s.tok.AccessToken, nil // --token: nothing to refresh
	}
	unlock, err := lockFile(s.g.envFile + ".lock")
	if err != nil {
		return "", err
	}
	defer unlock()
	if vals, err := readDotEnv(s.g.envFile); err == nil {
		disk := tokenFromEnv(func(k string) string { return vals[k] })
		if disk.AccessToken != "" && disk.AccessToken != s.tok.AccessToken && disk.Fresh(now) {
			s.tok = disk
			return disk.AccessToken, nil
		}
		if disk.RefreshToken != "" {
			s.tok.RefreshToken = disk.RefreshToken
		}
	}
	t, err := s.refresher.Refresh(ctx, s.cfg, s.tok.RefreshToken)
	if err != nil {
		if soundcloud.IsAuthError(err) {
			return "", authError{fmt.Errorf(`SoundCloud session expired (%v): run "soundmix-cli soundcloud auth login"`, err)}
		}
		return "", err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = s.tok.RefreshToken
	}
	if err := saveSCToken(s.g.envFile, t); err != nil {
		return "", fmt.Errorf("saving refreshed SoundCloud token to %s: %w", s.g.envFile, err)
	}
	s.tok = t
	return t.AccessToken, nil
}

// lockFile takes an exclusive lock by creating path, waiting up to 20s and
// breaking locks older than a minute (left by a crashed process).
func lockFile(path string) (func(), error) {
	deadline := time.Now().Add(20 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) > time.Minute {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for lock %s", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// pendingLogin carries PKCE secrets from "auth url" to "auth login --code".
type pendingLogin struct {
	soundcloud.PKCE
	RedirectURI string    `json:"redirect_uri"`
	CreatedAt   time.Time `json:"created_at"`
}

const pendingLoginTTL = 15 * time.Minute

func (g *globalOptions) pendingLoginPath() string {
	return filepath.Join(filepath.Dir(g.envFile), ".soundmix-oauth-pending.json")
}

func (g *globalOptions) savePendingLogin(p pendingLogin) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(g.pendingLoginPath(), data, 0o600)
}

func (g *globalOptions) loadPendingLogin() (*pendingLogin, error) {
	data, err := os.ReadFile(g.pendingLoginPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, usagef(`no login in progress: run "soundmix-cli soundcloud auth url" first`)
	}
	if err != nil {
		return nil, err
	}
	var p pendingLogin
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if time.Since(p.CreatedAt) > pendingLoginTTL {
		os.Remove(g.pendingLoginPath())
		return nil, usagef(`the login started by "auth url" has expired: run "soundmix-cli soundcloud auth url" again`)
	}
	return &p, nil
}
