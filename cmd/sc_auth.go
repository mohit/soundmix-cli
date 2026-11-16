package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mohit/soundmix-cli/media"
	"github.com/mohit/soundmix-cli/soundcloud"
)

func newSCAuthCmd(g *globalOptions) *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Log in to SoundCloud and manage the stored tokens",
	}
	c.AddCommand(newSCAuthURLCmd(g), newSCAuthLoginCmd(g), newSCAuthStatusCmd(g), newSCAuthLogoutCmd(g))
	return c
}

type scOAuthFlags struct{ clientID, clientSecret, redirectURI string }

func (o *scOAuthFlags) register(c *cobra.Command) {
	c.Flags().StringVar(&o.clientID, "client-id", "", "app client ID (default $"+envSCClientID+")")
	c.Flags().StringVar(&o.clientSecret, "client-secret", "", "app client secret (default $"+envSCSecret+")")
	c.Flags().StringVar(&o.redirectURI, "redirect-uri", "", "redirect URI registered on the app (default $"+envSCRedirectURI+")")
}

func (o *scOAuthFlags) config(needSecret bool) (soundcloud.OAuthConfig, error) {
	cfg := scOAuthConfig(o.clientID, o.clientSecret, o.redirectURI)
	var missing []string
	if cfg.ClientID == "" {
		missing = append(missing, envSCClientID)
	}
	if needSecret && cfg.ClientSecret == "" {
		missing = append(missing, envSCSecret)
	}
	if cfg.RedirectURI == "" {
		missing = append(missing, envSCRedirectURI+" (SoundCloud requires a redirect URI registered on the app, e.g. http://localhost:8765/callback)")
	}
	if len(missing) > 0 {
		return cfg, usagef("missing SoundCloud settings: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func (g *globalOptions) startLogin(cfg soundcloud.OAuthConfig) (string, error) {
	p, err := soundcloud.NewPKCE()
	if err != nil {
		return "", err
	}
	if err := g.savePendingLogin(pendingLogin{PKCE: p, RedirectURI: cfg.RedirectURI, CreatedAt: time.Now().UTC()}); err != nil {
		return "", err
	}
	c, err := scAnonClient()
	if err != nil {
		return "", err
	}
	return c.AuthorizeURL(cfg, p), nil
}

func newSCAuthURLCmd(g *globalOptions) *cobra.Command {
	var o scOAuthFlags
	c := &cobra.Command{
		Use:   "url",
		Short: "Print the URL where the account owner authorizes this app",
		Long: `Print the SoundCloud authorization URL and remember the PKCE secrets for
the next step (in .soundmix-oauth-pending.json, valid 15 minutes).

The account owner opens the URL and approves access. SoundCloud then
redirects their browser to the redirect URI with ?code=...&state=...; the
page itself may not load, but its address contains what's needed. Pass that
whole address to "soundcloud auth login --code".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := o.config(false)
			if err != nil {
				return err
			}
			u, err := g.startLogin(cfg)
			if err != nil {
				return err
			}
			if g.jsonOut {
				return g.printJSON(map[string]any{"url": u, "redirect_uri": cfg.RedirectURI,
					"expires_at": time.Now().Add(pendingLoginTTL).UTC().Format(time.RFC3339)})
			}
			g.printf("%s\n", u)
			return nil
		},
	}
	o.register(c)
	return c
}

func newSCAuthLoginCmd(g *globalOptions) *cobra.Command {
	var (
		o    scOAuthFlags
		code string
	)
	c := &cobra.Command{
		Use:   "login",
		Short: "Authorize this app on SoundCloud and save tokens to the .env file",
		Long: `Authorize soundmix-cli to act on a SoundCloud account (OAuth 2.1 + PKCE).

  1. Two-step (for agents): "soundcloud auth url", the account owner opens
     it, then "soundcloud auth login --code '<redirect URL or code>'".
  2. Local callback: if the redirect URI is http://localhost:PORT/...,
     "auth login" listens there and captures the code automatically.
  3. Interactive: "auth login" in a terminal prints the URL and asks you to
     paste the redirect URL.

Tokens are verified and saved to SOUNDCLOUD_ACCESS_TOKEN,
SOUNDCLOUD_REFRESH_TOKEN and SOUNDCLOUD_TOKEN_EXPIRES_AT in .env. They are
refreshed automatically when they expire (about every hour).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cfg, err := o.config(true)
			if err != nil {
				return err
			}
			var verifier, gotState, wantState string
			switch {
			case code != "":
				p, err := g.loadPendingLogin()
				if err != nil {
					return err
				}
				if p.RedirectURI != cfg.RedirectURI {
					return usagef("redirect URI %q differs from the one used by \"auth url\" (%q)", cfg.RedirectURI, p.RedirectURI)
				}
				verifier, wantState = p.Verifier, p.State
				code, gotState = extractCodeAndState(code)
			default:
				code, gotState, wantState, verifier, err = interactiveSCLogin(ctx, g, cfg)
				if err != nil {
					return err
				}
			}
			if gotState != "" && gotState != wantState {
				return usagef("the redirect's state doesn't match this login (possible CSRF, or an old URL): run \"soundcloud auth url\" again")
			}
			anon, err := scAnonClient()
			if err != nil {
				return err
			}
			tok, err := anon.ExchangeCode(ctx, cfg, code, verifier)
			if err != nil {
				return err
			}
			os.Remove(g.pendingLoginPath())
			verify, err := soundcloud.New(append(scBaseOptions(), soundcloud.WithAccessToken(tok.AccessToken))...)
			if err != nil {
				return err
			}
			me, err := verify.Me(ctx)
			if err != nil {
				return fmt.Errorf("token was issued but could not be verified: %w", err)
			}
			if err := saveSCToken(g.envFile, tok); err != nil {
				return fmt.Errorf("saving tokens to %s: %w", g.envFile, err)
			}
			if g.jsonOut {
				return g.printJSON(map[string]any{"username": me.Username, "plan": me.Plan, "saved_to": g.envFile})
			}
			g.printf("Logged in to SoundCloud as %s.\nTokens saved to %s.\n", me.Username, g.envFile)
			return nil
		},
	}
	o.register(c)
	c.Flags().StringVar(&code, "code", "", "the redirect URL (or just its code) from the browser, after \"auth url\"")
	return c
}

func interactiveSCLogin(ctx context.Context, g *globalOptions, cfg soundcloud.OAuthConfig) (code, gotState, wantState, verifier string, err error) {
	authURL, err := g.startLogin(cfg)
	if err != nil {
		return
	}
	p, err := g.loadPendingLogin()
	if err != nil {
		return
	}
	verifier, wantState = p.Verifier, p.State
	if u, perr := url.Parse(cfg.RedirectURI); perr == nil && isLoopback(u) {
		code, gotState, err = waitForCallback(ctx, g, u, authURL)
		return
	}
	if !isTerminal(g.stdin) {
		err = usagef(`no --code given and stdin is not a terminal: run "soundmix-cli soundcloud auth url", open the URL, then pass the redirect URL with --code`)
		return
	}
	g.infof("Open this URL in a browser and approve access:\n\n  %s\n\n", authURL)
	g.infof("You'll be redirected to %s?code=... (the page may not load).\nPaste that whole address here: ", cfg.RedirectURI)
	line, rerr := bufio.NewReader(g.stdin).ReadString('\n')
	if rerr != nil && strings.TrimSpace(line) == "" {
		err = fmt.Errorf("reading code: %w", rerr)
		return
	}
	code, gotState = extractCodeAndState(line)
	return
}

func newSCAuthStatusCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the logged-in SoundCloud account, plan and upload quota",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := g.scClient()
			if err != nil {
				return err
			}
			me, err := client.Me(cmd.Context())
			if err != nil {
				return err
			}
			tok := tokenFromEnv(os.Getenv)
			if g.jsonOut {
				out := map[string]any{"logged_in": true, "username": me.Username, "plan": me.Plan, "quota": scQuotaJSON(me)}
				if !tok.Expiry.IsZero() {
					out["token_expires_at"] = tok.Expiry.UTC().Format(time.RFC3339)
				}
				return g.printJSON(out)
			}
			g.printf("Logged in to SoundCloud as %s (plan: %s).\n%s\n", me.Username, firstNonEmpty(me.Plan, "unknown"), describeSCQuota(me))
			return nil
		},
	}
}

