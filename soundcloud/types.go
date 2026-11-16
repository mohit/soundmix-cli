package soundcloud

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// UserSummary is the short form of a user embedded in other objects.
type UserSummary struct {
	URN          string `json:"urn"`
	Kind         string `json:"kind,omitempty"`
	Username     string `json:"username"`
	Permalink    string `json:"permalink,omitempty"`
	PermalinkURL string `json:"permalink_url,omitempty"`
	AvatarURL    string `json:"avatar_url,omitempty"`
}

// Quota is a user's upload allowance.
type Quota struct {
	Unlimited   bool `json:"unlimited_upload_quota"`
	SecondsUsed int  `json:"upload_seconds_used"`
	SecondsLeft int  `json:"upload_seconds_left"`
}

// Used is the upload time already used.
func (q Quota) Used() time.Duration { return time.Duration(q.SecondsUsed) * time.Second }

// Left is the upload time remaining (meaningless if Unlimited).
func (q Quota) Left() time.Duration { return time.Duration(q.SecondsLeft) * time.Second }

// Me is the authorized user.
type Me struct {
	UserSummary
	FullName              string `json:"full_name,omitempty"`
	Plan                  string `json:"plan,omitempty"`
	TrackCount            int    `json:"track_count"`
	PrivateTracksCount    int    `json:"private_tracks_count"`
	PlaylistCount         int    `json:"playlist_count"`
	PrivatePlaylistsCount int    `json:"private_playlists_count"`
	FollowersCount        int    `json:"followers_count"`
	Quota                 Quota  `json:"quota"`
}

// Track is a SoundCloud track. SoundCloud has no separate type for shows,
// mixes or podcast episodes: they are all tracks.
type Track struct {
	URN              string      `json:"urn"`
	Kind             string      `json:"kind,omitempty"`
	Title            string      `json:"title"`
	MetadataArtist   string      `json:"metadata_artist,omitempty"`
	Description      string      `json:"description,omitempty"`
	Genre            string      `json:"genre,omitempty"`
	TagList          string      `json:"tag_list,omitempty"`
	Sharing          string      `json:"sharing"`
	PermalinkURL     string      `json:"permalink_url"`
	SecretURI        string      `json:"secret_uri,omitempty"`
	ArtworkURL       string      `json:"artwork_url,omitempty"`
	DurationMS       int64       `json:"duration"`
	CreatedAt        string      `json:"created_at,omitempty"`
	License          string      `json:"license,omitempty"`
	EmbeddableBy     string      `json:"embeddable_by,omitempty"`
	LabelName        string      `json:"label_name,omitempty"`
	PurchaseURL      string      `json:"purchase_url,omitempty"`
	ISRC             string      `json:"isrc,omitempty"`
	ReleaseYear      int         `json:"release_year,omitempty"`
	ReleaseMonth     int         `json:"release_month,omitempty"`
	ReleaseDay       int         `json:"release_day,omitempty"`
	Downloadable     bool        `json:"downloadable"`
	Streamable       bool        `json:"streamable"`
	Commentable      bool        `json:"commentable"`
	Access           string      `json:"access,omitempty"`
	PlaybackCount    int         `json:"playback_count"`
	FavoritingsCount int         `json:"favoritings_count"`
	CommentCount     int         `json:"comment_count"`
	RepostsCount     int         `json:"reposts_count"`
	User             UserSummary `json:"user"`
	// State is the processing state ("processing", "finished", "failed")
	// when the API includes it; it isn't in the published spec.
	State string `json:"state,omitempty"`
}

// Duration is the track length.
func (t Track) Duration() time.Duration { return time.Duration(t.DurationMS) * time.Millisecond }

// Tags returns the track's tags, decoded from TagList.
func (t Track) Tags() []string { return DecodeTags(t.TagList) }

// Playlist is a SoundCloud playlist (or album).
type Playlist struct {
	URN          string      `json:"urn"`
	Kind         string      `json:"kind,omitempty"`
	Title        string      `json:"title"`
	Description  string      `json:"description,omitempty"`
	Sharing      string      `json:"sharing"`
	SetType      string      `json:"set_type,omitempty"`
	PermalinkURL string      `json:"permalink_url"`
	TrackCount   int         `json:"track_count"`
	DurationMS   int64       `json:"duration"`
	User         UserSummary `json:"user"`
	Tracks       []Track     `json:"tracks,omitempty"`
}

// Sharing values.
const (
	Public  = "public"
	Private = "private"
)

// Licenses accepted by the API.
var Licenses = []string{"no-rights-reserved", "all-rights-reserved", "cc-by", "cc-by-nc",
	"cc-by-nd", "cc-by-sa", "cc-by-nc-nd", "cc-by-nc-sa"}

// EmbeddableBy values accepted by the API.
var EmbeddableBy = []string{"all", "me", "none"}

// EncodeTags builds a tag_list: space-separated, multi-word tags in quotes.
func EncodeTags(tags []string) string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(strings.ReplaceAll(t, `"`, ""))
		if t == "" {
			continue
		}
		if strings.ContainsAny(t, " \t") {
			t = `"` + t + `"`
		}
		out = append(out, t)
	}
	return strings.Join(out, " ")
}

var tagToken = regexp.MustCompile(`"([^"]*)"|(\S+)`)

// DecodeTags splits a tag_list into tags.
func DecodeTags(list string) []string {
	var out []string
	for _, m := range tagToken.FindAllStringSubmatch(list, -1) {
		t := m[1]
		if t == "" {
			t = m[2]
		}
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Resource kinds used in URNs.
const (
	KindTrack    = "tracks"
	KindPlaylist = "playlists"
	KindUser     = "users"
)

var digits = regexp.MustCompile(`^\d+$`)

// NormalizeURN turns a numeric ID or URN into a URN of the given kind
// ("tracks", "playlists"). It returns ok=false for anything else, such as a
// soundcloud.com URL, which needs Resolve.
func NormalizeURN(ref, kind string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if digits.MatchString(ref) {
		return "soundcloud:" + kind + ":" + ref, true
	}
	if strings.HasPrefix(ref, "soundcloud:"+kind+":") {
		return ref, true
	}
	return "", false
}

func isSoundCloudURL(ref string) bool {
	return strings.HasPrefix(ref, "https://soundcloud.com/") || strings.HasPrefix(ref, "http://soundcloud.com/") ||
		strings.HasPrefix(ref, "https://m.soundcloud.com/") || strings.HasPrefix(ref, "https://on.soundcloud.com/") ||
		strings.HasPrefix(ref, "https://www.soundcloud.com/")
}

func badRef(ref, kind string) error {
	return &ValidationError{Problems: []string{fmt.Sprintf(
		"%q is not a %s reference; use a URN (soundcloud:%s:123), a numeric ID or a soundcloud.com URL",
		ref, strings.TrimSuffix(kind, "s"), kind)}}
}
