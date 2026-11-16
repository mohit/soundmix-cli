package mixcloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mohit/soundmix-cli/internal/formdata"
	"github.com/mohit/soundmix-cli/media"
)

// File is an audio or picture file to send with an upload or edit.
type File = media.File

// OpenFile opens a file from disk. Close it when finished.
func OpenFile(path string) (*File, error) { return media.OpenFile(path) }

// Section is a tracklist entry: a track (Artist and Song) or a chapter.
type Section = media.Section

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
	f := formdata.Form{}
	f.File("mp3", req.Audio, req.Progress)
	f.Add("name", req.Name)
	if req.Description != "" {
		f.Add("description", req.Description)
	}
	if req.Picture != nil {
		f.File("picture", req.Picture, nil)
	}
	addTags(&f, req.Tags)
	addSections(&f, req.Sections)
	if req.Unlisted {
		f.Add("unlisted", "true")
	} else if !req.PublishDate.IsZero() {
		f.Add("publish_date", req.PublishDate.UTC().Format(publishDateLayout))
	}
	if req.DisableComments {
		f.Add("disable_comments", "true")
	}
	if req.HideStats {
		f.Add("hide_stats", "true")
	}
	addHosts(&f, req.Hosts, false)
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
	f := formdata.Form{}
	if req.Name != nil {
		f.Add("name", *req.Name)
	}
	if req.Description != nil {
		f.Add("description", *req.Description)
	}
	if req.Picture != nil {
		f.File("picture", req.Picture, nil)
	}
	addTags(&f, req.Tags)
	addSections(&f, req.Sections)
	if req.Hosts != nil {
		addHosts(&f, req.Hosts, true)
	}
	switch req.Visibility {
	case VisibilityUnlisted:
		f.Add("unlisted", "true")
	case VisibilityPublish:
		f.Add("publish", "true")
	case VisibilityUnpublish:
		f.Add("unpublish", "true")
	}
	if !req.PublishDate.IsZero() {
		f.Add("publish_date", req.PublishDate.UTC().Format(publishDateLayout))
	}
	if req.DisableComments != nil {
		f.Add("disable_comments", boolStr(*req.DisableComments))
	}
	if req.HideStats != nil {
		f.Add("hide_stats", boolStr(*req.HideStats))
	}
	if f.Len() == 0 {
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

func (c *Client) postForm(ctx context.Context, path string, f formdata.Form) (*UploadResult, error) {
	r, length, contentType, err := f.Body()
	if err != nil {
		return nil, err
	}
	if length < 0 {
		length = 0
	}
	var out resultEnvelope
	if err := c.doJSON(ctx, http.MethodPost, c.endpoint(path, nil), &requestBody{reader: r, length: length}, contentType, &out); err != nil {
		return nil, err
	}
	return out.result(), nil
}

func addTags(f *formdata.Form, tags []string) {
	for i, t := range tags {
		f.Add(fmt.Sprintf("tags-%d-tag", i), t)
	}
}

func addSections(f *formdata.Form, sections []Section) {
	for i, s := range sections {
		p := fmt.Sprintf("sections-%d-", i)
		if s.IsChapter() {
			f.Add(p+"chapter", s.Chapter)
		} else {
			f.Add(p+"artist", s.Artist)
			f.Add(p+"song", s.Song)
		}
		if s.StartTime != nil {
			f.Add(p+"start_time", fmt.Sprint(int64(s.StartTime.Seconds())))
		}
	}
}

func addHosts(f *formdata.Form, hosts []string, clearIfEmpty bool) {
	if len(hosts) == 0 && clearIfEmpty {
		f.Add("hosts-0-username", "")
		return
	}
	for i, h := range hosts {
		f.Add(fmt.Sprintf("hosts-%d-username", i), h)
	}
}
