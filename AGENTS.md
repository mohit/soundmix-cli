# AGENTS.md

Instructions for AI agents (and people) working **on** this repository and
working **with** the `mixcloud-cli` tool. Design notes live in [`plans/`](plans/).

## What this is

`mixcloud-cli` is a Go CLI and library for managing a Mixcloud account's
uploads: upload shows, edit metadata/tracklists/cover art, publish,
unpublish, unlist, delete, check the free-account quota, and search.

- `mixcloud/`: the API client library (package `mixcloud`), no CLI concerns.
- `cmd/`: the cobra CLI (package `cmd`), including `.env` handling and the
  local upload record (`ledger.go`).
- `main.go`: entry point.
- `plans/`: design docs and verified API behaviour. Read
  `plans/002-mixcloud-api-notes.md` before changing upload/publish logic.

## Developing

```bash
go build -o mixcloud-cli .
go vet ./...
gofmt -l .          # must print nothing
go test -race ./...
```

- Go 1.24+. Keep `go.mod` at `go 1.24`; check that a new dependency doesn't
  raise it (e.g. `golang.org/x/term` is pinned to v0.40.0 for this reason).
- Keep dependencies minimal: cobra and x/term only. No viper; config is env
  vars plus `.env`.
- Tests use local stubs (`httptest`), never the real API. `mixcloud/stub_test.go`
  and `cmd/cmd_test.go` have fake servers; extend them for new endpoints.
  `TestMP3DurationFFmpeg` uses ffmpeg when installed and is skipped otherwise.
- Every user-visible behaviour change needs a test, and docs updated in the
  same change: README, this file, and `plans/` if it changes a design decision.
- Library errors are `*mixcloud.Error` (API), `*mixcloud.ValidationError`
  (pre-flight). The CLI maps them to exit codes in `cmd/root.go`.

### Credentials and secrets

- Real credentials live only in `.env` (gitignored, mode 0600). `.env.example`
  is the committed template; keep it in sync when adding variables.
- Never commit `.env` or `.mixcloud-uploads.json`, and never print, log or paste
  an access token or client secret. The client redacts `access_token`,
  `client_secret` and `code` from error messages; keep it that way. Note
  that the API's own `metadata=1` responses embed the token in URLs.

### Testing against the real Mixcloud API

Only when stubs can't answer the question, and with the account owner's OK:

- Uploads count against the account (a free account can publish only 10 shows).
  Upload with `--unlisted`, use tiny low-bitrate files (e.g.
  `ffmpeg -f lavfi -i sine=duration=965 -ac 1 -b:a 32k test.mp3`, 16 min, ~4 MB),
  and delete everything you created before finishing.
- Record anything new you learn about API behaviour in
  `plans/002-mixcloud-api-notes.md`, with the date.

## Using the CLI (for agents)

Always pass `--json`. Results go to stdout as JSON; errors go to stderr as a
JSON object `{"error", "exit_code", "problems"?, "type"?, "status"?,
"retry_after_seconds"?, "details"?}`. Progress lines go to stderr.

### Exit codes

