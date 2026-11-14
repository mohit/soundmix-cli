package mixcloud

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Pictures maps a size name (e.g. "large", "640wx640h") to an image URL.
type Pictures map[string]string

// Tag is a tag (usually a genre) attached to a show.
type Tag struct {
	Key  string `json:"key"`
	URL  string `json:"url"`
	Name string `json:"name"`
}

// UserSummary is the short form of a user embedded in other objects.
type UserSummary struct {
	Key      string   `json:"key"`
	URL      string   `json:"url"`
	Name     string   `json:"name"`
	Username string   `json:"username"`
	Pictures Pictures `json:"pictures,omitempty"`
}

// User is a Mixcloud account.
type User struct {
	UserSummary
	Biog           string    `json:"biog"`
	CreatedTime    time.Time `json:"created_time"`
	UpdatedTime    time.Time `json:"updated_time"`
	FollowerCount  int       `json:"follower_count"`
	FollowingCount int       `json:"following_count"`
	CloudcastCount int       `json:"cloudcast_count"`
	FavoriteCount  int       `json:"favorite_count"`
	ListenCount    int       `json:"listen_count"`
	IsPro          bool      `json:"is_pro"`
	IsPremium      bool      `json:"is_premium"`
	City           string    `json:"city"`
	Country        string    `json:"country"`
}

// Cloudcast is a show (Mixcloud's API calls shows "cloudcasts").
type Cloudcast struct {
	Key           string        `json:"key"`
	URL           string        `json:"url"`
	Name          string        `json:"name"`
	Slug          string        `json:"slug"`
	Tags          []Tag         `json:"tags"`
	CreatedTime   time.Time     `json:"created_time"`
	UpdatedTime   time.Time     `json:"updated_time"`
	PlayCount     int           `json:"play_count"`
	FavoriteCount int           `json:"favorite_count"`
	CommentCount  int           `json:"comment_count"`
	ListenerCount int           `json:"listener_count"`
	RepostCount   int           `json:"repost_count"`
	AudioLength   int           `json:"audio_length"` // seconds
	Pictures      Pictures      `json:"pictures,omitempty"`
	User          UserSummary   `json:"user"`
	Hosts         []UserSummary `json:"hosts"`
	// Description and Sections are only returned when fetching a single show.
	Description string             `json:"description,omitempty"`
	Sections    []CloudcastSection `json:"sections,omitempty"`
	// Type is the object type, e.g. "cloudcast" (only returned with a token).
	Type string `json:"type,omitempty"`
}

// EverPublished reports whether the show has been public at some point.
// Mixcloud returns a null created_time for shows that have never been
// published (private uploads and drafts); it keeps the first publish time
// after a show is unpublished or unlisted.
func (c Cloudcast) EverPublished() bool { return !c.CreatedTime.IsZero() }

// Duration returns the show's audio length.
func (c Cloudcast) Duration() time.Duration { return time.Duration(c.AudioLength) * time.Second }

// TagNames returns the names of the show's tags.
func (c Cloudcast) TagNames() []string {
	out := make([]string, len(c.Tags))
	for i, t := range c.Tags {
		out[i] = t.Name
	}
	return out
}

// CloudcastSection is a tracklist entry as returned by the API.
type CloudcastSection struct {
	StartTime   *int   `json:"start_time"`
	SectionType string `json:"section_type,omitempty"`
	Chapter     string `json:"chapter,omitempty"`
	Track       *struct {
		Name   string `json:"name"`
		Artist struct {
			Name string `json:"name"`
		} `json:"artist"`
	} `json:"track,omitempty"`
}

// ToSection converts a returned section into the form used for editing, so
// an existing tracklist can be modified and re-posted.
func (s CloudcastSection) ToSection() Section {
	out := Section{Chapter: s.Chapter}
	if s.StartTime != nil {
		d := time.Duration(*s.StartTime) * time.Second
		out.StartTime = &d
	}
	if s.Track != nil {
		out.Artist = s.Track.Artist.Name
		out.Song = s.Track.Name
	}
	return out
}

// Paging holds the links to neighbouring pages of a list.
type Paging struct {
	Next     string `json:"next"`
	Previous string `json:"previous"`
}

// CloudcastPage is one page of a list of shows.
type CloudcastPage struct {
	Data   []Cloudcast `json:"data"`
	Paging Paging      `json:"paging"`
	Name   string      `json:"name"`
}

// ListOptions controls paging of list endpoints.
type ListOptions struct {
	Limit  int       // page size; 0 uses the API default
	Offset int       // start offset
	Since  time.Time // only items created after this time
	Until  time.Time // only items created before this time
}

func (o *ListOptions) values() url.Values {
	q := url.Values{}
	if o == nil {
		return q
	}
	if o.Limit > 0 {
		q.Set("limit", fmt.Sprint(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", fmt.Sprint(o.Offset))
	}
	if !o.Since.IsZero() {
		q.Set("since", fmt.Sprint(o.Since.Unix()))
	}
	if !o.Until.IsZero() {
		q.Set("until", fmt.Sprint(o.Until.Unix()))
	}
	return q
}

// ShowKey identifies one of a user's shows.
type ShowKey struct {
	Username string
	Slug     string
}

// String returns the API key form, "/username/slug/".
func (k ShowKey) String() string { return "/" + k.Username + "/" + k.Slug + "/" }

// path returns "username/slug/" for building endpoint paths.
func (k ShowKey) path() string {
	return url.PathEscape(k.Username) + "/" + url.PathEscape(k.Slug) + "/"
}

// ParseShowKey accepts any of these forms and returns the show's key:
//
//	/username/slug/
//	username/slug
//	https://www.mixcloud.com/username/slug/
//	https://api.mixcloud.com/username/slug/
func ParseShowKey(s string) (ShowKey, error) {
	orig := s
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return ShowKey{}, fmt.Errorf("mixcloud: invalid show URL %q: %w", orig, err)
		}
		host := strings.TrimPrefix(u.Hostname(), "www.")
		if host != "mixcloud.com" && host != "api.mixcloud.com" && host != "m.mixcloud.com" {
			return ShowKey{}, fmt.Errorf("mixcloud: %q is not a mixcloud.com URL", orig)
		}
		s = u.Path
	}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '/' })
	if len(parts) != 2 {
		return ShowKey{}, fmt.Errorf("mixcloud: %q is not a show key; expected username/slug", orig)
	}
	return ShowKey{Username: parts[0], Slug: parts[1]}, nil
}
