package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mohit/soundmix-cli/media"
	"github.com/mohit/soundmix-cli/mixcloud"
)

func newSearchCmd(g *globalOptions) *cobra.Command {
	var (
		typ               string
		limit             int
		include, excludes []string
	)
	c := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search Mixcloud for shows, users or tags",
		Long: `Search Mixcloud. --include and --exclude filter results on the client side
by case-insensitive match against the name and key (all --include terms must
match; any --exclude term removes a result).`,
		Example: `  soundmix-cli mixcloud search "nick warren" --include 1995
  soundmix-cli mixcloud search techno --type user --limit 10 --json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := mixcloud.SearchType(typ)
			switch st {
			case mixcloud.SearchCloudcasts, mixcloud.SearchUsers, mixcloud.SearchTags:
			default:
				return usagef("--type must be cloudcast, user or tag")
			}
			client, err := g.client(false)
			if err != nil {
				return err
			}
			query := strings.Join(args, " ")
			var results []mixcloud.SearchResult
			for r, err := range client.Search(cmd.Context(), query, st, nil) {
				if err != nil {
					return err
				}
				if !matches(r, include, excludes) {
					continue
				}
				results = append(results, r)
				if limit > 0 && len(results) >= limit {
					break
				}
			}
			if g.jsonOut {
				if results == nil {
					results = []mixcloud.SearchResult{}
				}
				return g.printJSON(results)
			}
			if len(results) == 0 {
				g.printf("No results.\n")
				return nil
			}
			tw := tabwriter.NewWriter(g.stdout, 0, 4, 2, ' ', 0)
			if st == mixcloud.SearchCloudcasts {
				fmt.Fprintln(tw, "KEY\tNAME\tBY\tLENGTH\tPLAYS")
				for _, r := range results {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", r.Key, truncate(r.Name, 50), r.User.Username,
						media.FormatDuration(r.Duration()), r.PlayCount)
				}
			} else {
				fmt.Fprintln(tw, "KEY\tNAME")
				for _, r := range results {
					fmt.Fprintf(tw, "%s\t%s\n", r.Key, r.Name)
				}
			}
			return tw.Flush()
		},
	}
	c.Flags().StringVar(&typ, "type", "cloudcast", "what to search for: cloudcast, user or tag")
	c.Flags().IntVarP(&limit, "limit", "l", 50, "maximum number of results (0 for no limit)")
	c.Flags().StringSliceVarP(&include, "include", "i", nil, "only keep results containing every one of these terms")
	c.Flags().StringSliceVarP(&excludes, "exclude", "e", nil, "drop results containing any of these terms")
	return c
}

func matches(r mixcloud.SearchResult, include, exclude []string) bool {
	hay := strings.ToLower(r.Name + " " + r.Key)
	norm := func(t string) []string {
		t = strings.ToLower(strings.TrimSpace(t))
		// Keys use dashes for spaces, so accept either form.
		return []string{t, strings.ReplaceAll(t, " ", "-")}
	}
	contains := func(t string) bool {
		for _, v := range norm(t) {
			if v != "" && strings.Contains(hay, v) {
				return true
			}
		}
		return false
	}
	for _, t := range exclude {
		if contains(t) {
			return false
		}
	}
	for _, t := range include {
		if !contains(t) {
			return false
		}
	}
	return true
}
