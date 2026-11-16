package cmd

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mohit/soundmix-cli/media"
	"github.com/mohit/soundmix-cli/soundcloud"
)

func newSCTracksCmd(g *globalOptions) *cobra.Command {
	c := &cobra.Command{
		Use:     "tracks",
		Aliases: []string{"track", "shows", "show"},
		Short:   "Upload, list, edit and delete tracks and shows",
		Long: `Upload, list, edit and delete SoundCloud tracks. A show (mix, radio
show, podcast episode) is uploaded as a track; "shows" is an alias.

Tracks are referred to by URN (soundcloud:tracks:123), numeric ID or
soundcloud.com URL.`,
	}
	c.AddCommand(newSCListCmd(g), newSCGetCmd(g), newSCCreateCmd(g), newSCEditCmd(g), newSCDeleteCmd(g))
	return c
}

// ---- list / get --------------------------------------------------------

func newSCListCmd(g *globalOptions) *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "list",
		Short: "List your tracks, newest first (including private ones)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := g.scClient()
			if err != nil {
				return err
			}
			tracks := []soundcloud.Track{}
			for t, err := range client.MyTracks(cmd.Context()) {
				if err != nil {
					return err
				}
				tracks = append(tracks, t)
				if limit > 0 && len(tracks) >= limit {
					break
				}
			}
			if g.jsonOut {
				return g.printJSON(tracks)
			}
			if len(tracks) == 0 {
				g.printf("No tracks.\n")
				return nil
			}
			tw := tabwriter.NewWriter(g.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "URN\tTITLE\tSHARING\tLENGTH\tPLAYS")
			for _, t := range tracks {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", t.URN, truncate(t.Title, 50), t.Sharing,
					media.FormatDuration(t.Duration()), t.PlaybackCount)
			}
			return tw.Flush()
		},
	}
	c.Flags().IntVarP(&limit, "limit", "l", 50, "maximum number of tracks (0 for all)")
	return c
}

func newSCGetCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "get TRACK",
		Short: "Show a track's details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := g.scClient()
			if err != nil {
				return err
			}
			urn, err := client.TrackURN(ctx, args[0])
			if err != nil {
				return err
			}
			t, err := client.Track(ctx, urn)
			if err != nil {
				return err
			}
			if g.jsonOut {
				return g.printJSON(t)
			}
			printTrack(g, t)
			return nil
		},
	}
}

func printTrack(g *globalOptions, t *soundcloud.Track) {
	g.printf("%s\n", t.Title)
	g.printf("  URN:      %s\n  URL:      %s\n", t.URN, t.PermalinkURL)
	if t.Sharing == soundcloud.Private && t.SecretURI != "" {
		g.printf("  Secret:   %s\n", t.SecretURI)
	}
	g.printf("  Sharing:  %s\n  Length:   %s\n", t.Sharing, media.FormatDuration(t.Duration()))
	if t.MetadataArtist != "" {
		g.printf("  Artist:   %s\n", t.MetadataArtist)
	}
	if t.Genre != "" {
		g.printf("  Genre:    %s\n", t.Genre)
	}
	if tags := t.Tags(); len(tags) > 0 {
		g.printf("  Tags:     %s\n", strings.Join(tags, ", "))
	}
	if t.State != "" {
		g.printf("  State:    %s\n", t.State)
	}
	g.printf("  Plays:    %d   Likes: %d   Reposts: %d   Comments: %d\n",
		t.PlaybackCount, t.FavoritingsCount, t.RepostsCount, t.CommentCount)
	if t.Description != "" {
		g.printf("\n%s\n", t.Description)
	}
}

// ---- shared metadata flags --------------------------------------------

type scTrackFlags struct {
	title, artist, description, descriptionFile, genre, permalink string
	tags, sections                                                []string
	tracklist                                                     string
	private, public                                               bool
	downloadable, streamable, commentable                         bool
	embeddableBy, license, releaseDate, label, purchaseURL, isrc  string
}

