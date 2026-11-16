package soundcloud

import (
	"context"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// PlaylistRequest describes a new playlist.
type PlaylistRequest struct {
	Title       string
	Description string
	Sharing     string // public or private (default public)
	SetType     string // "playlist" (default) or "album"
	TrackURNs   []string
}

type trackRef struct {
	URN string `json:"urn"`
}

func trackRefs(urns []string) []trackRef {
	out := make([]trackRef, len(urns))
	for i, u := range urns {
		out[i] = trackRef{u}
	}
	return out
}

// CreatePlaylist creates a playlist.
func (c *Client) CreatePlaylist(ctx context.Context, req PlaylistRequest) (*Playlist, error) {
	var p problems
	if strings.TrimSpace(req.Title) == "" {
		p.addf("playlist title is required")
	}
	validateFields(&p, fields{sharing: req.Sharing})
	if req.SetType != "" && req.SetType != "playlist" && req.SetType != "album" {
		p.addf("set type must be playlist or album")
	}
	if err := p.err(); err != nil {
		return nil, err
	}
	m := map[string]any{"title": req.Title, "tracks": trackRefs(req.TrackURNs)}
	if req.Description != "" {
		m["description"] = req.Description
	}
	if req.Sharing != "" {
		m["sharing"] = req.Sharing
	}
	if req.SetType != "" {
		m["set_type"] = req.SetType
	}
	b, err := jsonBody(map[string]any{"playlist": m})
	if err != nil {
		return nil, err
	}
	var pl Playlist
	if err := c.do(ctx, http.MethodPost, c.endpoint("playlists", nil), b, &pl); err != nil {
		return nil, err
	}
	return &pl, nil
}

// Playlist fetches a playlist without its tracks.
func (c *Client) Playlist(ctx context.Context, urn string) (*Playlist, error) {
	var pl Playlist
	q := url.Values{"show_tracks": {"false"}}
	if err := c.do(ctx, http.MethodGet, c.endpoint("playlists/"+url.PathEscape(urn), q), nil, &pl); err != nil {
		return nil, err
	}
	return &pl, nil
}

// PlaylistTracks iterates over a playlist's tracks in order.
func (c *Client) PlaylistTracks(ctx context.Context, urn string) iter.Seq2[Track, error] {
	return paginate[Track](ctx, c, c.endpoint("playlists/"+url.PathEscape(urn)+"/tracks",
		url.Values{"linked_partitioning": {"true"}, "limit": {"200"}}))
}

// MyPlaylists iterates over the authorized user's playlists.
func (c *Client) MyPlaylists(ctx context.Context) iter.Seq2[Playlist, error] {
	return paginate[Playlist](ctx, c, c.endpoint("me/playlists", url.Values{
		"linked_partitioning": {"true"}, "show_tracks": {"false"}, "limit": {"200"},
	}))
}

// SetPlaylistTracks replaces a playlist's tracks with exactly these, in order.
func (c *Client) SetPlaylistTracks(ctx context.Context, urn string, trackURNs []string) (*Playlist, error) {
	b, err := jsonBody(map[string]any{"playlist": map[string]any{"tracks": trackRefs(trackURNs)}})
	if err != nil {
		return nil, err
	}
	var pl Playlist
	if err := c.do(ctx, http.MethodPut, c.endpoint("playlists/"+url.PathEscape(urn), nil), b, &pl); err != nil {
		return nil, err
	}
	return &pl, nil
}

// PlaylistTrackURNs lists the URNs of a playlist's tracks.
func (c *Client) PlaylistTrackURNs(ctx context.Context, urn string) ([]string, error) {
	var out []string
	for t, err := range c.PlaylistTracks(ctx, urn) {
		if err != nil {
			return nil, err
		}
		out = append(out, t.URN)
	}
	return out, nil
}

// AddToPlaylist appends tracks that aren't already in the playlist.
// The API replaces the track list as a whole, so the current list is read
// first.
func (c *Client) AddToPlaylist(ctx context.Context, playlistURN string, trackURNs ...string) (*Playlist, error) {
	cur, err := c.PlaylistTrackURNs(ctx, playlistURN)
	if err != nil {
		return nil, err
	}
	for _, u := range trackURNs {
		if !slices.Contains(cur, u) {
			cur = append(cur, u)
		}
	}
	return c.SetPlaylistTracks(ctx, playlistURN, cur)
}

// RemoveFromPlaylist removes tracks from a playlist.
func (c *Client) RemoveFromPlaylist(ctx context.Context, playlistURN string, trackURNs ...string) (*Playlist, error) {
	cur, err := c.PlaylistTrackURNs(ctx, playlistURN)
	if err != nil {
		return nil, err
	}
	keep := cur[:0]
	for _, u := range cur {
		if !slices.Contains(trackURNs, u) {
			keep = append(keep, u)
		}
	}
	return c.SetPlaylistTracks(ctx, playlistURN, keep)
}

// DeletePlaylist deletes a playlist (not its tracks).
func (c *Client) DeletePlaylist(ctx context.Context, urn string) error {
	return c.do(ctx, http.MethodDelete, c.endpoint("playlists/"+url.PathEscape(urn), nil), nil, nil)
}

// PlaylistURN turns a URN, numeric ID or soundcloud.com playlist URL into a
// URN.
func (c *Client) PlaylistURN(ctx context.Context, ref string) (string, error) {
	return c.urnOf(ctx, ref, KindPlaylist)
}

// FindMyPlaylist returns the authorized user's playlist with this title
// (case-insensitive), or nil if there is none.
func (c *Client) FindMyPlaylist(ctx context.Context, title string) (*Playlist, error) {
	for pl, err := range c.MyPlaylists(ctx) {
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(strings.TrimSpace(pl.Title), strings.TrimSpace(title)) {
			return &pl, nil
		}
	}
	return nil, nil
}
