package soundcloud

import (
	"context"
	"iter"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mohit/soundmix-cli/internal/formdata"
	"github.com/mohit/soundmix-cli/media"
)

// Limits documented by SoundCloud's Upload Requirements.
const (
	MaxAudioSize     int64 = 4 << 30 // 4 GB per file
	MaxTrackDuration       = 24 * time.Hour
)

// ArtworkExtensions are the artwork formats the API accepts.
var ArtworkExtensions = []string{".gif", ".jpg", ".jpeg", ".png"}

// UploadRequest describes a new track (or show: a long track).
type UploadRequest struct {
	Audio       *media.File // required; AIFF, WAVE, FLAC, OGG, MP2, MP3, AAC, AMR, WMA
	Title       string      // required
	Artist      string      // shown when different from the uploader's name
	Description string
	Genre       string
	Tags        []string
	// Sharing is "public" (default) or "private".
	Sharing      string
	Permalink    string
	Artwork      *media.File // gif, jpeg or png
	Downloadable *bool
	Streamable   *bool
	Commentable  *bool
	EmbeddableBy string // all, me, none
	License      string // see Licenses
	ReleaseDate  string // YYYY-MM-DD
	LabelName    string
	PurchaseURL  string
	ISRC         string

	// Progress, if set, is called as audio bytes are sent.
	Progress func(sent, total int64)
}

// Validate checks the request against the documented limits and enums.
func (r *UploadRequest) Validate() error {
	var p problems
	if r == nil {
		return &ValidationError{Problems: []string{"request is nil"}}
	}
	if r.Audio == nil || r.Audio.Body == nil {
		p.addf("audio file is required")
	} else if r.Audio.Size > MaxAudioSize {
		p.addf("audio file is %s; the limit is %s", media.HumanBytes(r.Audio.Size), media.HumanBytes(MaxAudioSize))
	} else if r.Audio.Size == 0 {
		p.addf("audio file is empty")
	}
	if strings.TrimSpace(r.Title) == "" {
		p.addf("title is required")
	}
	if r.Artwork != nil {
		if r.Artwork.Body == nil {
			p.addf("artwork has no contents")
		} else if !slices.Contains(ArtworkExtensions, strings.ToLower(filepath.Ext(r.Artwork.Name))) {
			p.addf("artwork %q must be a GIF, JPEG or PNG", r.Artwork.Name)
		}
	}
	validateFields(&p, fields{
		sharing: r.Sharing, embeddableBy: r.EmbeddableBy, license: r.License,
		releaseDate: r.ReleaseDate, tags: r.Tags, permalink: r.Permalink,
	})
	return p.err()
}

type fields struct {
	sharing, embeddableBy, license, releaseDate, permalink string
	tags                                                   []string
}

var permalinkRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

func validateFields(p *problems, f fields) {
	if f.sharing != "" && f.sharing != Public && f.sharing != Private {
		p.addf("sharing must be public or private, not %q", f.sharing)
	}
	if f.embeddableBy != "" && !slices.Contains(EmbeddableBy, f.embeddableBy) {
		p.addf("embeddable-by must be one of %s", strings.Join(EmbeddableBy, ", "))
	}
	if f.license != "" && !slices.Contains(Licenses, f.license) {
		p.addf("license must be one of %s", strings.Join(Licenses, ", "))
	}
	if f.releaseDate != "" {
		if _, err := time.Parse("2006-01-02", f.releaseDate); err != nil {
			p.addf("release date %q must be YYYY-MM-DD", f.releaseDate)
		}
	}
	if f.permalink != "" && !permalinkRe.MatchString(f.permalink) {
		p.addf("permalink %q may only contain lowercase letters, digits, - and _", f.permalink)
	}
	for i, t := range f.tags {
		if strings.TrimSpace(t) == "" {
			p.addf("tag %d is empty", i+1)
		}
		if strings.Contains(t, `"`) {
			p.addf("tag %q can't contain a double quote", t)
		}
	}
}

