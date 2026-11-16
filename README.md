# soundmix-cli

A command-line tool and Go library for managing your [Mixcloud](https://www.mixcloud.com)
uploads. (An independent project, not made by or affiliated with Mixcloud.) You can upload shows, edit their details, tracklists and cover art,
publish, unpublish, unlist and delete them, check your free-account quota,
and search Mixcloud.

It is built to be driven by people **and AI agents**. Every command takes
`--json`, errors are structured and use stable exit codes, and uploads are
checked against Mixcloud's rules *before* any audio is sent. See
[AGENTS.md](AGENTS.md) for the full agent guide.

Based on [dreddick-home/mixcloudclient](https://github.com/dreddick-home/mixcloudclient)
(Apache-2.0), rewritten for the current Mixcloud API.

## Install

Requires Go 1.24+.

```bash
go install github.com/mohit/soundmix-cli@latest
```

or from a checkout:

```bash
git clone https://github.com/mohit/soundmix-cli.git && cd soundmix-cli
go build -o soundmix-cli .
```

## Set up

1. Create a Mixcloud app at <https://www.mixcloud.com/developers/create/>.
2. `cp .env.example .env` and fill in `MIXCLOUD_CLIENT_ID` and
   `MIXCLOUD_CLIENT_SECRET`. `.env` is gitignored.
3. Log in. Mixcloud only allows browser authorization:

   ```bash
   ./soundmix-cli auth login
   ```

   Open the printed URL, approve, and paste the code that Mixcloud shows.
   For a non-interactive flow, run `auth url`, then `auth login --code CODE`.
   The access token is saved to `.env`.

## Usage

```bash
soundmix-cli uploads quota
# 0/10 free published shows used

soundmix-cli uploads create mix.mp3 --name "Friday Mix" \
  -t House -t Disco --picture cover.jpg \
  --tracklist tracklist.txt --description "Recorded live" --dry-run

soundmix-cli uploads create mix.mp3 --name "Friday Mix" ...   # for real
soundmix-cli uploads list --include-private
soundmix-cli uploads get myname/friday-mix
soundmix-cli uploads edit myname/friday-mix --add-tag Techno --remove-tag Disco
soundmix-cli uploads unpublish myname/friday-mix
soundmix-cli uploads publish myname/friday-mix
soundmix-cli uploads delete myname/friday-mix

soundmix-cli search "nick warren" --include 1995
```

Run `soundmix-cli <command> --help` for every flag.

### Tracklists

```text
0:00 | Introduction
2:30 | Artist Name | Song Title
10:00 Another Artist - Another Song
```

A line with `time | name` or `time Text` is a chapter. A line with
`time | artist | song` or `time Artist - Song` is a track.

### Checks before uploading

Mixcloud only reports validation errors after the whole file has
uploaded, and it silently refuses to publish shows under 15 minutes. So
`create` and `publish` check these first:

- audio length is at least 15 minutes, and size is at most 4 GiB
- free accounts can have at most 10 published shows
- description is at most 1000 characters, there are at most 5 tags and
  2 hosts, and cover art is at most 10 MiB

After a publish, the CLI confirms the show is actually public.

### Private shows

Mixcloud's API doesn't list private uploads or drafts. The CLI keeps a local
record of the shows it uploads (`.soundmix-uploads.json`, gitignored), and
`uploads list --include-private` uses it.

## Library

```go
import "github.com/mohit/soundmix-cli/mixcloud"

client, _ := mixcloud.New(mixcloud.WithAccessToken(token))
f, _ := mixcloud.OpenFile("mix.mp3")
defer f.Close()
res, err := client.Upload(ctx, &mixcloud.UploadRequest{
    Audio: f, Name: "Friday Mix", Tags: []string{"House"}, Unlisted: true,
})
for show, err := range client.Uploads(ctx, "", nil) { /* your shows */ }
```

## Development

```bash
go vet ./... && go test -race ./...
```

Design notes and verified API behaviour are in [`plans/`](plans/).

## Licence

Apache 2.0. See [LICENSE](LICENSE).
