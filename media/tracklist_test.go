package media

import (
	"strings"
	"testing"
	"time"
)

func TestParseTracklist(t *testing.T) {
	in := `# My tracklist
0:00 | Introduction
02:30 | Artist Name | Song Title

1:02:03 Another Artist - Another - Song
[05:00] Bonus Chapter
| Untimed Artist | Untimed Song
`
	got, err := ParseTracklist(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d sections", len(got))
	}
	// Not sorted because one line has no time: order is preserved.
	check := func(i int, chapter, artist, song string, at time.Duration, timed bool) {
		t.Helper()
		s := got[i]
		if s.Chapter != chapter || s.Artist != artist || s.Song != song {
			t.Errorf("section %d = %+v", i, s)
		}
		if timed != (s.StartTime != nil) || (timed && *s.StartTime != at) {
			t.Errorf("section %d start = %v", i, s.StartTime)
		}
	}
	check(0, "Introduction", "", "", 0, true)
	check(1, "", "Artist Name", "Song Title", 150*time.Second, true)
	check(2, "", "Another Artist", "Another - Song", time.Hour+2*time.Minute+3*time.Second, true)
	check(3, "Bonus Chapter", "", "", 5*time.Minute, true)
	check(4, "", "Untimed Artist", "Untimed Song", 0, false)
}

func TestParseTracklistSorts(t *testing.T) {
	got, err := ParseTracklist(strings.NewReader("10:00 B - b\n0:00 A - a\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Artist != "A" {
		t.Fatalf("not sorted: %+v", got)
	}
}

func TestParseTracklistErrors(t *testing.T) {
	for _, bad := range []string{
		"no time here",
		"1:2:3:4 | x",
		"0:61 | x",
		"0:00 | artist | ",
		"0:00 | a | b | c",
		"0:00 |  ",
	} {
		if _, err := ParseTracklist(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
