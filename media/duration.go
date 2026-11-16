package media

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Duration returns the playing time of an audio file. MP3s are read
// natively; other formats (WAV, FLAC, AIFF, M4A, …) need ffprobe on PATH.
func Duration(path string) (time.Duration, error) {
	if strings.EqualFold(filepath.Ext(path), ".mp3") {
		f, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return 0, err
		}
		if d, err := MP3Duration(f, st.Size()); err == nil {
			return d, nil
		}
	}
	return ffprobeDuration(path)
}

// ErrNoFFprobe is returned by Duration for non-MP3 files when ffprobe isn't
// installed.
var ErrNoFFprobe = errors.New("not an MP3 and ffprobe is not installed")

func ffprobeDuration(path string) (time.Duration, error) {
	bin, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0, ErrNoFFprobe
	}
	out, err := exec.Command(bin, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", "--", path).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe returned %q", strings.TrimSpace(string(out)))
	}
	return time.Duration(secs * float64(time.Second)), nil
}
