# 002: Mixcloud API notes

Source of truth: <https://www.mixcloud.com/developers/> (reviewed 2026-11-14).
This file records what the docs say **and** what we observed against the live
API, because several behaviours aren't documented. Add to it, with dates,
whenever you learn something new.

## Documented

- Base `https://api.mixcloud.com/`; objects are addressed by key
  (`/username/slug/`). JSON, CORS.
- OAuth 2.0, browser only: `https://www.mixcloud.com/oauth/authorize?client_id=…[&redirect_uri=…]`,
  then `GET https://www.mixcloud.com/oauth/access_token?client_id&redirect_uri&client_secret&code`.
  Omitting `redirect_uri` makes Mixcloud show the code on screen.
- Token goes in the `access_token` query parameter. `/me/` needs it.
- Lists: `limit`, `offset`, `since`, `until`; `paging.next/previous` links
  page by date unless the first request has `offset`.
- Upload: `POST /upload/` multipart. Fields: `mp3` (≤ 4.0 GB), `name`
  (required), `picture` (≤ 10.0 MB), `description` (≤ 1000 chars),
  `tags-N-tag` (N 0–4), `unlisted`, `publish_date` (Pro, `YYYY-MM-DDTHH:MM:SSZ` UTC),
  `disable_comments` (Pro), `hide_stats` (Pro), `hosts-N-username` (Pro, N 0–1),
  `sections-N-{artist,song,chapter,start_time}`.
- Edit: `POST /upload/{key}/edit/`, all upload fields except `mp3`, plus
  `publish` / `unpublish` (at most one of unlisted/publish/unpublish). Name,
  description and picture change only if sent; tags, sections and hosts are
  replaced wholesale; `hosts-0-username=` clears hosts.
- Delete: `DELETE /upload/{key}/delete/` (or POST `method=delete`). 404 for
  missing/other users' shows.
- Rate limits per action type; `403` + `Retry-After` header + `error.retry_after`
  (`RateLimitException`).
- No audio streams via the API.

## Observed on the live API (2026-11-14, free account)

- **Token exchange** works with the code-on-screen flow (no `redirect_uri`).
  We accept both JSON and `access_token=…` bodies.
- **Upload response:** `{"result": {"success": true, "message": "Uploaded <name>", "key": "/user/slug/"}}`.
  Edit response is the same shape with message `"Upload updated"` and no key.
- **Short audio is accepted on upload.** A 1-minute `--unlisted` upload
  succeeded.
- **Publishing a show under 15 minutes silently does nothing.** `publish=true`
  on the 1-minute upload returned `success: true, "Upload updated"`, but the
  show stayed private (`created_time` null, anonymous GET 404). A 16-minute
  show published fine. The site's rule is "Upload mixes, shows or podcasts
  over 15 minutes long". Hence the CLI's pre-check and post-publish verification.
- **Private/unlisted uploads and drafts are not listed**: absent from
  `/me/cloudcasts/` and `/username/cloudcasts/` (with token), even with
  `private=1`, `include_private=1`, `unlisted=1`, `drafts=1`. Endpoints such as
  `/me/drafts/`, `/me/uploads/`, `/me/tracks/` return 404. They can be fetched
  by key with the owner's token.
- **`cloudcast_count` counts published shows only**, so it matches the
  site's "N/10 free published shows used". Unlisted uploads didn't change it.
- **Listing and `cloudcast_count` lag a publish by minutes**, while the show
  itself is immediately reachable by key. Search lagged too.
- **`created_time`** is `null` until first publish, then stays set through
  unpublish/unlist.
- **Visibility via anonymous GET (no token):** public → 200, unlisted
  (after publish then unlist) → 200, draft (unpublished) → 404, never-published
  private upload → 404.
- Titles get their first letter capitalised ("mixcloud-cli test" →
  "Mixcloud-cli test"); tag case is normalised ("API" → "Api").
- Single-show responses include `sections` as
  `{start_time, section_type, chapter | track{name, artist{name}}}`;
  chapter + track sections round-tripped correctly.
- 404 message for delete of a missing show: "Upload does not exist or does
  not belong to the authenticated user."
- `?metadata=1` responses embed the access token in connection URLs, so
  never log them.

## Open questions

- **Tracks.** The site now distinguishes shows (> 15 min) from tracks; the
  API docs don't mention tracks, and no `/tracks/` endpoint was found. A
  short upload via the API becomes an unpublishable private show, not a
  track. Revisit if Mixcloud documents a track upload endpoint.
- Does an unlisted show that was previously published count towards the
  10-show limit? (`cloudcast_count` suggests not.) Unverified.
- Pro-only fields (`publish_date`, `disable_comments`, `hide_stats`, hosts)
  weren't tested live (free account). Booleans are sent as `true`/`false`.
  Whether `disable_comments=false` turns comments back on is unverified.
- There's no documented way to remove all tags or the whole tracklist; the
  library refuses empty lists rather than guess.
- Does publishing a long, freshly uploaded show fail while audio is still
  processing? Not seen with a 4 MB file; `publish` retries its reachability
  check for ~10 s and the error message mentions processing.
