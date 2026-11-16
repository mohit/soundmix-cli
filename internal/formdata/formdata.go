// Package formdata builds streaming multipart/form-data request bodies.
package formdata

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"path/filepath"
	"strings"

	"github.com/mohit/soundmix-cli/media"
)

// Form is an ordered multipart form.
type Form []field

type field struct {
	name     string
	value    string
	file     *media.File
	progress func(sent, total int64)
}

// Add appends a text field.
func (f *Form) Add(name, value string) { *f = append(*f, field{name: name, value: value}) }

// File appends a file field. progress, if non-nil, is called as its bytes
// are written.
func (f *Form) File(name string, file *media.File, progress func(sent, total int64)) {
	*f = append(*f, field{name: name, file: file, progress: progress})
}

// Len is the number of fields.
func (f Form) Len() int { return len(f) }

// Body streams the form through a pipe, so multi-gigabyte files are never
// held in memory. When every file size is known, length is the exact
// Content-Length; otherwise it is -1 (send chunked).
func (f Form) Body() (body io.ReadCloser, length int64, contentType string, err error) {
	boundary := multipart.NewWriter(io.Discard).Boundary()

	cw := &countingWriter{}
	if err := f.write(cw, boundary, true); err != nil {
		return nil, 0, "", err
	}
	length = cw.n
	for _, fld := range f {
		if fld.file == nil {
			continue
		}
		if fld.file.Size < 0 {
			length = -1
			break
		}
		length += fld.file.Size
	}

	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(f.write(pw, boundary, false)) }()
	return pr, length, "multipart/form-data; boundary=" + boundary, nil
}

func (f Form) write(w io.Writer, boundary string, headersOnly bool) error {
	mw := multipart.NewWriter(w)
	if err := mw.SetBoundary(boundary); err != nil {
		return err
	}
	for _, fld := range f {
		if fld.file == nil {
			if err := mw.WriteField(fld.name, fld.value); err != nil {
				return err
			}
			continue
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
			escapeQuotes(fld.name), escapeQuotes(fld.file.Name)))
		h.Set("Content-Type", ContentType(fld.file.Name))
		part, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		if headersOnly {
			continue
		}
		var dst io.Writer = part
		if fld.progress != nil {
			dst = &progressWriter{w: part, total: fld.file.Size, fn: fld.progress}
		}
		if fld.file.Size >= 0 {
			n, err := io.CopyN(dst, fld.file.Body, fld.file.Size)
			if err != nil {
				return fmt.Errorf("reading %s (%d of %d bytes): %w", fld.file.Name, n, fld.file.Size, err)
			}
		} else if _, err := io.Copy(dst, fld.file.Body); err != nil {
			return fmt.Errorf("reading %s: %w", fld.file.Name, err)
		}
	}
	return mw.Close()
}

// ContentType guesses a MIME type from a filename's extension.
func ContentType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".wav":
		return "audio/wav"
	case ".aif", ".aiff":
		return "audio/aiff"
	case ".ogg":
		return "audio/ogg"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

func escapeQuotes(s string) string { return quoteEscaper.Replace(s) }

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

type progressWriter struct {
	w     io.Writer
	sent  int64
	total int64
	fn    func(sent, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.sent += int64(n)
	p.fn(p.sent, p.total)
	return n, err
}
