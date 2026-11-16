# 004: SoundCloud support (tracks and shows, free-plan features)

Status: implemented (2026-11-16), not yet tested against the live API.

## Goal

Let soundmix-cli upload and manage **tracks and shows on SoundCloud** using
the features available to a **free** SoundCloud account, with the same
agent-friendly behaviour as the Mixcloud side: `--json`, structured errors,
stable exit codes, pre-flight checks before any audio is sent.

## Sources (read 2026-11-16)

- <https://developers.soundcloud.com/docs/building-with-ai> and
  <https://developers.soundcloud.com/docs/llm-context>
- OpenAPI spec: <https://developers.soundcloud.com/docs/api/explorer/api.json>
  (source of truth for fields; OpenAPI 3.0.4)
- API Guide: <https://developers.soundcloud.com/docs/api/guide>
- Rate limits: <https://developers.soundcloud.com/docs/api/rate-limits>

## What the API gives us

| Need | Endpoint | Notes |
|---|---|---|
| Log in | `https://secure.soundcloud.com/authorize` + `POST https://secure.soundcloud.com/oauth/token` | OAuth 2.1, **PKCE (S256) required**, registered `redirect_uri` required, `state` for CSRF. Client secret required (all clients confidential). |
| Stay logged in | `grant_type=refresh_token` | Access tokens last ~1 h. **Refresh tokens are single-use**: the new one must be saved before anything else happens. |
| Log out | `POST /disconnect` | Revokes the app's tokens for that user (204). |
| Who am I / quota | `GET /me` | `quota.{unlimited_upload_quota, upload_seconds_used, upload_seconds_left}`, `plan`, `track_count`, `private_tracks_count`. |
| Upload | `POST /tracks` multipart | `track[title]`*, `track[asset_data]`*, `track[artist]`, `description`, `genre`, `tag_list`, `sharing` (public/private), `permalink`, `artwork_data`, `downloadable`, `streamable`, `commentable`, `embeddable_by`, `license`, `release_date`, `label_name`, `purchase_url`, `isrc`. 201 → Track. |
| Edit | `PUT /tracks/{urn}` JSON `{"track": {…}}` | Same fields minus audio; artist is `metadata_artist`. **Artwork on edit is "Only for PRO users"**, so it's not offered. Audio can't be replaced. |
| Delete | `DELETE /tracks/{urn}` | 200 / 404. |
| List mine | `GET /me/tracks?linked_partitioning=true` | Includes private tracks (unlike Mixcloud), so no local record is needed. |
| Get | `GET /tracks/{urn}`, `GET /resolve?url=` | IDs are URNs `soundcloud:tracks:123`; permalinks resolve. |
| Playlists | `GET /me/playlists`, `POST /playlists`, `PUT /playlists/{urn}` (tracks list replaced wholesale), `DELETE /playlists/{urn}` | Free accounts can make playlists. |

Errors are JSON `{code, message, status, errors: [{error_message}]}`. 429 with
`Retry-After` and `X-Ratelimit-*` headers; general budget ~100 req/min per app.

## Free vs paid

- **Registering an API app requires SoundCloud Artist Pro** (per "Get an API
  key"). Whoever owns the app needs it. The *accounts that authorize the app*
  can be free, and every feature below works for them.
- Free accounts have a limited total upload time. We don't hardcode it; we
  read `quota.upload_seconds_left` and refuse an upload longer than what's
  left (unless `unlimited_upload_quota`).
- Not offered as Pro-only or not in the API: replacing audio, artwork on
  edit, scheduled releases, quiet mode (`reveal_stats` / `reveal_comments`,
  whose plan requirements aren't documented, so they're left out rather
  than guessed).

## Tracks vs shows

SoundCloud has one content type: the track. A **show** (mix, radio show,
podcast episode) is a long track with a tracklist. So:

- `soundcloud tracks …` with `shows` as an alias of the same command group.
- `--tracklist FILE` / `--section` (same format as Mixcloud) are rendered
  into the description as a timestamped list, because SoundCloud has no
  chapter/section field.
- `--playlist NAME|URN` on upload adds the new track to a playlist (creating
  a private one if the name doesn't exist), which works as a show's
  "series" page.

## CLI shape

```
soundmix-cli soundcloud (alias: sc)
  auth url | login | status | logout
  quota
  tracks (alias: shows) list | get | create | edit | delete
  playlists list | get | create | add | remove | delete
soundmix-cli mixcloud (alias: mc)
  auth | uploads | search          (the existing commands)
```

The existing top-level `auth`, `uploads` and `search` stay as hidden
aliases of `mixcloud …`, so nothing that already works breaks.

## Design decisions

- **New package `soundcloud/`**, a sibling of `mixcloud/`. Shared code moves
  out so neither depends on the other:
  - `media/`: audio duration (MP3 native, ffprobe fallback) and the
    tracklist parser (public; `mixcloud` re-exports via aliases).
  - `internal/formdata/`: streaming multipart with exact Content-Length
    and progress.
- **Token handling.** The library takes a `TokenSource`; the CLI's source
  keeps `SOUNDCLOUD_ACCESS_TOKEN`, `SOUNDCLOUD_REFRESH_TOKEN` and
  `SOUNDCLOUD_TOKEN_EXPIRES_AT` in `.env`. It refreshes when the token is
  within 2 minutes of expiry or after a 401, under a lock file, re-reading
  `.env` first. That way parallel agent invocations don't burn the
  single-use refresh token twice. The new tokens are written before the
  request is retried.
- **PKCE across two invocations.** `auth url` stores the verifier, state and
  redirect URI in `.soundmix-oauth-pending.json` (0600, gitignored, expires
  after 15 min). `auth login --code <code or redirect URL>` uses it and
  checks `state` when it's present. A loopback `--redirect-uri` captures the
  code automatically, as on Mixcloud.
- **Pre-flight checks:** title required; audio ≤ 4 GiB and ≤ 24 h; length ≤
  remaining upload quota; enum values (sharing, license, embeddable_by);
  release date format; artwork is gif/jpeg/png. Length comes from
  `media.Duration` (MP3 native, otherwise ffprobe). If the length can't be
  determined, the quota check is skipped with a warning rather than
  blocking WAV/FLAC users without ffprobe.
- **Tags:** given as a list and encoded to `tag_list` (space-separated,
  multi-word tags quoted).
- **Retries:** 429 waits for `Retry-After` (≤ 2 min, up to 3 times) on
  requests without a streamed body; 401 triggers one refresh + retry on
  those requests. Uploads refresh proactively before starting and are
  never re-sent automatically.
- **Exit codes** are shared with Mixcloud (2 invalid, 3 auth, 4 not found,
  5 rate limited).

## Testing

- Stub servers for the `soundcloud` package (OAuth with PKCE, refresh
  rotation, upload multipart fields, update JSON, delete, paging,
  errors/429) and for the CLI (quota pre-check, `.env` token rotation,
  pending-state flow, playlist add on upload).
- Live testing needs SoundCloud app credentials, which need Artist Pro.
  Until then the stub tests are the only verification, and this doc says so.

## Later

- `soundmix-cli upload FILE --to mixcloud,soundcloud`: one command that
  runs both platforms' pre-flight checks, then cross-posts.
- Track processing status after upload (the spec has no `state` field; check
  the live response).
