package media

import (
	"encoding/json"
	"strings"
	"time"
)

// Section is a tracklist entry: either a track (Artist and Song) or a
// chapter (Chapter).
type Section struct {
	Artist  string
	Song    string
	Chapter string
	// StartTime is how far into the audio the section starts. Nil omits it.
	StartTime *time.Duration
}

// IsChapter reports whether the section is a chapter rather than a track.
func (s Section) IsChapter() bool { return s.Chapter != "" }

// String renders the section as one line, e.g. "2:30 Artist - Song".
func (s Section) String() string {
	var b strings.Builder
	if s.StartTime != nil {
		b.WriteString(FormatDuration(*s.StartTime))
		b.WriteString(" ")
	}
	if s.IsChapter() {
		b.WriteString(s.Chapter)
	} else {
		b.WriteString(s.Artist + " - " + s.Song)
	}
	return b.String()
}

// MarshalJSON lets sections print cleanly.
func (s Section) MarshalJSON() ([]byte, error) {
	type out struct {
		StartTime *int64 `json:"start_time,omitempty"`
		Artist    string `json:"artist,omitempty"`
		Song      string `json:"song,omitempty"`
		Chapter   string `json:"chapter,omitempty"`
	}
	o := out{Artist: s.Artist, Song: s.Song, Chapter: s.Chapter}
	if s.StartTime != nil {
		sec := int64(s.StartTime.Seconds())
		o.StartTime = &sec
	}
	return json.Marshal(o)
}
