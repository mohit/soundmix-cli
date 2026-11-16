package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mohit/soundmix-cli/media"
	"github.com/mohit/soundmix-cli/mixcloud"
)

func newUploadsCmd(g *globalOptions) *cobra.Command {
	c := &cobra.Command{
		Use:     "uploads",
		Aliases: []string{"upload", "shows"},
		Short:   "List, upload, edit, publish and delete shows",
	}
	c.AddCommand(
		newListCmd(g), newGetCmd(g), newQuotaCmd(g), newCreateCmd(g), newEditCmd(g),
		newVisibilityCmd(g, "publish", "Make a private or draft show public", mixcloud.VisibilityPublish),
		newVisibilityCmd(g, "unpublish", "Move a show to drafts", mixcloud.VisibilityUnpublish),
		newVisibilityCmd(g, "unlist", "Make a show private (reachable only by its link)", mixcloud.VisibilityUnlisted),
		newDeleteCmd(g),
	)
	return c
}

// ---- list / get / quota ------------------------------------------------

// listedShow is a show plus where it stands (for list and get output).
type listedShow struct {
	mixcloud.Cloudcast
	showStatus
}

func newListCmd(g *globalOptions) *cobra.Command {
	var (
		user           string
		limit          int
		includePrivate bool
	)
	c := &cobra.Command{
		Use:   "list",
		Short: "List shows, newest first (yours by default)",
		Long: `List shows, newest first.

Mixcloud's API only lists public shows, and a newly published show can take
a while to appear. With --include-private, shows this CLI uploaded or changed
(recorded in .soundmix-uploads.json) are added and their current status is
checked: public, unlisted (link only), draft or scheduled.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if includePrivate && user != "" {
				return usagef("--include-private only works for your own shows")
			}
			client, err := g.client(user == "")
			if err != nil {
				return err
			}
			var shows []listedShow
			seen := map[string]bool{}
			for cc, err := range client.Uploads(ctx, user, nil) {
				if err != nil {
					return err
				}
				seen[cc.Key] = true
				shows = append(shows, listedShow{cc, showStatus{Status: stPublic, Reachable: true, Listed: ptr(true)}})
				if limit > 0 && len(shows) >= limit && !includePrivate {
					break
				}
			}
			if includePrivate {
				l, err := g.loadLedger()
				if err != nil {
					return err
				}
				var extra []listedShow
				for _, e := range l.Entries {
					if seen[e.Key] {
						continue
					}
					cc, err := client.Cloudcast(ctx, e.Key)
					if mixcloud.IsNotFound(err) {
						continue // deleted outside this CLI
					}
					if err != nil {
						return err
					}
					st, err := statusOf(ctx, client, e.Key, false, &e)
					if err != nil {
						return err
					}
					extra = append(extra, listedShow{*cc, st})
				}
				shows = append(extra, shows...)
				if limit > 0 && len(shows) > limit {
					shows = shows[:limit]
				}
			}
			if g.jsonOut {
				if shows == nil {
					shows = []listedShow{}
				}
				return g.printJSON(shows)
			}
			if len(shows) == 0 {
				g.printf("No shows.\n")
				if !includePrivate && user == "" {
					g.printf("(Private shows and drafts aren't listed by Mixcloud; try --include-private.)\n")
				}
				return nil
			}
			tw := tabwriter.NewWriter(g.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "KEY\tNAME\tSTATUS\tPUBLISHED\tLENGTH\tPLAYS")
			for _, s := range shows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", s.Key, truncate(s.Name, 50), s.Status,
					publishedDate(s.Cloudcast), media.FormatDuration(s.Duration()), s.PlayCount)
			}
			return tw.Flush()
		},
	}
	c.Flags().StringVarP(&user, "user", "u", "", "list another user's public shows instead of your own")
	c.Flags().IntVarP(&limit, "limit", "l", 20, "maximum number of shows (0 for all)")
	c.Flags().BoolVarP(&includePrivate, "include-private", "a", false, "also show private, draft and not-yet-listed shows uploaded with this CLI")
	return c
}

func publishedDate(cc mixcloud.Cloudcast) string {
	if !cc.EverPublished() {
		return "never"
	}
	return cc.CreatedTime.Format("2006-01-02")
}

func newGetCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "get KEY",
		Short: "Show details and status of a show, including its tracklist",
		Long: `Show details of a show and whether it is public, unlisted (link only),
a draft or scheduled. KEY can be username/slug, /username/slug/ or a
mixcloud.com URL. Private shows and drafts can only be fetched by their owner.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := g.client(false)
			if err != nil {
				return err
			}
			cc, err := client.Cloudcast(ctx, args[0])
			if err != nil {
				return err
			}
			var entry *ledgerEntry
			if l, err := g.loadLedger(); err == nil {
				entry = l.get(cc.Key)
			}
			st, err := statusOf(ctx, client, cc.Key, false, entry)
			if err != nil {
				return err
			}
			if g.jsonOut {
				return g.printJSON(listedShow{*cc, st})
			}
			printShow(g, cc, st)
			return nil
		},
	}
}

