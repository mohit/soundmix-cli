package mixcloud

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrUnknownAudioFormat is returned by MP3Duration when the data is not a
// recognisable MP3 stream.
var ErrUnknownAudioFormat = errors.New("mixcloud: not a recognisable MP3 file")

// MP3Duration works out the playing time of an MP3 without decoding it. It
// reads the Xing/Info or VBRI header when present (VBR files and most
// encoders) and otherwise estimates from the bitrate and size (CBR). size is
// the total length of r in bytes.
func MP3Duration(r io.ReadSeeker, size int64) (time.Duration, error) {
	start, err := skipID3v2(r)
	if err != nil {
		return 0, err
	}

	// Look for the first frame within the first 256 KiB after the tag.
	buf := make([]byte, 256<<10)
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, ErrUnknownAudioFormat
	}
	buf = buf[:n]

	for i := 0; i+4 <= len(buf); i++ {
		h, ok := parseFrameHeader(buf[i:])
		if !ok {
			continue
		}
		// Guard against false sync words: the next frame must also parse,
		// unless this frame runs past the end of what we read.
		if next := i + h.length; next+4 <= len(buf) {
			if _, ok := parseFrameHeader(buf[next:]); !ok {
				continue
			}
		}
		if frames := vbrFrameCount(buf[i:], h); frames > 0 {
			secs := float64(frames) * float64(h.samples) / float64(h.sampleRate)
			return time.Duration(secs * float64(time.Second)), nil
		}
		audioBytes := size - start - int64(i)
		if hasID3v1(r, size) {
			audioBytes -= 128
		}
		if audioBytes <= 0 || h.bitrate == 0 {
			return 0, ErrUnknownAudioFormat
		}
		secs := float64(audioBytes) * 8 / float64(h.bitrate)
		return time.Duration(secs * float64(time.Second)), nil
	}
	return 0, ErrUnknownAudioFormat
}

func skipID3v2(r io.ReadSeeker) (int64, error) {
	var hdr [10]byte
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, ErrUnknownAudioFormat
	}
	if !bytes.Equal(hdr[:3], []byte("ID3")) {
		return 0, nil
	}
	size := int64(hdr[6]&0x7f)<<21 | int64(hdr[7]&0x7f)<<14 | int64(hdr[8]&0x7f)<<7 | int64(hdr[9]&0x7f)
	start := 10 + size
	if hdr[5]&0x10 != 0 { // footer present
		start += 10
	}
	return start, nil
}

func hasID3v1(r io.ReadSeeker, size int64) bool {
	if size < 128 {
		return false
	}
	var tag [3]byte
	if _, err := r.Seek(size-128, io.SeekStart); err != nil {
		return false
	}
	if _, err := io.ReadFull(r, tag[:]); err != nil {
		return false
	}
	return string(tag[:]) == "TAG"
}

type frameHeader struct {
	version    int // 1, 2, or 25 (MPEG 2.5)
	layer      int // 1, 2 or 3
	bitrate    int // bits per second
	sampleRate int
	samples    int // samples per frame
	length     int // frame length in bytes
	mono       bool
}

var bitrates = map[[2]int][16]int{
	{1, 1}: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
	{1, 2}: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
	{1, 3}: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	{2, 1}: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
	{2, 2}: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	{2, 3}: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
}

var sampleRates = map[int][3]int{
	1:  {44100, 48000, 32000},
	2:  {22050, 24000, 16000},
	25: {11025, 12000, 8000},
}

func parseFrameHeader(b []byte) (frameHeader, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return frameHeader{}, false
	}
	var h frameHeader
	switch (b[1] >> 3) & 3 {
	case 0:
		h.version = 25
	case 2:
		h.version = 2
	case 3:
		h.version = 1
	default:
		return frameHeader{}, false
	}
	switch (b[1] >> 1) & 3 {
	case 1:
		h.layer = 3
	case 2:
		h.layer = 2
	case 3:
		h.layer = 1
	default:
		return frameHeader{}, false
	}
	brIdx, srIdx := int(b[2]>>4), int((b[2]>>2)&3)
	if brIdx == 0 || brIdx == 15 || srIdx == 3 {
		return frameHeader{}, false
	}
	tableVersion := h.version
	if tableVersion == 25 {
		tableVersion = 2
	}
	h.bitrate = bitrates[[2]int{tableVersion, h.layer}][brIdx] * 1000
	h.sampleRate = sampleRates[h.version][srIdx]
	padding := int((b[2] >> 1) & 1)
	h.mono = b[3]>>6 == 3

	switch {
	case h.layer == 1:
		h.samples = 384
		h.length = (12*h.bitrate/h.sampleRate + padding) * 4
	case h.layer == 2 || h.version == 1:
		h.samples = 1152
		h.length = 144*h.bitrate/h.sampleRate + padding
	default: // Layer III, MPEG 2 / 2.5
		h.samples = 576
		h.length = 72*h.bitrate/h.sampleRate + padding
	}
	if h.length < 4 {
		return frameHeader{}, false
	}
	return h, true
}

// vbrFrameCount reads the frame count from a Xing/Info or VBRI header in the
// first frame, or returns 0 if there isn't one.
func vbrFrameCount(frame []byte, h frameHeader) uint32 {
	var side int
	switch {
	case h.version == 1 && h.mono:
		side = 17
	case h.version == 1:
		side = 32
	case h.mono:
		side = 9
	default:
		side = 17
	}
	if off := 4 + side; len(frame) >= off+12 {
		tag := string(frame[off : off+4])
		if tag == "Xing" || tag == "Info" {
			flags := binary.BigEndian.Uint32(frame[off+4:])
			if flags&1 != 0 {
				return binary.BigEndian.Uint32(frame[off+8:])
			}
		}
	}
	if off := 4 + 32; len(frame) >= off+18 && string(frame[off:off+4]) == "VBRI" {
		return binary.BigEndian.Uint32(frame[off+14:])
	}
	return 0
}

// String implements fmt.Stringer for debugging.
func (h frameHeader) String() string {
	return fmt.Sprintf("MPEG%d layer %d %dkbps %dHz", h.version, h.layer, h.bitrate/1000, h.sampleRate)
}
