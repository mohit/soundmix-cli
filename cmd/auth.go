package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mixcloud-cli/mixcloud"
)

func newAuthCmd(g *globalOptions) *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Log in to Mixcloud and manage the stored access token",
	}
	c.AddCommand(newAuthURLCmd(g), newAuthLoginCmd(g), newAuthStatusCmd(g), newAuthLogoutCmd(g))
	return c
}

type oauthFlags struct {
	clientID, clientSecret, redirectURI string
}

func (o *oauthFlags) register(c *cobra.Command, withSecret bool) {
	c.Flags().StringVar(&o.clientID, "client-id", "", "application client ID (default $"+envClientID+")")
	if withSecret {
		c.Flags().StringVar(&o.clientSecret, "client-secret", "", "application client secret (default $"+envSecret+")")
	}
	c.Flags().StringVar(&o.redirectURI, "redirect-uri", "", "OAuth redirect URI (default $"+envRedirectURI+"; empty shows the code on screen)")
}

func (o *oauthFlags) config(needSecret bool) (mixcloud.OAuthConfig, error) {
	cfg := mixcloud.OAuthConfig{
		ClientID:     firstNonEmpty(o.clientID, os.Getenv(envClientID)),
		ClientSecret: firstNonEmpty(o.clientSecret, os.Getenv(envSecret)),
		RedirectURI:  firstNonEmpty(o.redirectURI, os.Getenv(envRedirectURI)),
	}
	if cfg.ClientID == "" {
		return cfg, usagef("client ID is required: pass --client-id or set %s", envClientID)
	}
	if needSecret && cfg.ClientSecret == "" {
		return cfg, usagef("client secret is required: pass --client-secret or set %s", envSecret)
	}
	return cfg, nil
}

func newAuthURLCmd(g *globalOptions) *cobra.Command {
	var o oauthFlags
	c := &cobra.Command{
		Use:   "url",
		Short: "Print the URL where the account owner authorizes this app",
		Long: `Print the Mixcloud authorization URL. The account owner opens it in a
browser and approves access; Mixcloud then shows a code (or redirects to the
redirect URI with ?code=...). Pass that code to "auth login --code".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := o.config(false)
			if err != nil {
				return err
			}
			client, err := g.client(false)
			if err != nil {
				return err
			}
			u := client.AuthorizeURL(cfg)
			if g.jsonOut {
				return g.printJSON(map[string]string{"url": u})
			}
			g.printf("%s\n", u)
			return nil
		},
	}
	o.register(c, false)
	return c
}

func newAuthLoginCmd(g *globalOptions) *cobra.Command {
	var (
		o      oauthFlags
		code   string
		noSave bool
	)
	c := &cobra.Command{
		Use:   "login",
		Short: "Authorize this app and save an access token to the .env file",
		Long: `Authorize mixcloud-cli to act on a Mixcloud account.

Mixcloud only supports browser-based authorization, so a person has to open
a URL and approve access once. There are three ways to finish:

  1. Interactive: run "auth login", open the printed URL, paste the code.
  2. Two-step (for agents): run "auth url", have the account owner open it,
     then run "auth login --code CODE".
  3. Local callback: register http://localhost:PORT/callback as your app's
     redirect URI and pass it with --redirect-uri; the code is captured
     automatically.

The token is verified and then written to MIXCLOUD_ACCESS_TOKEN in the
.env file (unless --no-save).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := o.config(true)
			if err != nil {
				return err
			}
			client, err := g.client(false)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if code == "" {
				code, err = obtainCode(ctx, g, client, cfg)
				if err != nil {
					return err
				}
			}
			token, err := client.ExchangeCode(ctx, cfg, extractCode(code))
			if err != nil {
				return err
			}
			g.token = token
			authed, err := g.client(true)
			if err != nil {
				return err
			}
			me, err := authed.Me(ctx)
			if err != nil {
				return fmt.Errorf("token was issued but could not be verified: %w", err)
			}
			saved := ""
			if !noSave {
				if err := setDotEnv(g.envFile, envToken, token); err != nil {
					return fmt.Errorf("saving token to %s: %w", g.envFile, err)
				}
				saved = g.envFile
			}
			if g.jsonOut {
				out := map[string]any{"username": me.Username, "name": me.Name, "is_pro": me.IsPro, "saved_to": saved}
				if noSave {
					out["access_token"] = token
				}
				return g.printJSON(out)
			}
			g.printf("Logged in as %s (%s).\n", me.Username, me.Name)
			if saved != "" {
				g.printf("Access token saved to %s.\n", saved)
			} else {
				g.printf("Access token (not saved): %s\n", token)
			}
			return nil
		},
	}
	o.register(c, true)
	c.Flags().StringVar(&code, "code", "", "authorization code (or the full redirect URL containing it)")
	c.Flags().BoolVar(&noSave, "no-save", false, "print the token instead of saving it to the .env file")
	return c
}