func printShow(g *globalOptions, cc *mixcloud.Cloudcast, st showStatus) {
	g.printf("%s\n", cc.Name)
	g.printf("  Key:       %s\n  URL:       %s\n", cc.Key, cc.URL)
	g.printf("  Status:    %s\n", describeStatus(st))
	published := "never"
	if cc.EverPublished() {
		published = cc.CreatedTime.Format(time.RFC3339)
	}
	g.printf("  Published: %s\n  Length:    %s\n", published, media.FormatDuration(cc.Duration()))
	g.printf("  Plays:     %d   Favorites: %d   Reposts: %d   Comments: %d\n",
		cc.PlayCount, cc.FavoriteCount, cc.RepostCount, cc.CommentCount)
	if len(cc.Tags) > 0 {
		g.printf("  Tags:      %s\n", strings.Join(cc.TagNames(), ", "))
	}
	if len(cc.Hosts) > 0 {
		names := make([]string, len(cc.Hosts))
		for i, h := range cc.Hosts {
			names[i] = h.Username
		}
		g.printf("  Hosts:     %s\n", strings.Join(names, ", "))
	}
	if cc.Description != "" {
		g.printf("\n%s\n", cc.Description)
	}
	if len(cc.Sections) > 0 {
		g.printf("\nTracklist:\n")
		for _, s := range cc.Sections {
			sec := s.ToSection()
			ts := "     "
			if sec.StartTime != nil {
				ts = media.FormatDuration(*sec.StartTime)
			}
			if sec.IsChapter() {
				g.printf("  %8s  %s\n", ts, sec.Chapter)
			} else {
				g.printf("  %8s  %s - %s\n", ts, sec.Artist, sec.Song)
			}
		}
	}
}

func newQuotaCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "quota",
		Short: "Show how many published shows you have and how many more you can publish",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := g.client(true)
			if err != nil {
				return err
			}
			q, err := effectiveQuota(cmd.Context(), g, client)
			if err != nil {
				return err
			}
			if g.jsonOut {
				return g.printJSON(quotaJSON(q))
			}
			g.printf("%s\n", q)
			if !q.Unlimited() {
				g.printf("%d remaining.\n", q.Remaining())
			}
			return nil
		},
	}
}

func quotaJSON(q *mixcloud.UploadQuota) map[string]any {
	return map[string]any{
		"username":  q.Username,
		"is_pro":    q.IsPro,
		"published": q.Published,
		"limit":     q.Limit,
		"remaining": q.Remaining(),
		"unlimited": q.Unlimited(),
	}
}

// ---- create ------------------------------------------------------------

type metadataFlags struct {
	name, description, descriptionFile, picture string
	tags, sections, hosts                       []string
	tracklist, publishDate                      string
	disableComments, hideStats                  bool
}

