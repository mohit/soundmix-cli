# 001: Architecture

Status: implemented (2026-11-14)

## Goal

Rebuild [dreddick-home/mixcloudclient](https://github.com/dreddick-home/mixcloudclient)
(a 2020 search-only CLI, Go 1.14, no auth) as a tool whose main job is
**managing a user's uploads** on Mixcloud through the current API, and which
AI agents can drive reliably.

## What was kept from the original

- Go + cobra CLI; Apache-2.0 licence.
- `search` with client-side include/exclude filters. It now pages by the
  API's `paging.next` links instead of guessing offsets, and filters on name
  and key case-insensitively.

## What changed, and why

| Area | Original | Now | Why |
|---|---|---|---|
| Name | `mixcloudclient` | `soundmix-cli` (repo, binary, module `github.com/mohit/soundmix-cli`); library package `mixcloud` | Go naming: lowercase, hyphenated repo/binary; package names are single lowercase words. The tool's own name doesn't use the Mixcloud brand; the package is named for the API it wraps |
| Scope | search | uploads: create/edit/publish/unpublish/unlist/delete/list/get/quota + search | requested focus |
| Auth | none | OAuth 2.0 (browser code flow), token in `.env` | required for every write and `/me/` |
| Config | viper + `~/.mixcloudclient.yaml` | env vars + `.env` / `.env.example` | requested; fewer dependencies |
| Concurrency | goroutine fan-out over offsets | sequential paging | API is rate limited per action type; fan-out would trip it |
| Tests | filter unit tests | stub-server tests for library and CLI | |

## Layout

```
main.go          entry point
cmd/             CLI: root (flags, exit codes, JSON errors), auth, uploads, search,
                 env (.env read/write), ledger (local upload record)
mixcloud/        library: client, errors, types, oauth, reads, uploads,
                 validate (limits, quota), audio (MP3 duration), tracklist
plans/           design docs (this folder)
AGENTS.md        instructions for agents
```

The library has no CLI concerns, so it can be imported by other Go
programs (`mixcloud.New(mixcloud.WithAccessToken(...))`).

## Key design decisions

**Validate before uploading.** The API only reports validation errors after
the entire audio file (up to 4 GiB) has been received. So `UploadRequest.Validate`
checks every documented limit, and the CLI adds two checks that need I/O:
audio length ≥ 15 min (`MP3Duration`, ffprobe fallback) and the free-account
published-show quota. `--dry-run` runs everything without sending.

**Stream uploads with an exact Content-Length.** The multipart body is
written through an `io.Pipe`, so files are never buffered. The length is
computed up front by rendering the form headers with a fixed boundary and
adding the known file sizes, so the request isn't sent chunked (some
proxies reject that). If the body is shorter than declared, the upload
fails instead of hanging.

**Verify, don't trust.** Mixcloud returns `success: true` for a publish that
it silently ignores (shows under 15 min). `publish` checks the show's length
first and confirms anonymous reachability afterwards
(`Client.PubliclyReachable`).

**Local upload record (ledger).** The API can't list private uploads or
drafts, and listings lag behind publishes by minutes. The CLI records keys
and the visibility it last set in `.soundmix-uploads.json`. That file is used for:
`list --include-private`, accurate `status`, and a quota count that includes
shows published but not yet counted. It's best-effort: failures to write it
only warn, and shows managed outside the CLI are unknown to it.

**Status model.** Derived per show: in profile listing → `public`;
anonymously unreachable → `draft` (or `scheduled` if the ledger says so);
reachable and ledger says public → `public`; otherwise reachable shows are
checked against the owner's listing, and those not in it are `unlisted`.

**Agent-friendly output.** `--json` on every command; JSON errors on stderr
with stable exit codes (0 ok, 1 error, 2 invalid, 3 auth, 4 not found,
5 rate limited); progress on stderr as whole lines when not a TTY;
destructive commands refuse to run non-interactively without `--yes`.

**Rate limits.** `WithRateLimitRetry(maxWait)` retries reads/deletes (no
body) up to 3 times when `Retry-After` ≤ maxWait (CLI uses 2 min). Uploads
and edits are never retried automatically, because re-sending a multi-GB
body should be the caller's decision.

**Token safety.** The token is sent as the `access_token` query parameter
(as documented). `url.Error`s are rewritten to redact `access_token`,
`client_secret` and `code`; paging links are only followed if they point at
the API host, so a tampered response can't exfiltrate the token.
