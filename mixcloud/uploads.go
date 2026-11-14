package mixcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File is an audio or picture file to send with an upload or edit.
type File struct {
	// Name is the filename sent to Mixcloud; its extension sets the MIME type.
	Name string
	// Body supplies the file contents.
	Body io.Reader
	// Size is the length in bytes, or -1 if unknown. A known size lets the
	// client validate limits up front and send a Content-Length.
	Size int64
}

// OpenFile opens a file from disk. Close it when finished.
func OpenFile(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.IsDir() {
		f.Close()
		return nil, fmt.Errorf("%s is a directory", path)
	}
	return &File{Name: filepath.Base(path), Body: f, Size: st.Size()}, nil
}

// Close closes the file's Body if it is an io.Closer.
func (f *File) Close() error {
	if f == nil {
		return nil
	}
	if c, ok := f.Body.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Section is a tracklist entry for an upload or edit: either a track
// (Artist and Song) or a chapter (Chapter).
type Section struct {
	Artist  string
	Song    string
	Chapter string
	// StartTime is how far into the show the section starts. Nil omits it.
	StartTime *time.Duration
}

// IsChapter reports whether the section is a chapter rather than a track.
func (s Section) IsChapter() bool { return s.Chapter != "" }

// UploadRequest describes a new show.
type UploadRequest struct {
	Audio       *File  // required; at most 4 GiB
	Name        string // required
	Description string // up to 1000 characters
	Picture     *File  // optional cover art; at most 10 MiB
	Tags        []string
	Sections    []Section
	// Unlisted keeps the show private (reachable only by its link). It
	// overrides PublishDate.
	Unlisted bool
	// PublishDate schedules the show to go public (Pro only).
	PublishDate time.Time
	// DisableComments and HideStats are Pro only.
	DisableComments bool
	HideStats       bool
	// Hosts are usernames of up to 2 accounts to tag on the show (Pro only).
	Hosts []string

	// Progress, if set, is called as audio bytes are sent.
	Progress func(sent, total int64)
}

// Visibility changes a show's public state when editing.
type Visibility int

// Visibility values for EditRequest.
const (
	VisibilityUnchanged Visibility = iota
	VisibilityUnlisted             // make the show private
	VisibilityPublish              // make a private or draft show public
	VisibilityUnpublish            // move the show to drafts
)

// EditRequest describes changes to an existing show. Nil / zero fields are
// left unchanged.
//
// Tags, Sections and Hosts replace the existing values wholesale when
// non-nil: to add a tag, include the existing tags too. An empty non-nil
// Hosts removes every host.
type EditRequest struct {
	Name            *string
	Description     *string
	Picture         *File
	Tags            []string
	Sections        []Section
	Hosts           []string
	Visibility      Visibility
	PublishDate     time.Time
	DisableComments *bool
	HideStats       *bool
}

// UploadResult is the API's response to an upload, edit or delete.
type UploadResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	// Key of the show, e.g. "/username/slug/" (set for uploads and edits).
	Key string `json:"key"`
}

// Upload uploads a new show. The request is validated first, because the API
// only reports validation errors after the whole audio file has been sent.
func (c *Client) Upload(ctx context.Context, req *UploadRequest) (*UploadResult, error) {
	if !c.HasAccessToken() {
		return nil, errNoToken
	}
	if err := req.Validate(time.Now()); err != nil {
		return nil, err
	}
	f := form{}
	f.file("mp3", req.Audio, req.Progress)
	f.add("name", req.Name)
	if req.Description != "" {
		f.add("description", req.Description)
	}
	if req.Picture != nil {
		f.file("picture", req.Picture, nil)
	}
	f.tags(req.Tags)
	f.sections(req.Sections)
	if req.Unlisted {
		f.add("unlisted", "true")
	} else if !req.PublishDate.IsZero() {
		f.add("publish_date", req.PublishDate.UTC().Format(publishDateLayout))
	}
	if req.DisableComments {
		f.add("disable_comments", "true")
	}
	if req.HideStats {
		f.add("hide_stats", "true")
	}
	f.hosts(req.Hosts, false)
	return c.postForm(ctx, "upload/", f)
}

// Edit changes an existing show's metadata, cover art, visibility or
// tracklist. key may be in any form accepted by ParseShowKey.
func (c *Client) Edit(ctx context.Context, key string, req *EditRequest) (*UploadResult, error) {
	if !c.HasAccessToken() {
		return nil, errNoToken
	}
	k, err := ParseShowKey(key)
	if err != nil {
		return nil, err
	}
	if err := req.Validate(time.Now()); err != nil {
		return nil, err
	}
	f := form{}
	if req.Name != nil {
		f.add("name", *req.Name)
	}
	if req.Description != nil {
		f.add("description", *req.Description)
	}
	if req.Picture != nil {
		f.file("picture", req.Picture, nil)
	}
	f.tags(req.Tags)
	f.sections(req.Sections)
	if req.Hosts != nil {
		f.hosts(req.Hosts, true)
	}
	switch req.Visibility {
	case VisibilityUnlisted:
		f.add("unlisted", "true")
	case VisibilityPublish:
		f.add("publish", "true")
	case VisibilityUnpublish:
		f.add("unpublish", "true")
	}
	if !req.PublishDate.IsZero() {
		f.add("publish_date", req.PublishDate.UTC().Format(publishDateLayout))
	}
	if req.DisableComments != nil {
		f.add("disable_comments", boolStr(*req.DisableComments))
	}
	if req.HideStats != nil {
		f.add("hide_stats", boolStr(*req.HideStats))
	}
	if len(f) == 0 {
		return nil, errors.New("mixcloud: edit request contains no changes")
	}
	return c.postForm(ctx, "upload/"+k.path()+"edit/", f)
}