func (m *metadataFlags) register(c *cobra.Command) {
	f := c.Flags()
	f.StringVarP(&m.name, "name", "n", "", "show title")
	f.StringVarP(&m.description, "description", "d", "", "description (up to 1000 characters)")
	f.StringVar(&m.descriptionFile, "description-file", "", "read the description from a file ('-' for stdin)")
	f.StringVarP(&m.picture, "picture", "p", "", "cover art image (up to 10 MiB)")
	f.StringArrayVarP(&m.tags, "tag", "t", nil, "tag/genre (repeatable, up to 5)")
	f.StringVar(&m.tracklist, "tracklist", "", "tracklist file ('-' for stdin); see AGENTS.md for the format")
	f.StringArrayVar(&m.sections, "section", nil, `tracklist entry, e.g. "2:30 | Artist | Song" or "0:00 | Intro" (repeatable)`)
	f.StringArrayVar(&m.hosts, "host", nil, "username of a co-host to tag (repeatable, up to 2, Pro only)")
	f.StringVar(&m.publishDate, "publish-date", "", "schedule publishing (Pro only), RFC 3339 or 'YYYY-MM-DD HH:MM' local time")
	f.BoolVar(&m.disableComments, "disable-comments", false, "turn off comments (Pro only)")
	f.BoolVar(&m.hideStats, "hide-stats", false, "hide play/favorite/repost counts (Pro only)")
}

func (m *metadataFlags) readDescription(stdin io.Reader) (string, bool, error) {
	if m.descriptionFile != "" {
		if m.description != "" {
			return "", false, usagef("use either --description or --description-file, not both")
		}
		data, err := readFileOrStdin(m.descriptionFile, stdin)
		if err != nil {
			return "", false, err
		}
		return strings.TrimRight(string(data), "\r\n"), true, nil
	}
	return m.description, m.description != "", nil
}

func (m *metadataFlags) readSections(stdin io.Reader) ([]media.Section, error) {
	var out []media.Section
	if m.tracklist != "" {
		data, err := readFileOrStdin(m.tracklist, stdin)
		if err != nil {
			return nil, err
		}
		secs, err := media.ParseTracklist(strings.NewReader(string(data)))
		if err != nil {
			return nil, usageError{err}
		}
		out = append(out, secs...)
	}
	for _, line := range m.sections {
		s, err := media.ParseSection(line)
		if err != nil {
			return nil, usageError{fmt.Errorf("--section: %w", err)}
		}
		out = append(out, s)
	}
	media.SortSections(out)
	return out, nil
}

func (m *metadataFlags) parsePublishDate() (time.Time, error) {
	if m.publishDate == "" {
		return time.Time{}, nil
	}
	t, err := parsePublishDate(m.publishDate)
	if err != nil {
		return time.Time{}, usageError{err}
	}
	return t, nil
}

type checkFlags struct {
	dryRun, skipLength, skipQuota, quiet bool
}