func (m *scTrackFlags) register(c *cobra.Command) {
	f := c.Flags()
	f.StringVarP(&m.title, "title", "n", "", "track title")
	f.StringVar(&m.artist, "artist", "", "artist name, if different from your profile name")
	f.StringVarP(&m.description, "description", "d", "", "description")
	f.StringVar(&m.descriptionFile, "description-file", "", "read the description from a file ('-' for stdin)")
	f.StringVarP(&m.genre, "genre", "g", "", "genre")
	f.StringArrayVarP(&m.tags, "tag", "t", nil, "tag (repeatable; multi-word tags are fine)")
	f.StringVar(&m.tracklist, "tracklist", "", "tracklist file ('-' for stdin), added to the description")
	f.StringArrayVar(&m.sections, "section", nil, `tracklist entry, e.g. "2:30 | Artist | Song" (repeatable)`)
	f.BoolVar(&m.private, "private", false, "only you (and people with the secret link) can listen")
	f.BoolVar(&m.public, "public", false, "anyone can listen")
	f.StringVar(&m.permalink, "permalink", "", "URL slug (lowercase letters, digits, - and _)")
	f.BoolVar(&m.downloadable, "downloadable", false, "allow downloads")
	f.BoolVar(&m.streamable, "streamable", true, "allow streaming (--streamable=false to disable)")
	f.BoolVar(&m.commentable, "commentable", true, "allow comments (--commentable=false to disable)")
	f.StringVar(&m.embeddableBy, "embeddable-by", "", "who can embed it: all, me or none")
	f.StringVar(&m.license, "license", "", "licence: "+strings.Join(soundcloud.Licenses, ", "))
	f.StringVar(&m.releaseDate, "release-date", "", "release date, YYYY-MM-DD")
	f.StringVar(&m.label, "label", "", "record label name")
	f.StringVar(&m.purchaseURL, "purchase-url", "", "link to buy the track")
	f.StringVar(&m.isrc, "isrc", "", "ISRC code")
	c.MarkFlagsMutuallyExclusive("private", "public")
	c.MarkFlagsMutuallyExclusive("description", "description-file")
}

func (m *scTrackFlags) sharing() string {
	switch {
	case m.private:
		return soundcloud.Private
	case m.public:
		return soundcloud.Public
	}
	return ""
}

// readDescription returns the description from --description or
// --description-file, and whether one was given.
func (m *scTrackFlags) readDescription(g *globalOptions) (string, bool, error) {
	if m.descriptionFile != "" {
		data, err := readFileOrStdin(m.descriptionFile, g.stdin)
		if err != nil {
			return "", false, err
		}
		return strings.TrimRight(string(data), "\r\n"), true, nil
	}
	return m.description, m.description != "", nil
}

func boolIfChanged(c *cobra.Command, name string, v bool) *bool {
	if c.Flags().Changed(name) {
		return &v
	}
	return nil
}

func strIfChanged(c *cobra.Command, name string, v string) *string {
	if c.Flags().Changed(name) {
		return &v
	}
	return nil
}

// ---- create ------------------------------------------------------------

