package mixcloud

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits documented by Mixcloud for uploads. Sizes use binary units, which
// is how mixcloud.com displays them ("4.0GB", "10.0MB").
const (
	MaxAudioSize         int64 = 4 << 30  // 4 GiB
	MaxPictureSize       int64 = 10 << 20 // 10 MiB
	MaxDescriptionLength       = 1000     // characters
	MaxTags                    = 5
	MaxHosts                   = 2

	// MinShowLength is the shortest show Mixcloud accepts: "Upload mixes,
	// shows or podcasts over 15 minutes long."
	MinShowLength = 15 * time.Minute

	// FreePublishedShowLimit is the number of published shows a free
	// (non-Pro) account may have, as shown on mixcloud.com's upload page
	// ("0/10 free published shows used"). It isn't exposed by the API.
	FreePublishedShowLimit = 10
)

// ValidationError lists every problem found with a request.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "mixcloud: invalid request: " + strings.Join(e.Problems, "; ")
}

type problems []string

func (p *problems) addf(format string, a ...any) { *p = append(*p, fmt.Sprintf(format, a...)) }

func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return &ValidationError{Problems: p}
}

// Validate checks the request against Mixcloud's documented limits. now is
// used to check that PublishDate is in the future.
func (r *UploadRequest) Validate(now time.Time) error {
	var p problems
	if r == nil {
		return &ValidationError{Problems: []string{"request is nil"}}
	}
	if r.Audio == nil || r.Audio.Body == nil {
		p.addf("audio file is required")
	} else if r.Audio.Size > MaxAudioSize {
		p.addf("audio file is %s; the limit is %s", HumanBytes(r.Audio.Size), HumanBytes(MaxAudioSize))
	} else if r.Audio.Size == 0 {
		p.addf("audio file is empty")
	}
	if strings.TrimSpace(r.Name) == "" {
		p.addf("name is required")
	}
	validateCommon(&p, r.Description, r.Picture, r.Tags, r.Sections, r.Hosts)
	if !r.PublishDate.IsZero() {
		if r.Unlisted {
			p.addf("unlisted and publish date can't both be set (unlisted ignores the publish date)")
		}
		if !r.PublishDate.After(now) {
			p.addf("publish date %s is not in the future", r.PublishDate.UTC().Format(time.RFC3339))
		}
	}
	return p.err()
}

// Validate checks the request against Mixcloud's documented limits.
func (r *EditRequest) Validate(now time.Time) error {
	var p problems
	if r == nil {
		return &ValidationError{Problems: []string{"request is nil"}}
	}
	if r.Name != nil && strings.TrimSpace(*r.Name) == "" {
		p.addf("name can't be empty")
	}
	desc := ""
	if r.Description != nil {
		desc = *r.Description
	}
	validateCommon(&p, desc, r.Picture, r.Tags, r.Sections, r.Hosts)
	if r.Tags != nil && len(r.Tags) == 0 {
		p.addf("the API has no documented way to remove every tag; provide at least one")
	}
	if r.Sections != nil && len(r.Sections) == 0 {
		p.addf("the API has no documented way to remove the whole tracklist; provide at least one section")
	}
	if !r.PublishDate.IsZero() {
		if r.Visibility == VisibilityUnlisted || r.Visibility == VisibilityUnpublish {
			p.addf("a publish date can't be combined with unlisting or unpublishing")
		}
		if !r.PublishDate.After(now) {
			p.addf("publish date %s is not in the future", r.PublishDate.UTC().Format(time.RFC3339))
		}
	}
	return p.err()
}

func validateCommon(p *problems, description string, picture *File, tags []string, sections []Section, hosts []string) {
	if n := utf8.RuneCountInString(description); n > MaxDescriptionLength {
		p.addf("description is %d characters; the limit is %d", n, MaxDescriptionLength)
	}
	if picture != nil {
		if picture.Body == nil {
			p.addf("picture has no contents")
		} else if picture.Size > MaxPictureSize {
			p.addf("picture is %s; the limit is %s", HumanBytes(picture.Size), HumanBytes(MaxPictureSize))
		}
	}
	if len(tags) > MaxTags {
		p.addf("%d tags given; the limit is %d", len(tags), MaxTags)
	}
	for i, t := range tags {
		if strings.TrimSpace(t) == "" {
			p.addf("tag %d is empty", i+1)
		}
	}
	for i, s := range sections {
		switch {
		case s.Chapter != "" && (s.Artist != "" || s.Song != ""):
			p.addf("section %d has both a chapter and a track; use one", i+1)
		case s.Chapter == "" && (strings.TrimSpace(s.Artist) == "" || strings.TrimSpace(s.Song) == ""):
			p.addf("section %d is a track, so it needs both an artist and a song", i+1)
		}
		if s.StartTime != nil && *s.StartTime < 0 {
			p.addf("section %d has a negative start time", i+1)
		}
	}
	if len(hosts) > MaxHosts {
		p.addf("%d hosts given; the limit is %d", len(hosts), MaxHosts)
	}
}

// CheckShowLength returns an error if d is shorter than MinShowLength.
func CheckShowLength(d time.Duration) error {
	if d < MinShowLength {
		return &ValidationError{Problems: []string{fmt.Sprintf(
			"audio is %s long; Mixcloud shows must be at least %s",
			FormatDuration(d), FormatDuration(MinShowLength))}}
	}
	return nil
}

// UploadQuota summarises how many more shows the user can publish.
type UploadQuota struct {
	Username  string `json:"username"`
	IsPro     bool   `json:"is_pro"`
	Published int    `json:"published"`
	// Limit is the number of published shows allowed; 0 means unlimited (Pro).
	Limit int `json:"limit"`
}

// Unlimited reports whether the account has no published-show limit.
func (q UploadQuota) Unlimited() bool { return q.Limit == 0 }

// Remaining is how many more shows can be published (-1 if unlimited).
func (q UploadQuota) Remaining() int {
	if q.Unlimited() {
		return -1
	}
	if r := q.Limit - q.Published; r > 0 {
		return r
	}
	return 0
}

func (q UploadQuota) String() string {
	if q.Unlimited() {
		return fmt.Sprintf("%d published shows (Pro: unlimited uploads)", q.Published)
	}
	return fmt.Sprintf("%d/%d free published shows used", q.Published, q.Limit)
}

// Quota returns the authorized user's published-show quota. The count comes
// from the user's public cloudcast_count; the limit for free accounts is
// FreePublishedShowLimit.
func (c *Client) Quota(ctx context.Context) (*UploadQuota, error) {
	me, err := c.Me(ctx)
	if err != nil {
		return nil, err
	}
	q := &UploadQuota{Username: me.Username, IsPro: me.IsPro, Published: me.CloudcastCount}
	if !me.IsPro {
		q.Limit = FreePublishedShowLimit
	}
	return q, nil
}

// HumanBytes formats a byte count using binary units (e.g. "4.0 GB").
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// FormatDuration formats d as H:MM:SS or M:SS.
func FormatDuration(d time.Duration) string {
	s := int64(d.Round(time.Second).Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