func newCreateCmd(g *globalOptions) *cobra.Command {
	var (
		m        metadataFlags
		chk      checkFlags
		unlisted bool
	)
	c := &cobra.Command{
		Use:   "create AUDIO_FILE --name NAME [flags]",
		Short: "Upload a new show",
		Long: `Upload a new show (mix, radio show or podcast).

Before anything is sent, the upload is checked against Mixcloud's rules,
because the API only reports problems after the whole file has uploaded:
  - the audio must be at least 15 minutes long and at most 4 GiB
  - free accounts can have at most 10 published shows (checked unless
    --unlisted; see "uploads quota")
  - name is required; description <= 1000 characters; <= 5 tags;
    <= 2 hosts; cover art <= 10 MiB

Use --dry-run to run the checks without uploading.`,
		Example: `  soundmix-cli uploads create mix.mp3 --name "Friday Mix" -t House -t Disco \
    --picture cover.jpg --tracklist tracklist.txt --description "Live from the shop"

  soundmix-cli uploads create show.mp3 -n "Test" --unlisted --dry-run --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if strings.TrimSpace(m.name) == "" {
				return usagef("--name is required")
			}
			audio, err := mixcloud.OpenFile(args[0])
			if err != nil {
				return usageError{err}
			}
			defer audio.Close()

			req := &mixcloud.UploadRequest{
				Audio:           audio,
				Name:            m.name,
				Tags:            m.tags,
				Unlisted:        unlisted,
				DisableComments: m.disableComments,
				HideStats:       m.hideStats,
				Hosts:           m.hosts,
			}
			if req.Description, _, err = m.readDescription(g.stdin); err != nil {
				return err
			}
			if req.Sections, err = m.readSections(g.stdin); err != nil {
				return err
			}
			if req.PublishDate, err = m.parsePublishDate(); err != nil {
				return err
			}
			if m.picture != "" {
				pic, err := mixcloud.OpenFile(m.picture)
				if err != nil {
					return usageError{err}
				}
				defer pic.Close()
				req.Picture = pic
			}
			if err := req.Validate(time.Now()); err != nil {
				return err
			}

			plan := map[string]any{
				"file":       args[0],
				"size_bytes": audio.Size,
				"name":       req.Name,
				"unlisted":   req.Unlisted,
				"tags":       req.Tags,
				"sections":   req.Sections,
			}
			if !req.PublishDate.IsZero() {
				plan["publish_date"] = req.PublishDate.UTC().Format(time.RFC3339)
			}

			if !chk.skipLength {
				d, err := media.Duration(args[0])
				if err != nil {
					return usagef("can't determine the audio length of %s (%v); install ffprobe or pass --skip-length-check", args[0], err)
				}
				plan["duration_seconds"] = int(d.Seconds())
				if err := mixcloud.CheckShowLength(d); err != nil {
					return err
				}
			}

			client, err := g.client(!chk.dryRun || !chk.skipQuota)
			if err != nil {
				return err
			}
			if !chk.skipQuota && !req.Unlisted {
				q, err := effectiveQuota(ctx, g, client)
				if err != nil {
					return fmt.Errorf("checking upload quota: %w", err)
				}
				plan["quota"] = quotaJSON(q)
				if !q.Unlimited() && q.Remaining() == 0 {
					return &mixcloud.ValidationError{Problems: []string{fmt.Sprintf(
						"%s: upload with --unlisted, unpublish or delete a show, or upgrade to Pro (--skip-quota-check to try anyway)", q)}}
				}
			}

			if chk.dryRun {
				plan["dry_run"] = true
				plan["ok"] = true
				if g.jsonOut {
					return g.printJSON(plan)
				}
				g.printf("Dry run: %q passed all checks and is ready to upload.\n", req.Name)
				if d, ok := plan["duration_seconds"].(int); ok {
					g.printf("  Length: %s\n", media.FormatDuration(time.Duration(d)*time.Second))
				}
				if q, ok := plan["quota"].(map[string]any); ok {
					g.printf("  Quota:  %v published, %v remaining\n", q["published"], q["remaining"])
				}
				return nil
			}

			if !chk.quiet {
				req.Progress = newProgress(g.stderr, "Uploading "+filepath.Base(args[0]))
			}
			res, err := client.Upload(ctx, req)
			if req.Progress != nil {
				g.infof("\n")
			}
			if err != nil {
				return err
			}
			if res.Key != "" {
				vis := stPublic
				switch {
				case req.Unlisted:
					vis = stUnlisted
				case !req.PublishDate.IsZero():
					vis = stScheduled
				}
				g.updateLedger(func(l *ledger) {
					e := l.set(res.Key, req.Name, vis)
					e.File, _ = filepath.Abs(args[0])
					e.UploadedAt = time.Now().UTC()
				})
			}
			return printResult(g, "uploaded", res)
		},
	}
	m.register(c)
	c.Flags().BoolVar(&unlisted, "unlisted", false, "upload as private: only people with the link can listen")
	c.Flags().BoolVar(&chk.dryRun, "dry-run", false, "run all checks without uploading")
	c.Flags().BoolVar(&chk.skipLength, "skip-length-check", false, "don't check that the audio is at least 15 minutes")
	c.Flags().BoolVar(&chk.skipQuota, "skip-quota-check", false, "don't check the free-account published-show limit")
	c.Flags().BoolVarP(&chk.quiet, "quiet", "q", false, "don't print upload progress")
	c.MarkFlagsMutuallyExclusive("unlisted", "publish-date")
	return c
}

// ---- edit / publish / unpublish / unlist -------------------------------

func newEditCmd(g *globalOptions) *cobra.Command {
	var (
		m                             metadataFlags
		addTags, removeTags           []string
		clearHosts                    bool
		unlisted, publish, unpublish  bool
		skipQuota, skipLength, dryRun bool
	)
	c := &cobra.Command{
		Use:   "edit KEY [flags]",
		Short: "Change a show's details, cover art, tracklist or visibility",
		Long: `Change an existing show. Only the fields you pass are changed.

Tags, tracklist sections and hosts are replaced as a whole by Mixcloud:
--tag, --tracklist/--section and --host each replace every existing value.
Use --add-tag/--remove-tag to change tags while keeping the others.`,
		Example: `  soundmix-cli uploads edit myname/friday-mix --name "Friday Mix (Remastered)"
  soundmix-cli uploads edit myname/friday-mix --add-tag Techno --remove-tag Disco
  soundmix-cli uploads edit myname/friday-mix --tracklist tracklist.txt
  soundmix-cli uploads edit https://www.mixcloud.com/myname/friday-mix/ --publish`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			key := args[0]
			if _, err := mixcloud.ParseShowKey(key); err != nil {
				return usageError{err}
			}
			client, err := g.client(true)
			if err != nil {
				return err
			}
			req := &mixcloud.EditRequest{}
			flags := cmd.Flags()
			if flags.Changed("name") {
				req.Name = &m.name
			}
			desc, set, err := m.readDescription(g.stdin)
			if err != nil {
				return err
			}
			if set || flags.Changed("description") {
				req.Description = &desc
			}
			if m.picture != "" {
				pic, err := mixcloud.OpenFile(m.picture)
				if err != nil {
					return usageError{err}
				}
				defer pic.Close()
				req.Picture = pic
			}
			if len(m.tags) > 0 || len(addTags) > 0 || len(removeTags) > 0 {
				tags := m.tags
				if len(m.tags) == 0 {
					cur, err := client.Cloudcast(ctx, key)
					if err != nil {
						return fmt.Errorf("fetching current tags: %w", err)
					}
					tags = cur.TagNames()
				}
				req.Tags = editTags(tags, addTags, removeTags)
			}
			if m.tracklist != "" || len(m.sections) > 0 {
				if req.Sections, err = m.readSections(g.stdin); err != nil {
					return err
				}
			}
			if clearHosts {
				if len(m.hosts) > 0 {
					return usagef("use either --host or --clear-hosts, not both")
				}
				req.Hosts = []string{}
			} else if len(m.hosts) > 0 {
				req.Hosts = m.hosts
			}
			switch {
			case unlisted:
				req.Visibility = mixcloud.VisibilityUnlisted
			case publish:
				req.Visibility = mixcloud.VisibilityPublish
			case unpublish:
				req.Visibility = mixcloud.VisibilityUnpublish
			}
			if req.PublishDate, err = m.parsePublishDate(); err != nil {
				return err
			}
			if flags.Changed("disable-comments") {
				req.DisableComments = &m.disableComments
			}
			if flags.Changed("hide-stats") {
				req.HideStats = &m.hideStats
			}
			if err := req.Validate(time.Now()); err != nil {
				return err
			}
			if publish {
				if err := publishChecks(ctx, g, client, key, skipQuota, skipLength); err != nil {
					return err
				}
			}
			if dryRun {
				if g.jsonOut {
					return g.printJSON(map[string]any{"dry_run": true, "ok": true, "key": key, "tags": req.Tags, "sections": req.Sections})
				}
				g.printf("Dry run: edit of %s passed all checks.\n", key)
				return nil
			}
			res, err := client.Edit(ctx, key, req)
			if err != nil {
				return err
			}
			vis := visibilityName(req.Visibility)
			if !req.PublishDate.IsZero() && vis == "" {
				vis = stScheduled
			}
			name := ""
			if req.Name != nil {
				name = *req.Name
			}
			g.updateLedger(func(l *ledger) {
				if vis != "" || l.get(key) != nil {
					l.set(key, name, vis)
				}
			})
			if publish {
				if err := verifyPublished(ctx, client, key); err != nil {
					return err
				}
			}
			return printResult(g, "edited", res)
		},
	}
	m.register(c)
	f := c.Flags()
	f.StringArrayVar(&addTags, "add-tag", nil, "add a tag, keeping existing ones (repeatable)")
	f.StringArrayVar(&removeTags, "remove-tag", nil, "remove a tag, keeping the others (repeatable)")
	f.BoolVar(&clearHosts, "clear-hosts", false, "remove every co-host")
	f.BoolVar(&unlisted, "unlisted", false, "make the show private")
	f.BoolVar(&publish, "publish", false, "make the show public")
	f.BoolVar(&unpublish, "unpublish", false, "move the show to drafts")
	f.BoolVar(&skipQuota, "skip-quota-check", false, "with --publish, don't check the free-account published-show limit")
	f.BoolVar(&skipLength, "skip-length-check", false, "with --publish, don't check that the show is at least 15 minutes")
	f.BoolVar(&dryRun, "dry-run", false, "check the edit without sending it")
	c.MarkFlagsMutuallyExclusive("unlisted", "publish", "unpublish")
	c.MarkFlagsMutuallyExclusive("tag", "add-tag")
	return c
}