| Code | Meaning | What to do |
|-----:|---------|------------|
| 0 | Success | |
| 1 | Other error (network, publish didn't take effect, …) | Read `error` |
| 2 | Invalid input / failed pre-flight check | Fix the input; `problems` lists every issue |
| 3 | Not logged in, or token invalid/revoked | Ask the account owner to log in again (below) |
| 4 | Show not found (or not yours) | Check the key |
| 5 | Rate limited | Wait `retry_after_seconds`, then retry |

### Setup and login

Configuration comes from the environment or `.env` (process env wins).
Mixcloud only supports **browser-based** authorization, so a human must
approve access once:

```bash
mixcloud-cli auth url --json         # give this URL to the account owner
mixcloud-cli auth login --code CODE  # they paste back the code (or full redirect URL)
mixcloud-cli auth status --json      # verify
```

The token is saved to `MIXCLOUD_ACCESS_TOKEN` in `.env`. Never ask for the
user's Mixcloud password.

### Shows are keys

A show is identified by its key: `username/slug`, `/username/slug/` or a
`https://www.mixcloud.com/username/slug/` URL all work.

### Uploading a show

```bash
mixcloud-cli --json uploads quota
mixcloud-cli --json uploads create mix.mp3 --name "Friday Mix" \
  -t House -t Disco --picture cover.jpg --tracklist tracklist.txt \
  --description "Recorded live" --dry-run      # check first; nothing is sent
mixcloud-cli --json uploads create mix.mp3 ...  # same flags, without --dry-run
```

Pre-flight checks (run before any bytes are sent; failures exit 2):

- audio **at least 15 minutes** (Mixcloud's rule for shows), at most 4 GiB.
  MP3 length is read natively; other formats need `ffprobe`.
- free accounts: at most **10 published shows** (skipped for `--unlisted`).
- name required; description ≤ 1000 characters; ≤ 5 tags; ≤ 2 `--host`;
  cover art ≤ 10 MiB; `--publish-date` must be in the future.

`--skip-length-check` / `--skip-quota-check` exist but are almost never
right. Mixcloud accepts short uploads and then **silently refuses to publish
them**.

Pro-only flags: `--publish-date`, `--disable-comments`, `--hide-stats`, `--host`.

### Tracklist format

One section per line; `#` comments and blank lines are ignored. Times are
`SS`, `MM:SS` or `HH:MM:SS`. Sections are sorted by time when every line
has one.

```text
0:00 | Introduction                 # chapter: time | name
2:30 | Artist Name | Song Title     # track:   time | artist | song
| Artist | Song Without Time        # time is optional in pipe form
10:00 Another Artist - Another Song # plain:   time Artist - Song
1:02:03 Closing Chapter             # plain without " - " is a chapter
```

Pass a file with `--tracklist FILE` (`-` for stdin) and/or single lines with
`--section "2:30 | Artist | Song"` (repeatable).

### Managing existing shows

```bash
mixcloud-cli --json uploads list --include-private   # yours, incl. private/drafts known locally
mixcloud-cli --json uploads get KEY                  # details + status
mixcloud-cli --json uploads edit KEY --name "New name" --add-tag Techno --remove-tag Disco
mixcloud-cli --json uploads edit KEY --tracklist new-tracklist.txt
mixcloud-cli --json uploads publish KEY      # verifies it's actually public afterwards
mixcloud-cli --json uploads unpublish KEY    # to drafts (not publicly reachable)
mixcloud-cli --json uploads unlist KEY       # link-only
mixcloud-cli --json uploads delete KEY --yes # permanent; needs --yes when non-interactive
```

Gotchas, all verified against the live API:

- **Edits replace lists wholesale.** `--tag`, `--tracklist`/`--section` and
  `--host` replace every existing value. Use `--add-tag`/`--remove-tag` to
  keep the other tags. Omitted fields (name, description, picture) are kept.
- **The API doesn't list private uploads or drafts.** The CLI records every
  upload/visibility change it makes in `.mixcloud-uploads.json` (next to
  `.env`; override with `MIXCLOUD_LEDGER`), and `list --include-private`
  merges those in. Shows made elsewhere that are private can only be fetched
  by key.
- **Newly published shows take minutes to appear** in listings and in the
  account's show count. `quota` corrects for shows this CLI published.
- **"success" isn't proof.** A publish of a show under 15 minutes returns
  success and does nothing. `publish` checks reachability afterwards and
  exits 1 if the show isn't public.
- Mixcloud capitalises the first letter of titles and normalises tag case
  (`API` → `Api`).
- `get`'s `status` is one of `public`, `unlisted`, `draft` (includes
  never-published private uploads), `scheduled`.

### Searching

```bash
mixcloud-cli --json search "nick warren" --include 1995 --limit 10
mixcloud-cli --json search techno --type user
```

Not supported by Mixcloud's API: audio streaming/downloads, and listing
private uploads made outside this CLI.
