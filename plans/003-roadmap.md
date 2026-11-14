# 003: Roadmap

Candidate next steps, roughly in priority order. Move items into their own
plan file when work starts.

1. **Publish the repository.** Create it on GitHub, then change the module
   path from `mixcloud-cli` to `github.com/<owner>/mixcloud-cli` (`go mod edit -module …`
   plus an import rewrite) so `go install …@latest` works.
2. **Batch uploads from a manifest.** `uploads create --manifest shows.yaml`
   (or JSON) with per-show metadata, running every pre-flight check for the
   whole batch before the first upload, and respecting the upload rate limit.
3. **Tracks.** If Mixcloud documents track uploads in the API, add
   `tracks create` with its own length rules (see open questions in 002).
4. **Import the ledger from the site.** There's no API for private uploads;
   `uploads track KEY` could at least let users add shows made on the
   website to the local record.
5. **Resumable/robust large uploads.** Retry on connection loss with
   exponential backoff (re-sending the file), opt-in via `--retries`.
6. **Account actions** the API supports: follow, favorite, repost,
   listen-later (`POST`/`DELETE` on `…/follow/` etc.).
7. **Release automation.** The release workflow builds binaries on `v*`
   tags; add a container image push once a registry is chosen.