func newSCCreateCmd(g *globalOptions) *cobra.Command {
	var (
		m         scTrackFlags
		artwork   string
		playlist  string
		dryRun    bool
		skipQuota bool
		quiet     bool
	)
	c := &cobra.Command{
		Use:   "create AUDIO_FILE --title TITLE [flags]",
		Short: "Upload a track or show",
		Long: `Upload a track or show (a show is just a long track).

Checked before anything is sent:
  - title is required; audio is at most 4 GB and 24 hours
  - the audio fits in the account's remaining upload time (free accounts
    have a limit; see "soundcloud quota")
  - artwork is GIF, JPEG or PNG; licence, sharing, embeddable-by and
    release date are valid

The audio length is read natively for MP3 and with ffprobe for other
formats (WAV, FLAC, AIFF, OGG, …). If it can't be determined, the length
and quota checks are skipped with a warning.

--tracklist/--section add a timestamped tracklist to the description, and
--playlist adds the upload to a playlist (by URN, URL or title; a private
playlist with that title is created if none exists). Tracks are public
unless --private.`,
		Example: `  soundmix-cli soundcloud tracks create song.wav --title "New Song" -g Electronic -t synth --artwork cover.jpg
  soundmix-cli soundcloud shows create mix.mp3 --title "Friday Mix #12" --tracklist tracklist.txt \
    --playlist "Friday Mixes" --dry-run --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if strings.TrimSpace(m.title) == "" {
				return usagef("--title is required")
			}
			audio, err := media.OpenFile(args[0])
			if err != nil {
				return usageError{err}
			}
			defer audio.Close()
			desc, _, err := m.readDescription(g)
			if err != nil {
				return err
			}
			sections, err := readSections(m.tracklist, m.sections, g.stdin)
			if err != nil {
				return err
			}
			req := &soundcloud.UploadRequest{
				Audio:        audio,
				Title:        m.title,
				Artist:       m.artist,
				Description:  soundcloud.DescriptionWithTracklist(desc, sections),
				Genre:        m.genre,
				Tags:         m.tags,
				Sharing:      m.sharing(),
				Permalink:    m.permalink,
				Downloadable: boolIfChanged(cmd, "downloadable", m.downloadable),
				Streamable:   boolIfChanged(cmd, "streamable", m.streamable),
				Commentable:  boolIfChanged(cmd, "commentable", m.commentable),
				EmbeddableBy: m.embeddableBy,
				License:      m.license,
				ReleaseDate:  m.releaseDate,
				LabelName:    m.label,
				PurchaseURL:  m.purchaseURL,
				ISRC:         m.isrc,
			}
			if artwork != "" {
				art, err := media.OpenFile(artwork)
				if err != nil {
					return usageError{err}
				}
				defer art.Close()
				req.Artwork = art
			}
			if err := req.Validate(); err != nil {
				return err
			}

			plan := map[string]any{"file": args[0], "size_bytes": audio.Size, "title": req.Title,
				"sharing": firstNonEmpty(req.Sharing, soundcloud.Public), "tags": req.Tags,
				"description": req.Description}
			d, durErr := media.Duration(args[0])
			if durErr != nil {
				g.infof("Warning: can't determine the length of %s (%v); skipping the length and quota checks.\n", args[0], durErr)
				plan["duration_unknown"] = true
			} else {
				plan["duration_seconds"] = int(d.Seconds())
			}

			var client *soundcloud.Client
			if !dryRun || !skipQuota {
				if client, err = g.scClient(); err != nil {
					return err
				}
			}
			var quota *soundcloud.Quota
			if !skipQuota {
				me, err := client.Me(ctx)
				if err != nil {
					return fmt.Errorf("checking upload quota: %w", err)
				}
				quota = &me.Quota
				plan["quota"] = scQuotaJSON(me)
				if durErr != nil && !quota.Unlimited && quota.SecondsLeft <= 0 {
					return &soundcloud.ValidationError{Problems: []string{"no upload time is left on this SoundCloud account (see \"soundcloud quota\")"}}
				}
			}
			if durErr == nil {
				if err := soundcloud.CheckLength(d, quota); err != nil {
					return err
				}
			}
			if playlist != "" {
				plan["playlist"] = playlist
			}

			if dryRun {
				plan["dry_run"], plan["ok"] = true, true
				if g.jsonOut {
					return g.printJSON(plan)
				}
				g.printf("Dry run: %q passed all checks and is ready to upload.\n", req.Title)
				if durErr == nil {
					g.printf("  Length: %s\n", media.FormatDuration(d))
				}
				if quota != nil && !quota.Unlimited {
					g.printf("  Upload time left: %s\n", media.FormatDuration(quota.Left()))
				}
				return nil
			}

			if !quiet {
				req.Progress = newProgress(g.stderr, "Uploading "+filepath.Base(args[0]))
			}
			t, err := client.Upload(ctx, req)
			if req.Progress != nil {
				g.infof("\n")
			}
			if err != nil {
				return err
			}
			out := map[string]any{"track": t}
			if playlist != "" {
				pl, err := addToNamedPlaylist(ctx, client, playlist, t.URN)
				if err != nil {
					return fmt.Errorf("uploaded %s, but adding it to playlist %q failed: %w", t.URN, playlist, err)
				}
				out["playlist"] = pl
			}
			if g.jsonOut {
				return g.printJSON(out)
			}
			g.printf("Uploaded %q.\n  URN: %s\n  URL: %s\n", t.Title, t.URN, t.PermalinkURL)
			if t.Sharing == soundcloud.Private && t.SecretURI != "" {
				g.printf("  Secret link: %s\n", t.SecretURI)
			}
			if pl, ok := out["playlist"].(*soundcloud.Playlist); ok {
				g.printf("  Added to playlist %q (%s)\n", pl.Title, pl.URN)
			}
			g.printf("SoundCloud is now processing the audio; it can take a few minutes to become playable.\n")
			return nil
		},
	}
	m.register(c)
	c.Flags().StringVarP(&artwork, "artwork", "p", "", "cover art (GIF, JPEG or PNG)")
	c.Flags().StringVar(&playlist, "playlist", "", "add the upload to this playlist (URN, URL or title; created if missing)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "run all checks without uploading")
	c.Flags().BoolVar(&skipQuota, "skip-quota-check", false, "don't check the remaining upload time")
	c.Flags().BoolVarP(&quiet, "quiet", "q", false, "don't print upload progress")
	return c
}

// addToNamedPlaylist adds a track to a playlist given by URN, URL or title,
// creating a private playlist with that title if there's none.
func addToNamedPlaylist(ctx context.Context, client *soundcloud.Client, ref, trackURN string) (*soundcloud.Playlist, error) {
	urn, err := resolvePlaylist(ctx, client, ref)
	if soundcloud.IsNotFound(err) && !isPlaylistRef(ref) {
		return client.CreatePlaylist(ctx, soundcloud.PlaylistRequest{
			Title: ref, Sharing: soundcloud.Private, TrackURNs: []string{trackURN},
		})
	}
	if err != nil {
		return nil, err
	}
	return client.AddToPlaylist(ctx, urn, trackURN)
}

func isPlaylistRef(ref string) bool {
	if _, ok := soundcloud.NormalizeURN(ref, soundcloud.KindPlaylist); ok {
		return true
	}
	return strings.Contains(ref, "soundcloud.com/")
}

// resolvePlaylist accepts a URN, ID, URL or one of your playlists' titles.
func resolvePlaylist(ctx context.Context, client *soundcloud.Client, ref string) (string, error) {
	if isPlaylistRef(ref) {
		return client.PlaylistURN(ctx, ref)
	}
	pl, err := client.FindMyPlaylist(ctx, ref)
	if err != nil {
		return "", err
	}
	if pl == nil {
		return "", &soundcloud.Error{StatusCode: http.StatusNotFound, Message: fmt.Sprintf("you have no playlist titled %q", ref)}
	}
	return pl.URN, nil
}

// ---- edit --------------------------------------------------------------

func newSCEditCmd(g *globalOptions) *cobra.Command {
	var (
		m                   scTrackFlags
		addTags, removeTags []string
		dryRun              bool
	)
	c := &cobra.Command{
		Use:   "edit TRACK [flags]",
		Short: "Change a track's details",
		Long: `Change an existing track's details. Only the fields you pass change.

--tag replaces all tags; --add-tag/--remove-tag keep the others.
--tracklist/--section replace the tracklist in the description (keeping
the rest of it). SoundCloud doesn't allow replacing the audio, and changing
artwork through the API is limited to Pro accounts, so neither is offered.`,
		Example: `  soundmix-cli soundcloud tracks edit soundcloud:tracks:123 --title "New title" --add-tag techno
  soundmix-cli soundcloud shows edit https://soundcloud.com/me/friday-mix --tracklist fixed.txt --public`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := g.scClient()
			if err != nil {
				return err
			}
			urn, err := client.TrackURN(ctx, args[0])
			if err != nil {
				return err
			}
			req := &soundcloud.UpdateRequest{
				Title:        strIfChanged(cmd, "title", m.title),
				Artist:       strIfChanged(cmd, "artist", m.artist),
				Genre:        strIfChanged(cmd, "genre", m.genre),
				Permalink:    strIfChanged(cmd, "permalink", m.permalink),
				Downloadable: boolIfChanged(cmd, "downloadable", m.downloadable),
				Streamable:   boolIfChanged(cmd, "streamable", m.streamable),
				Commentable:  boolIfChanged(cmd, "commentable", m.commentable),
				EmbeddableBy: strIfChanged(cmd, "embeddable-by", m.embeddableBy),
				License:      strIfChanged(cmd, "license", m.license),
				ReleaseDate:  strIfChanged(cmd, "release-date", m.releaseDate),
				LabelName:    strIfChanged(cmd, "label", m.label),
				PurchaseURL:  strIfChanged(cmd, "purchase-url", m.purchaseURL),
				ISRC:         strIfChanged(cmd, "isrc", m.isrc),
			}
			if s := m.sharing(); s != "" {
				req.Sharing = &s
			}
			desc, descGiven, err := m.readDescription(g)
			if err != nil {
				return err
			}
			newTracklist := m.tracklist != "" || len(m.sections) > 0
			var current *soundcloud.Track
			if (newTracklist && !descGiven) || len(addTags) > 0 || len(removeTags) > 0 {
				if current, err = client.Track(ctx, urn); err != nil {
					return err
				}
			}
			if newTracklist {
				sections, err := readSections(m.tracklist, m.sections, g.stdin)
				if err != nil {
					return err
				}
				if !descGiven {
					desc = current.Description
				}
				desc, descGiven = soundcloud.DescriptionWithTracklist(desc, sections), true
			}
			if descGiven {
				req.Description = &desc
			}
			if len(m.tags) > 0 || len(addTags) > 0 || len(removeTags) > 0 {
				base := m.tags
				if len(m.tags) == 0 {
					base = current.Tags()
				}
				req.Tags = editTags(base, addTags, removeTags)
			}
			if err := req.Validate(); err != nil {
				return err
			}
			if dryRun {
				if g.jsonOut {
					return g.printJSON(map[string]any{"dry_run": true, "ok": true, "urn": urn, "tags": req.Tags, "description": req.Description})
				}
				g.printf("Dry run: edit of %s passed all checks.\n", urn)
				return nil
			}
			t, err := client.UpdateTrack(ctx, urn, req)
			if err != nil {
				return err
			}
			if g.jsonOut {
				return g.printJSON(t)
			}
			g.printf("Updated %q (%s).\n", t.Title, t.URN)
			return nil
		},
	}
	m.register(c)
	c.Flags().StringArrayVar(&addTags, "add-tag", nil, "add a tag, keeping existing ones (repeatable)")
	c.Flags().StringArrayVar(&removeTags, "remove-tag", nil, "remove a tag, keeping the others (repeatable)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "check the edit without sending it")
	c.MarkFlagsMutuallyExclusive("tag", "add-tag")
	return c
}