// Publish makes a private (unlisted) or draft show public.
func (c *Client) Publish(ctx context.Context, key string) (*UploadResult, error) {
	return c.Edit(ctx, key, &EditRequest{Visibility: VisibilityPublish})
}

// Unpublish moves a show to drafts.
func (c *Client) Unpublish(ctx context.Context, key string) (*UploadResult, error) {
	return c.Edit(ctx, key, &EditRequest{Visibility: VisibilityUnpublish})
}

// Unlist makes a show private: reachable only by people who have its link.
func (c *Client) Unlist(ctx context.Context, key string) (*UploadResult, error) {
	return c.Edit(ctx, key, &EditRequest{Visibility: VisibilityUnlisted})
}

// Delete permanently deletes one of the authorized user's shows. It cannot
// be undone. A show that doesn't exist or belongs to someone else returns an
// error for which IsNotFound is true.
func (c *Client) Delete(ctx context.Context, key string) (*UploadResult, error) {
	if !c.HasAccessToken() {
		return nil, errNoToken
	}
	k, err := ParseShowKey(key)
	if err != nil {
		return nil, err
	}
	var out resultEnvelope
	if err := c.doJSON(ctx, http.MethodDelete, c.endpoint("upload/"+k.path()+"delete/", nil), nil, "", &out); err != nil {
		return nil, err
	}
	return out.result(), nil
}

const publishDateLayout = "2006-01-02T15:04:05Z"

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

type resultEnvelope struct {
	Result *UploadResult `json:"result"`
	UploadResult
}

func (r resultEnvelope) result() *UploadResult {
	if r.Result != nil {
		return r.Result
	}
	res := r.UploadResult
	if res.Message == "" && res.Key == "" {
		res.Success = true // empty 2xx body
	}
	return &res
}

func (c *Client) postForm(ctx context.Context, path string, f form) (*UploadResult, error) {
	body, contentType, err := f.body()
	if err != nil {
		return nil, err
	}
	var out resultEnvelope
	if err := c.doJSON(ctx, http.MethodPost, c.endpoint(path, nil), body, contentType, &out); err != nil {
		return nil, err
	}
	return out.result(), nil
}

// form is an ordered multipart form.
type form []formField

type formField struct {
	name     string
	value    string
	file     *File
	progress func(sent, total int64)
}

func (f *form) add(name, value string) { *f = append(*f, formField{name: name, value: value}) }

func (f *form) file(name string, file *File, progress func(int64, int64)) {
	*f = append(*f, formField{name: name, file: file, progress: progress})
}

func (f *form) tags(tags []string) {
	for i, t := range tags {
		f.add(fmt.Sprintf("tags-%d-tag", i), t)
	}
}

func (f *form) sections(sections []Section) {
	for i, s := range sections {
		p := fmt.Sprintf("sections-%d-", i)
		if s.IsChapter() {
			f.add(p+"chapter", s.Chapter)
		} else {
			f.add(p+"artist", s.Artist)
			f.add(p+"song", s.Song)
		}
		if s.StartTime != nil {
			f.add(p+"start_time", fmt.Sprint(int64(s.StartTime.Seconds())))
		}
	}
}

func (f *form) hosts(hosts []string, clearIfEmpty bool) {
	if len(hosts) == 0 && clearIfEmpty {
		f.add("hosts-0-username", "")
		return
	}
	for i, h := range hosts {
		f.add(fmt.Sprintf("hosts-%d-username", i), h)
	}
}

// body streams the form through a pipe so multi-gigabyte files are never held
// in memory. When every file size is known, the exact Content-Length is
// computed up front so the request isn't sent chunked.
func (f form) body() (*requestBody, string, error) {
	boundary := multipart.NewWriter(io.Discard).Boundary()

	length := int64(0)
	known := true
	cw := &countingWriter{}
	if err := f.write(cw, boundary, true); err != nil {
		return nil, "", err
	}
	length = cw.n
	for _, fld := range f {
		if fld.file == nil {
			continue
		}
		if fld.file.Size < 0 {
			known = false
			break
		}
		length += fld.file.Size
	}
	if !known {
		length = 0
	}

	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(f.write(pw, boundary, false))
	}()
	return &requestBody{reader: pr, length: length}, "multipart/form-data; boundary=" + boundary, nil
}

func (f form) write(w io.Writer, boundary string, headersOnly bool) error {
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
		h.Set("Content-Type", contentTypeFor(fld.file.Name))
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
				return fmt.Errorf("mixcloud: reading %s (%d of %d bytes): %w", fld.file.Name, n, fld.file.Size, err)
			}
		} else if _, err := io.Copy(dst, fld.file.Body); err != nil {
			return fmt.Errorf("mixcloud: reading %s: %w", fld.file.Name, err)
		}
	}
	return mw.Close()
}

func contentTypeFor(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
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

// MarshalJSON lets results print cleanly.
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
