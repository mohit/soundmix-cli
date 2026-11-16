// Package cmd implements the soundmix-cli command line.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/mohit/soundmix-cli/mixcloud"
)

// Exit codes, documented in AGENTS.md.
const (
	exitError       = 1
	exitInvalid     = 2
	exitAuth        = 3
	exitNotFound    = 4
	exitRateLimited = 5
)

// Version is set at build time with -ldflags "-X github.com/mohit/soundmix-cli/cmd.Version=...".
var Version = "dev"

type globalOptions struct {
	jsonOut bool
	envFile string
	token   string
	stdout  io.Writer
	stderr  io.Writer
	stdin   io.Reader
}

// usageError marks errors caused by bad input, so they exit with code 2.
type usageError struct{ error }

func usagef(format string, a ...any) error { return usageError{fmt.Errorf(format, a...)} }

// Execute runs the CLI and exits with an appropriate status.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	g := &globalOptions{stdout: stdout, stderr: stderr, stdin: stdin}
	root := newRootCmd(g)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	return reportError(g, err)
}

func newRootCmd(g *globalOptions) *cobra.Command {
	root := &cobra.Command{
		Use:   "soundmix-cli",
		Short: "Manage Mixcloud uploads from the command line",
		Long: `soundmix-cli uploads, edits, publishes and deletes shows on Mixcloud,
and searches Mixcloud. It is designed to be driven by people and AI agents:
every command accepts --json for machine-readable output.

Credentials are read from the environment or a .env file (see .env.example).
Run "soundmix-cli auth login" once to get an access token.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			path := g.envFile
			if path == "" {
				path = os.Getenv(envEnvFile)
			}
			if path == "" {
				path = ".env"
			}
			g.envFile = path
			return loadDotEnv(path)
		},
	}
	pf := root.PersistentFlags()
	pf.BoolVar(&g.jsonOut, "json", false, "print machine-readable JSON")
	pf.StringVar(&g.envFile, "env-file", "", "path to the .env file (default .env, or $"+envEnvFile+")")
	pf.StringVar(&g.token, "token", "", "access token (default $"+envToken+")")

	root.AddCommand(newAuthCmd(g), newUploadsCmd(g), newSearchCmd(g))
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error { return usageError{err} })
	return root
}

// client builds an API client. If needToken is set and no token is
// available, it returns an error pointing at "auth login".
func (g *globalOptions) client(needToken bool) (*mixcloud.Client, error) {
	token := g.token
	if token == "" {
		token = os.Getenv(envToken)
	}
	if needToken && token == "" {
		return nil, authError{errors.New(`not logged in: run "soundmix-cli auth login" or set ` + envToken)}
	}
	opts := []mixcloud.Option{
		mixcloud.WithAccessToken(token),
		mixcloud.WithUserAgent("github.com/mohit/soundmix-cli/" + Version),
		mixcloud.WithRateLimitRetry(2 * time.Minute),
	}
	if u := os.Getenv(envAPIURL); u != "" {
		opts = append(opts, mixcloud.WithBaseURL(u))
	}
	if u := os.Getenv(envOAuthURL); u != "" {
		opts = append(opts, mixcloud.WithOAuthBaseURL(u))
	}
	return mixcloud.New(opts...)
}

type authError struct{ error }

func (g *globalOptions) printJSON(v any) error {
	enc := json.NewEncoder(g.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (g *globalOptions) printf(format string, a ...any) { fmt.Fprintf(g.stdout, format, a...) }

func (g *globalOptions) infof(format string, a ...any) { fmt.Fprintf(g.stderr, format, a...) }

func reportError(g *globalOptions, err error) int {
	code := exitError
	out := map[string]any{"error": err.Error()}

	var apiErr *mixcloud.Error
	var valErr *mixcloud.ValidationError
	var uErr usageError
	var aErr authError
	switch {
	case errors.As(err, &valErr):
		code = exitInvalid
		out["problems"] = valErr.Problems
	case errors.As(err, &uErr):
		code = exitInvalid
	case errors.As(err, &aErr), mixcloud.IsAuthError(err):
		code = exitAuth
	case mixcloud.IsNotFound(err):
		code = exitNotFound
	case mixcloud.IsRateLimited(err):
		code = exitRateLimited
	}
	if errors.As(err, &apiErr) {
		out["type"] = apiErr.Type
		out["status"] = apiErr.StatusCode
		if apiErr.RetryAfter > 0 {
			out["retry_after_seconds"] = int(apiErr.RetryAfter.Seconds())
		}
		if len(apiErr.Details) > 0 {
			out["details"] = apiErr.Details
		}
	}
	if errors.Is(err, context.Canceled) {
		out["error"] = "interrupted"
	}
	out["exit_code"] = code

	if g.jsonOut {
		enc := json.NewEncoder(g.stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	} else {
		fmt.Fprintln(g.stderr, "Error:", out["error"])
	}
	return code
}
