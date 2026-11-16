package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mohit/soundmix-cli/media"
	"github.com/mohit/soundmix-cli/soundcloud"
)

func newSCPlaylistsCmd(g *globalOptions) *cobra.Command {
	c := &cobra.Command{
		Use:     "playlists",
		Aliases: []string{"playlist", "sets"},
		Short:   "Create playlists and add or remove tracks (e.g. a show's episodes)",
		Long: `Manage your SoundCloud playlists. A playlist can be referred to by URN
(soundcloud:playlists:123), numeric ID, soundcloud.com URL, or by the title
of one of your playlists.`,
	}
	c.AddCommand(newSCPlaylistListCmd(g), newSCPlaylistGetCmd(g), newSCPlaylistCreateCmd(g),
		newSCPlaylistAddCmd(g, true), newSCPlaylistAddCmd(g, false), newSCPlaylistDeleteCmd(g))
	return c
}

func newSCPlaylistListCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List your playlists",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := g.scClient()
			if err != nil {
				return err
			}
			pls := []soundcloud.Playlist{}
			for pl, err := range client.MyPlaylists(cmd.Context()) {
				if err != nil {
					return err
				}
				pls = append(pls, pl)
			}
			if g.jsonOut {
				return g.printJSON(pls)
			}
			if len(pls) == 0 {
				g.printf("No playlists.\n")
				return nil
			}
			tw := tabwriter.NewWriter(g.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "URN\tTITLE\tSHARING\tTRACKS")
			for _, p := range pls {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", p.URN, truncate(p.Title, 50), p.Sharing, p.TrackCount)
			}
			return tw.Flush()
		},
	}
}

func newSCPlaylistGetCmd(g *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "get PLAYLIST",
		Short: "Show a playlist and its tracks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := g.scClient()
			if err != nil {
				return err
			}
			urn, err := resolvePlaylist(ctx, client, args[0])
			if err != nil {
				return err
			}
			pl, err := client.Playlist(ctx, urn)
			if err != nil {
				return err
			}
			pl.Tracks = []soundcloud.Track{}
			for t, err := range client.PlaylistTracks(ctx, urn) {
				if err != nil {
					return err
				}
				pl.Tracks = append(pl.Tracks, t)
			}
			if g.jsonOut {
				return g.printJSON(pl)
			}
			g.printf("%s\n  URN:     %s\n  URL:     %s\n  Sharing: %s\n", pl.Title, pl.URN, pl.PermalinkURL, pl.Sharing)
			for i, t := range pl.Tracks {
				g.printf("  %2d. %s  %s  (%s)\n", i+1, t.Title, media.FormatDuration(t.Duration()), t.URN)
			}
			return nil
		},
	}
}

func newSCPlaylistCreateCmd(g *globalOptions) *cobra.Command {
	var (
		description string
		private     bool
		album       bool
		tracks      []string
	)
	c := &cobra.Command{
		Use:   "create TITLE [--track TRACK...]",
		Short: "Create a playlist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := g.scClient()
			if err != nil {
				return err
			}
			req := soundcloud.PlaylistRequest{Title: args[0], Description: description, Sharing: soundcloud.Public}
			if private {
				req.Sharing = soundcloud.Private
			}
			if album {
				req.SetType = "album"
			}
			for _, ref := range tracks {
				urn, err := client.TrackURN(ctx, ref)
				if err != nil {
					return err
				}
				req.TrackURNs = append(req.TrackURNs, urn)
			}
			pl, err := client.CreatePlaylist(ctx, req)
			if err != nil {
				return err
			}
			if g.jsonOut {
				return g.printJSON(pl)
			}
			g.printf("Created playlist %q (%s).\n", pl.Title, pl.URN)
			return nil
		},
	}
	c.Flags().StringVarP(&description, "description", "d", "", "description")
	c.Flags().BoolVar(&private, "private", false, "make the playlist private")
	c.Flags().BoolVar(&album, "album", false, "make it an album rather than a playlist")
	c.Flags().StringArrayVar(&tracks, "track", nil, "track to include (repeatable)")
	return c
}

func newSCPlaylistAddCmd(g *globalOptions, add bool) *cobra.Command {
	name, short := "add", "Add tracks to the end of a playlist (skipping ones already in it)"
	if !add {
		name, short = "remove", "Remove tracks from a playlist (the tracks themselves are kept)"
	}
	return &cobra.Command{
		Use:   name + " PLAYLIST TRACK [TRACK...]",
		Short: short,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := g.scClient()
			if err != nil {
				return err
			}
			urn, err := resolvePlaylist(ctx, client, args[0])
			if err != nil {
				return err
			}
			var trackURNs []string
			for _, ref := range args[1:] {
				t, err := client.TrackURN(ctx, ref)
				if err != nil {
					return err
				}
				trackURNs = append(trackURNs, t)
			}
			var pl *soundcloud.Playlist
			if add {
				pl, err = client.AddToPlaylist(ctx, urn, trackURNs...)
			} else {
				pl, err = client.RemoveFromPlaylist(ctx, urn, trackURNs...)
			}
			if err != nil {
				return err
			}
			if g.jsonOut {
				return g.printJSON(pl)
			}
			verb := map[bool]string{true: "Added", false: "Removed"}[add]
			g.printf("%s %s %s %q.\n", verb, strings.Join(trackURNs, ", "), map[bool]string{true: "to", false: "from"}[add],
				firstNonEmpty(pl.Title, urn))
			return nil
		},
	}
}

func newSCPlaylistDeleteCmd(g *globalOptions) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "delete PLAYLIST",
		Short: "Delete a playlist (its tracks are kept)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := g.scClient()
			if err != nil {
				return err
			}
			urn, err := resolvePlaylist(ctx, client, args[0])
			if err != nil {
				return err
			}
			if err := confirm(g, yes, "Delete playlist "+args[0]+"?"); err != nil {
				return err
			}
			if err := client.DeletePlaylist(ctx, urn); err != nil {
				return err
			}
			if g.jsonOut {
				return g.printJSON(map[string]any{"deleted": true, "urn": urn})
			}
			g.printf("Deleted playlist %s.\n", urn)
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return c
}
