package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// Environment variables read by the CLI. Values in the process environment
// take precedence over values in the .env file.
const (
	envClientID    = "MIXCLOUD_CLIENT_ID"
	envSecret      = "MIXCLOUD_CLIENT_SECRET"
	envRedirectURI = "MIXCLOUD_REDIRECT_URI"
	envToken       = "MIXCLOUD_ACCESS_TOKEN"
	envAPIURL      = "MIXCLOUD_API_URL"
	envOAuthURL    = "MIXCLOUD_OAUTH_URL"
	envEnvFile     = "MIXCLOUD_ENV_FILE"
)

// loadDotEnv reads KEY=VALUE pairs from path into the process environment,
// without overriding variables that are already set. A missing file is not
// an error.
func loadDotEnv(path string) error {
	vals, err := readDotEnv(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for k, v := range vals {
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
	return nil
}

func readDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		out[strings.TrimSpace(k)] = unquote(strings.TrimSpace(v))
	}
	return out, sc.Err()
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"') {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1]
	}
	// Strip trailing " # comment" on unquoted values.
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

// setDotEnv sets (or with value "" removes) key in the .env file at path,
// preserving other lines, and makes the file readable only by its owner.
func setDotEnv(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var lines []string
	if len(data) > 0 {
		lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	found := false
	out := lines[:0]
	for _, l := range lines {
		t := strings.TrimPrefix(strings.TrimSpace(l), "export ")
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key {
			if found || value == "" {
				continue
			}
			out = append(out, key+"="+value)
			found = true
			continue
		}
		out = append(out, l)
	}
	if !found && value != "" {
		out = append(out, key+"="+value)
	}
	content := strings.Join(out, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