// obtainCode gets an authorization code either by running a local callback
// server (for localhost redirect URIs) or by prompting on stdin.
func obtainCode(ctx context.Context, g *globalOptions, client *mixcloud.Client, cfg mixcloud.OAuthConfig) (string, error) {
	authURL := client.AuthorizeURL(cfg)
	if u, err := url.Parse(cfg.RedirectURI); err == nil && isLoopback(u) {
		return waitForCallback(ctx, g, u, authURL)
	}
	if !isTerminal(g.stdin) {
		return "", usagef("no --code given and stdin is not a terminal; run \"mixcloud-cli auth url\", open the URL, then pass the code with --code")
	}
	g.infof("Open this URL in a browser and approve access:\n\n  %s\n\n", authURL)
	if cfg.RedirectURI == "" {
		g.infof("Mixcloud will show an authorization code. ")
	} else {
		g.infof("You'll be redirected to %s?code=... ", cfg.RedirectURI)
	}
	g.infof("Paste the code (or the whole redirect URL) here: ")
	line, err := bufio.NewReader(g.stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return "", fmt.Errorf("reading code: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func isLoopback(u *url.URL) bool {
	if u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func waitForCallback(ctx context.Context, g *globalOptions, redirect *url.URL, authURL string) (string, error) {
	addr := redirect.Host
	if redirect.Port() == "" {
		addr = net.JoinHostPort(redirect.Hostname(), "80")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("listening on %s for the OAuth callback: %w", addr, err)
	}
	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)
	path := redirect.Path
	if path == "" {
		path = "/"
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, "Authorization failed: "+e, http.StatusBadRequest)
			ch <- result{err: fmt.Errorf("authorization denied: %s", e)}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "Missing code", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "mixcloud-cli is authorized. You can close this window.")
		select {
		case ch <- result{code: code}:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	g.infof("Open this URL in a browser and approve access:\n\n  %s\n\nWaiting for the redirect to %s ...\n", authURL, redirect)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	select {
	case r := <-ch:
		return r.code, r.err
	case <-ctx.Done():
		return "", errors.New("timed out waiting for authorization")
	}
}

// extractCode accepts a bare code or a redirect URL containing ?code=.
func extractCode(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "code=") {
		if u, err := url.Parse(s); err == nil {
			if c := u.Query().Get("code"); c != "" {
				return c
			}
		}
		if q, err := url.ParseQuery(s); err == nil && q.Get("code") != "" {
			return q.Get("code")
		}
	}
	return s
}

func newAuthStatusCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which account the access token belongs to, and its upload quota",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := g.client(true)
			if err != nil {
				return err
			}
			q, err := effectiveQuota(cmd.Context(), g, client)
			if err != nil {
				return err
			}
			if g.jsonOut {
				return g.printJSON(map[string]any{"logged_in": true, "quota": quotaJSON(q)})
			}
			g.printf("Logged in as %s.\n%s\n", q.Username, q)
			return nil
		},
	}
}

func newAuthLogoutCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the access token from the .env file",
		Long: `Remove MIXCLOUD_ACCESS_TOKEN from the .env file. This does not revoke the
token on Mixcloud; to do that, remove the app from your Mixcloud account
settings.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := os.Stat(g.envFile); err == nil {
				if err := setDotEnv(g.envFile, envToken, ""); err != nil {
					return err
				}
			}
			if g.jsonOut {
				return g.printJSON(map[string]any{"logged_out": true, "env_file": g.envFile})
			}
			g.printf("Removed %s from %s.\n", envToken, g.envFile)
			return nil
		},
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