func editTags(base, add, remove []string) []string {
	drop := map[string]bool{}
	for _, t := range remove {
		drop[strings.ToLower(strings.TrimSpace(t))] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range append(append([]string{}, base...), add...) {
		k := strings.ToLower(strings.TrimSpace(t))
		if k == "" || drop[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, strings.TrimSpace(t))
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func newVisibilityCmd(g *globalOptions, name, short string, v mixcloud.Visibility) *cobra.Command {
	var skipQuota, skipLength bool
	c := &cobra.Command{
		Use:   name + " KEY",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			key := args[0]
			client, err := g.client(true)
			if err != nil {
				return err
			}
			if v == mixcloud.VisibilityPublish {
				if err := publishChecks(ctx, g, client, key, skipQuota, skipLength); err != nil {
					return err
				}
			}
			res, err := client.Edit(ctx, key, &mixcloud.EditRequest{Visibility: v})
			if err != nil {
				return err
			}
			g.updateLedger(func(l *ledger) { l.set(key, "", visibilityName(v)) })
			if v == mixcloud.VisibilityPublish {
				if err := verifyPublished(ctx, client, key); err != nil {
					return err
				}
			}
			return printResult(g, name+"ed", res)
		},
	}
	if v == mixcloud.VisibilityPublish {
		c.Long = `Make a private or draft show public.

Mixcloud answers "success" to a publish request even when it leaves the show
private, which is what happens to shows shorter than 15 minutes. So this
command checks the length and the free-account quota first, and afterwards
confirms that the show really is publicly reachable.`
		c.Flags().BoolVar(&skipQuota, "skip-quota-check", false, "don't check the free-account published-show limit")
		c.Flags().BoolVar(&skipLength, "skip-length-check", false, "don't check that the show is at least 15 minutes")
	}
	return c
}

// publishChecks verifies a show can be published: long enough, and the
// account has quota left.
func publishChecks(ctx context.Context, g *globalOptions, client *mixcloud.Client, key string, skipQuota, skipLength bool) error {
	if !skipLength {
		cc, err := client.Cloudcast(ctx, key)
		if err != nil {
			return err
		}
		if err := mixcloud.CheckShowLength(cc.Duration()); err != nil {
			return err
		}
	}
	if skipQuota {
		return nil
	}
	return checkQuota(ctx, g, client)
}

// publishVerifyDelays are the waits between reachability checks after a
// publish; a variable so tests can shorten it.
var publishVerifyDelays = []time.Duration{time.Second, 3 * time.Second, 6 * time.Second}

// verifyPublished confirms a show is publicly reachable after a publish.
func verifyPublished(ctx context.Context, client *mixcloud.Client, key string) error {
	for _, d := range publishVerifyDelays {
		if ok, err := client.PubliclyReachable(ctx, key); err == nil && ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	if ok, err := client.PubliclyReachable(ctx, key); err == nil && ok {
		return nil
	}
	return fmt.Errorf("mixcloud accepted the publish request but %s is still not public; "+
		"Mixcloud silently keeps shows under 15 minutes private, and a recent upload may still be processing "+
		"(check again with \"soundmix-cli uploads get %s\")", key, key)
}

func checkQuota(ctx context.Context, g *globalOptions, client *mixcloud.Client) error {
	q, err := effectiveQuota(ctx, g, client)
	if err != nil {
		return fmt.Errorf("checking upload quota: %w", err)
	}
	if !q.Unlimited() && q.Remaining() == 0 {
		return &mixcloud.ValidationError{Problems: []string{fmt.Sprintf(
			"%s: unpublish or delete a show first, or upgrade to Pro (--skip-quota-check to try anyway)", q)}}
	}
	return nil
}

// ---- delete ------------------------------------------------------------

func newDeleteCmd(g *globalOptions) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "delete KEY [KEY...]",
		Short: "Permanently delete shows (cannot be undone)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, k := range args {
				if _, err := mixcloud.ParseShowKey(k); err != nil {
					return usageError{err}
				}
			}
			client, err := g.client(true)
			if err != nil {
				return err
			}
			if !yes {
				if !isTerminal(g.stdin) {
					return usagef("refusing to delete without confirmation: pass --yes")
				}
				g.infof("Permanently delete %s? This cannot be undone. [y/N] ", strings.Join(args, ", "))
				line, _ := bufio.NewReader(g.stdin).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
					return errors.New("cancelled")
				}
			}
			var results []map[string]any
			var firstErr error
			for _, k := range args {
				_, err := client.Delete(cmd.Context(), k)
				r := map[string]any{"key": k, "deleted": err == nil}
				if err == nil || mixcloud.IsNotFound(err) {
					g.updateLedger(func(l *ledger) { l.remove(k) })
				}
				if err != nil {
					r["error"] = err.Error()
					if firstErr == nil {
						firstErr = err
					}
				}
				results = append(results, r)
				if !g.jsonOut {
					if err != nil {
						g.infof("Failed to delete %s: %v\n", k, err)
					} else {
						g.printf("Deleted %s\n", k)
					}
				}
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

// ---- helpers -----------------------------------------------------------

func printResult(g *globalOptions, verb string, res *mixcloud.UploadResult) error {
	if g.jsonOut {
		out := map[string]any{"success": res.Success, "message": res.Message, "key": res.Key}
		if res.Key != "" {
			out["url"] = "https://www.mixcloud.com" + res.Key
		}
		return g.printJSON(out)
	}
	msg := res.Message
	if msg == "" {
		msg = "Done"
	}
	g.printf("%s\n", msg)
	if res.Key != "" {
		g.printf("Key: %s\nURL: https://www.mixcloud.com%s\n", res.Key, res.Key)
	}
	return nil
}

func parsePublishDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid --publish-date %q: use RFC 3339 (2030-11-21T14:05:00Z) or 'YYYY-MM-DD HH:MM' local time", s)
}

func readFileOrStdin(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(stdin)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, usageError{err}
	}
	return data, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// newProgress returns an upload progress callback. On a terminal it redraws
// one line; otherwise (e.g. under an agent) it prints a line every 10%.
func newProgress(w io.Writer, label string) func(sent, total int64) {
	tty := isTerminal(w)
	start := time.Now()
	var last time.Time
	lastStep := int64(-1)
	return func(sent, total int64) {
		now := time.Now()
		elapsed := now.Sub(start).Seconds()
		rate := ""
		if elapsed > 0 {
			rate = media.HumanBytes(int64(float64(sent)/elapsed)) + "/s"
		}
		pct := 0.0
		if total > 0 {
			pct = float64(sent) * 100 / float64(total)
		}
		if tty {
			if now.Sub(last) < 200*time.Millisecond && sent != total {
				return
			}
			last = now
			fmt.Fprintf(w, "\r%s: %5.1f%% (%s / %s) %s   ", label, pct, media.HumanBytes(sent), media.HumanBytes(total), rate)
			return
		}
		if step := int64(pct) / 10; step > lastStep {
			lastStep = step
			fmt.Fprintf(w, "%s: %3.0f%% (%s / %s) %s\n", label, pct, media.HumanBytes(sent), media.HumanBytes(total), rate)
		}
	}
}

func describeStatus(st showStatus) string {
	switch st.Status {
	case stPublic:
		if st.Listed != nil && !*st.Listed {
			return "public (not yet in the profile listing)"
		}
		return "public"
	case stUnlisted:
		return "unlisted (anyone with the link can listen)"
	case stScheduled:
		return "scheduled (private until its publish date)"
	default:
		return "draft / private (not publicly reachable)"
	}
}