// CheckLength returns an error if an audio file of length d can't be
// uploaded: longer than 24 hours, or longer than the remaining quota.
func CheckLength(d time.Duration, q *Quota) error {
	var p problems
	if d > MaxTrackDuration {
		p.addf("audio is %s long; SoundCloud tracks can be at most 24 hours", media.FormatDuration(d))
	}
	if q != nil && !q.Unlimited && d > q.Left() {
		p.addf("audio is %s long but only %s of upload time is left on this account (%s used)",
			media.FormatDuration(d), media.FormatDuration(q.Left()), media.FormatDuration(q.Used()))
	}
	return p.err()
}

// Upload uploads a new track.
func (c *Client) Upload(ctx context.Context, req *UploadRequest) (*Track, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	var f formdata.Form
	f.Add("track[title]", req.Title)
	add := func(name, v string) {
		if v != "" {
			f.Add("track["+name+"]", v)
		}
	}
	addBool := func(name string, v *bool) {
		if v != nil {
			f.Add("track["+name+"]", strconv.FormatBool(*v))
		}
	}
	add("artist", req.Artist)
	add("description", req.Description)
	add("genre", req.Genre)
	add("tag_list", EncodeTags(req.Tags))
	add("sharing", req.Sharing)
	add("permalink", req.Permalink)
	add("embeddable_by", req.EmbeddableBy)
	add("license", req.License)
	add("release_date", req.ReleaseDate)
	add("label_name", req.LabelName)
	add("purchase_url", req.PurchaseURL)
	add("isrc", req.ISRC)
	addBool("downloadable", req.Downloadable)
	addBool("streamable", req.Streamable)
	addBool("commentable", req.Commentable)
	if req.Artwork != nil {
		f.File("track[artwork_data]", req.Artwork, nil)
	}
	f.File("track[asset_data]", req.Audio, req.Progress)

	r, length, contentType, err := f.Body()
	if err != nil {
		return nil, err
	}
	var t Track
	if err := c.do(ctx, http.MethodPost, c.endpoint("tracks", nil), &body{stream: r, length: length, contentType: contentType}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateRequest changes an existing track's metadata. Nil fields are left
// unchanged. Audio can't be replaced, and changing artwork is Pro-only, so
// neither is offered.
type UpdateRequest struct {
	Title        *string
	Artist       *string
	Description  *string
	Genre        *string
	Tags         []string // nil: unchanged; empty: remove all tags
	Sharing      *string
	Permalink    *string
	Downloadable *bool
	Streamable   *bool
	Commentable  *bool
	EmbeddableBy *string
	License      *string
	ReleaseDate  *string
	LabelName    *string
	PurchaseURL  *string
	ISRC         *string
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Validate checks the request's enums and formats.
func (r *UpdateRequest) Validate() error {
	var p problems
	if r.Title != nil && strings.TrimSpace(*r.Title) == "" {
		p.addf("title can't be empty")
	}
	validateFields(&p, fields{
		sharing: deref(r.Sharing), embeddableBy: deref(r.EmbeddableBy), license: deref(r.License),
		releaseDate: deref(r.ReleaseDate), tags: r.Tags, permalink: deref(r.Permalink),
	})
	return p.err()
}

func (r *UpdateRequest) payload() map[string]any {
	m := map[string]any{}
	set := func(k string, v *string) {
		if v != nil {
			m[k] = *v
		}
	}
	setB := func(k string, v *bool) {
		if v != nil {
			m[k] = *v
		}
	}
	set("title", r.Title)
	set("metadata_artist", r.Artist)
	set("description", r.Description)
	set("genre", r.Genre)
	if r.Tags != nil {
		m["tag_list"] = EncodeTags(r.Tags)
	}
	set("sharing", r.Sharing)
	set("permalink", r.Permalink)
	setB("downloadable", r.Downloadable)
	setB("streamable", r.Streamable)
	setB("commentable", r.Commentable)
	set("embeddable_by", r.EmbeddableBy)
	set("license", r.License)
	set("release_date", r.ReleaseDate)
	set("label_name", r.LabelName)
	set("purchase_url", r.PurchaseURL)
	set("isrc", r.ISRC)
	return m
}

// UpdateTrack changes a track's metadata.
func (c *Client) UpdateTrack(ctx context.Context, urn string, req *UpdateRequest) (*Track, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	m := req.payload()
	if len(m) == 0 {
		return nil, &ValidationError{Problems: []string{"update contains no changes"}}
	}
	b, err := jsonBody(map[string]any{"track": m})
	if err != nil {
		return nil, err
	}
	var t Track
	if err := c.do(ctx, http.MethodPut, c.endpoint("tracks/"+url.PathEscape(urn), nil), b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// DeleteTrack permanently deletes a track.
func (c *Client) DeleteTrack(ctx context.Context, urn string) error {
	return c.do(ctx, http.MethodDelete, c.endpoint("tracks/"+url.PathEscape(urn), nil), nil, nil)
}

// Track fetches a track by URN.
func (c *Client) Track(ctx context.Context, urn string) (*Track, error) {
	var t Track
	if err := c.do(ctx, http.MethodGet, c.endpoint("tracks/"+url.PathEscape(urn), nil), nil, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Me returns the authorized user, including their upload quota.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var m Me
	if err := c.do(ctx, http.MethodGet, c.endpoint("me", nil), nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// MyTracks iterates over the authorized user's tracks, newest first,
// including private ones.
func (c *Client) MyTracks(ctx context.Context) iter.Seq2[Track, error] {
	return paginate[Track](ctx, c, c.endpoint("me/tracks", url.Values{
		"linked_partitioning": {"true"}, "limit": {"200"},
	}))
}

// Resolve looks up a soundcloud.com URL and decodes the resource into v.
func (c *Client) Resolve(ctx context.Context, pageURL string, v any) error {
	return c.do(ctx, http.MethodGet, c.endpoint("resolve", url.Values{"url": {pageURL}}), nil, v)
}

// TrackURN turns a URN, numeric ID or soundcloud.com track URL into a URN.
func (c *Client) TrackURN(ctx context.Context, ref string) (string, error) {
	return c.urnOf(ctx, ref, KindTrack)
}

func (c *Client) urnOf(ctx context.Context, ref, kind string) (string, error) {
	if urn, ok := NormalizeURN(ref, kind); ok {
		return urn, nil
	}
	if !isSoundCloudURL(ref) {
		return "", badRef(ref, kind)
	}
	var v struct {
		URN  string `json:"urn"`
		Kind string `json:"kind"`
	}
	if err := c.Resolve(ctx, ref, &v); err != nil {
		return "", err
	}
	if !strings.HasPrefix(v.URN, "soundcloud:"+kind+":") {
		return "", badRef(ref, kind)
	}
	return v.URN, nil
}

func paginate[T any](ctx context.Context, c *Client, first *url.URL) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		var page struct {
			Collection []T    `json:"collection"`
			NextHref   string `json:"next_href"`
		}
		err := c.do(ctx, http.MethodGet, first, nil, &page)
		for {
			if err != nil {
				yield(zero, err)
				return
			}
			for _, item := range page.Collection {
				if !yield(item, nil) {
					return
				}
			}
			if page.NextHref == "" || len(page.Collection) == 0 {
				return
			}
			next := page.NextHref
			page.Collection, page.NextHref = nil, ""
			err = c.getURL(ctx, next, &page)
		}
	}
}

// DescriptionWithTracklist appends a timestamped tracklist to a
// description. SoundCloud has no chapter field, so this is how a show's
// tracklist is published. Any tracklist previously added this way is
// replaced.
func DescriptionWithTracklist(description string, sections []media.Section) string {
	description = StripTracklist(description)
	if len(sections) == 0 {
		return description
	}
	var b strings.Builder
	if description != "" {
		b.WriteString(description)
		b.WriteString("\n\n")
	}
	b.WriteString(tracklistHeading)
	for _, s := range sections {
		b.WriteString("\n")
		b.WriteString(s.String())
	}
	return b.String()
}

const tracklistHeading = "Tracklist:"

// StripTracklist removes a tracklist added by DescriptionWithTracklist.
func StripTracklist(description string) string {
	if description == tracklistHeading || strings.HasPrefix(description, tracklistHeading+"\n") {
		return ""
	}
	if i := strings.LastIndex(description, "\n\n"+tracklistHeading+"\n"); i >= 0 {
		return strings.TrimRight(description[:i], "\n")
	}
	return description
}

// DescriptionLength counts characters as SoundCloud does (runes).
func DescriptionLength(s string) int { return utf8.RuneCountInString(s) }
