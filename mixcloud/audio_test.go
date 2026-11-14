package mixcloud

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cbrFrames builds n silent MPEG-1 Layer III frames at 128 kbps / 44.1 kHz.
func cbrFrames(n int) []byte {
	hdr := []byte{0xFF, 0xFB, 0x90, 0x00} // MPEG1 L3, 128kbps, 44100Hz, no padding, stereo
	frameLen := 144 * 128000 / 44100      // 417
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		b.Write(hdr)
		b.Write(make([]byte, frameLen-4))
	}
	return b.Bytes()
}

func TestMP3DurationCBR(t *testing.T) {
	// 1 second of 44.1kHz audio is 44100/1152 ≈ 38.28 frames.
	frames := 3828 // ~100s
	data := append(id3Tag(1000), cbrFrames(frames)...)
	d, err := MP3Duration(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Duration(float64(frames*417*8) / 128000 * float64(time.Second))
	if diff := d - want; diff < -time.Second || diff > time.Second {
		t.Fatalf("duration %v, want ~%v", d, want)
	}
}

func TestMP3DurationXing(t *testing.T) {
	first := cbrFrames(1)
	off := 4 + 32
	copy(first[off:], "Xing")
	binary.BigEndian.PutUint32(first[off+4:], 1) // frames flag
	binary.BigEndian.PutUint32(first[off+8:], 45000)
	data := append(first, cbrFrames(10)...) // file is tiny; header says 45000 frames
	d, err := MP3Duration(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	frames := 45000.0
	want := time.Duration(frames * 1152 / 44100 * float64(time.Second))
	if diff := d - want; diff < -time.Millisecond || diff > time.Millisecond {
		t.Fatalf("duration %v, want %v", d, want)
	}
}

func TestMP3DurationRejectsJunk(t *testing.T) {
	data := []byte(strings.Repeat("not an mp3 ", 1000))
	if _, err := MP3Duration(bytes.NewReader(data), int64(len(data))); err != ErrUnknownAudioFormat {
		t.Fatalf("got %v", err)
	}
}

// TestMP3DurationFFmpeg checks real encoder output when ffmpeg is installed.
func TestMP3DurationFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"cbr.mp3", []string{"-b:a", "128k"}},
		{"vbr.mp3", []string{"-q:a", "4"}},
		{"mono22k.mp3", []string{"-ac", "1", "-ar", "22050", "-b:a", "32k"}},
	} {
		path := filepath.Join(dir, tc.name)
		args := append([]string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=61"}, tc.args...)
		args = append(args, "-metadata", "title=test", path)
		if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, out)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		st, _ := f.Stat()
		d, err := MP3Duration(f, st.Size())
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if diff := d - 61*time.Second; diff < -500*time.Millisecond || diff > 500*time.Millisecond {
			t.Errorf("%s: duration %v, want ~61s", tc.name, d)
		}
	}
}

func id3Tag(size int) []byte {
	h := []byte{'I', 'D', '3', 4, 0, 0,
		byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
	return append(h, make([]byte, size)...)
}

func TestCheckShowLength(t *testing.T) {
	if err := CheckShowLength(14*time.Minute + 59*time.Second); err == nil {
		t.Fatal("expected 14:59 to be rejected")
	}
	if err := CheckShowLength(15 * time.Minute); err != nil {
		t.Fatal(err)
	}
}