// ---- delete ------------------------------------------------------------

func newSCDeleteCmd(g *globalOptions) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "delete TRACK [TRACK...]",
		Short: "Permanently delete tracks (cannot be undone)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := g.scClient()
			if err != nil {
				return err
			}
			if err := confirm(g, yes, "Permanently delete "+strings.Join(args, ", ")+"?"); err != nil {
				return err
			}
			var results []map[string]any
			var firstErr error
			for _, ref := range args {
				urn, err := client.TrackURN(ctx, ref)
				if err == nil {
					err = client.DeleteTrack(ctx, urn)
				}
				r := map[string]any{"track": ref, "deleted": err == nil}
				if urn != "" {
					r["urn"] = urn
				}
				if err != nil {
					r["error"] = err.Error()
					if firstErr == nil {
						firstErr = err
					}
					if !g.jsonOut {
						g.infof("Failed to delete %s: %v\n", ref, err)
					}
				} else if !g.jsonOut {
					g.printf("Deleted %s\n", firstNonEmpty(urn, ref))
				}
				results = append(results, r)
			}
			if g.jsonOut {
				if err := g.printJSON(results); err != nil {
					return err
				}
			}
			return firstErr
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return c
}

// confirm asks before a destructive action; non-interactive runs need --yes.
func confirm(g *globalOptions, yes bool, question string) error {
	if yes {
		return nil
	}
	if !isTerminal(g.stdin) {
		return usagef("refusing to delete without confirmation: pass --yes")
	}
	g.infof("%s This cannot be undone. [y/N] ", question)
	line, _ := bufio.NewReader(g.stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		return errCancelled
	}
	return nil
}
