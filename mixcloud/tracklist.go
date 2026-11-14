package mixcloud

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ParseTracklist reads a tracklist, one section per line. Two styles are
// accepted, and can be mixed:
//
//	# Pipe-separated: time | artist | song   (a track)
//	#                 time | chapter name    (a chapter)
//	0:00 | Introduction
//	2:30 | Artist Name | Song Title
//	| Artist Without A Time | Song
//
//	# Plain: time, then "Artist - Song" for a track or any other text for a chapter
//	0:00 Introduction
//	02:30 Artist Name - Song Title
//	1:02:03 Another Artist - Another Song
//
// Times are SS, MM:SS or HH:MM:SS. Blank lines and lines starting with '#'
// are ignored. If every section has a time, the result is sorted by time.
func ParseTracklist(r io.Reader) ([]Section, error) {
	var out []Section
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		s, err := ParseSection(text)
		if err != nil {
			return nil, fmt.Errorf("tracklist line %d: %w", line, err)
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	SortSections(out)
	return out, nil
}

var plainLine = regexp.MustCompile(`^\[?(\d{1,2}(?::\d{1,2}){0,2})\]?\s+(.+)$`)

// ParseSection parses a single tracklist line (see ParseTracklist).
func ParseSection(text string) (Section, error) {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "|") {
		parts := strings.Split(text, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		var s Section
		if parts[0] != "" {
			d, err := ParseTimestamp(parts[0])
			if err != nil {
				return Section{}, err
			}
			s.StartTime = &d
		}
		switch len(parts) {
		case 2:
			s.Chapter = parts[1]
			if s.Chapter == "" {
				return Section{}, fmt.Errorf("%q: chapter name is empty", text)
			}
		case 3:
			s.Artist, s.Song = parts[1], parts[2]
			if s.Artist == "" || s.Song == "" {
				return Section{}, fmt.Errorf("%q: a track needs both an artist and a song", text)
			}
		default:
			return Section{}, fmt.Errorf("%q: expected 'time | chapter' or 'time | artist | song'", text)
		}
		return s, nil
	}

	m := plainLine.FindStringSubmatch(text)
	if m == nil {
		return Section{}, fmt.Errorf("%q: expected a start time followed by 'Artist - Song' or a chapter name", text)
	}
	d, err := ParseTimestamp(m[1])
	if err != nil {
		return Section{}, err
	}
	s := Section{StartTime: &d}
	rest := strings.TrimSpace(m[2])
	if artist, song, ok := strings.Cut(rest, " - "); ok && strings.TrimSpace(artist) != "" && strings.TrimSpace(song) != "" {
		s.Artist, s.Song = strings.TrimSpace(artist), strings.TrimSpace(song)
	} else {
		s.Chapter = rest
	}
	return s, nil
}

// ParseTimestamp parses SS, MM:SS or HH:MM:SS.
func ParseTimestamp(s string) (time.Duration, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	var total int64
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid time %q", s)
		}
		if i > 0 && n >= 60 {
			return 0, fmt.Errorf("invalid time %q: minutes and seconds must be below 60", s)
		}
		total = total*60 + n
	}
	return time.Duration(total) * time.Second, nil
}

// SortSections sorts sections by start time in place, keeping the original
// order for equal times. It does nothing unless every section has a time.
func SortSections(s []Section) {
	for _, sec := range s {
		if sec.StartTime == nil {
			return
		}
	}
	sort.SliceStable(s, func(i, j int) bool { return *s[i].StartTime < *s[j].StartTime })
}