func newSCAuthLogoutCmd(g *globalOptions) *cobra.Command {
	var local bool
	c := &cobra.Command{
		Use:   "logout",
		Short: "Revoke this app's access on SoundCloud and remove the tokens from .env",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			revoked := false
			if !local {
				if client, err := g.scClient(); err == nil {
					if err := client.Disconnect(cmd.Context()); err != nil {
						g.infof("Warning: could not revoke the token on SoundCloud: %v\n", err)
					} else {
						revoked = true
					}
				}
			}
			if _, err := os.Stat(g.envFile); err == nil {
				if err := saveSCToken(g.envFile, soundcloud.Token{}); err != nil {
					return err
				}
			}
			if g.jsonOut {
				return g.printJSON(map[string]any{"logged_out": true, "revoked": revoked, "env_file": g.envFile})
			}
			if revoked {
				g.printf("Revoked this app's SoundCloud access and removed the tokens from %s.\n", g.envFile)
			} else {
				g.printf("Removed the SoundCloud tokens from %s.\n", g.envFile)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&local, "local", false, "only remove the tokens from .env; don't revoke them on SoundCloud")
	return c
}

func newSCQuotaCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "quota",
		Short: "Show how much upload time is left on the SoundCloud account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := g.scClient()
			if err != nil {
				return err
			}
			me, err := client.Me(cmd.Context())
			if err != nil {
				return err
			}
			if g.jsonOut {
				return g.printJSON(scQuotaJSON(me))
			}
			g.printf("%s\n", describeSCQuota(me))
			return nil
		},
	}
}

func scQuotaJSON(me *soundcloud.Me) map[string]any {
	return map[string]any{
		"username":            me.Username,
		"plan":                me.Plan,
		"unlimited":           me.Quota.Unlimited,
		"upload_seconds_used": me.Quota.SecondsUsed,
		"upload_seconds_left": me.Quota.SecondsLeft,
		"public_tracks":       me.TrackCount,
		"private_tracks":      me.PrivateTracksCount,
	}
}

func describeSCQuota(me *soundcloud.Me) string {
	if me.Quota.Unlimited {
		return fmt.Sprintf("Unlimited upload time (%s used).", media.FormatDuration(me.Quota.Used()))
	}
	return fmt.Sprintf("%s of upload time used, %s left.", media.FormatDuration(me.Quota.Used()), media.FormatDuration(me.Quota.Left()))
}

var errCancelled = errors.New("cancelled")
