package mixcloud

import (
	"context"
	"errors"
	"iter"
	"net/url"
	"strings"
)

// Me returns the authorized user. Requires an access token.
func (c *Client) Me(ctx context.Context) (*User, error) {
	if !c.HasAccessToken() {
		return nil, errNoToken
	}
	var u User
	if err := c.get(ctx, "me/", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// User returns a user by username.
func (c *Client) User(ctx context.Context, username string) (*User, error) {
	if username == "" {
		return nil, errors.New("mixcloud: username is required")
	}
	var u User
	if err := c.get(ctx, url.PathEscape(username)+"/", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Cloudcast returns a single show, including its description and tracklist.
// key may be in any form accepted by ParseShowKey.
func (c *Client) Cloudcast(ctx context.Context, key string) (*Cloudcast, error) {
	k, err := ParseShowKey(key)
	if err != nil {
		return nil, err
	}
	var cc Cloudcast
	if err := c.get(ctx, k.path(), nil, &cc); err != nil {
		return nil, err
	}
	return &cc, nil
}

// PubliclyReachable reports whether anyone can open the show without logging
// in: true for public and unlisted (link-only) shows, false for drafts,
// never-published private uploads and deleted shows. It requests the show
// without the access token.
//
// This is the reliable way to confirm a publish: Mixcloud reports success
// for a publish request even when it leaves the show private (for example
// when the audio is shorter than MinShowLength).
func (c *Client) PubliclyReachable(ctx context.Context, key string) (bool, error) {
	k, err := ParseShowKey(key)
	if err != nil {
		return false, err
	}
	u := c.baseURL.ResolveReference(&url.URL{Path: k.path()})
	err = c.doJSON(ctx, "GET", u, nil, "", nil)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// UploadsPage returns one page of a user's shows. An empty username means
// the authorized user (via /me/), which requires an access token.
func (c *Client) UploadsPage(ctx context.Context, username string, opts *ListOptions) (*CloudcastPage, error) {
	path, err := c.uploadsPath(username)
	if err != nil {
		return nil, err
	}
	var p CloudcastPage
	if err := c.get(ctx, path, opts.values(), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// NextPage fetches the page after p, or returns (nil, nil) if p is the last.
func (c *Client) NextPage(ctx context.Context, p *CloudcastPage) (*CloudcastPage, error) {
	if p == nil || p.Paging.Next == "" || len(p.Data) == 0 {
		return nil, nil
	}
	var next CloudcastPage
	if err := c.getURL(ctx, p.Paging.Next, &next); err != nil {
		return nil, err
	}
	return &next, nil
}

// Uploads iterates over all of a user's shows, newest first, following
// paging links. An empty username means the authorized user. Iteration stops
// at the first error, which is yielded with a zero Cloudcast.
//
//	for show, err := range client.Uploads(ctx, "", nil) {
//		if err != nil { return err }
//		fmt.Println(show.Key)
//	}
func (c *Client) Uploads(ctx context.Context, username string, opts *ListOptions) iter.Seq2[Cloudcast, error] {
	path, err := c.uploadsPath(username)
	if err != nil {
		return errSeq(err)
	}
	q := opts.values()
	if q.Get("limit") == "" {
		q.Set("limit", "100")
	}
	// Page by offset rather than date: date paging can skip shows that share
	// a timestamp. The API switches to offset links when offset is present.
	if q.Get("offset") == "" {
		q.Set("offset", "0")
	}
	return c.iterate(ctx, path, q)
}

// SearchType is the kind of object to search for.
type SearchType string

// Search types supported by the API.
const (
	SearchCloudcasts SearchType = "cloudcast"
	SearchUsers      SearchType = "user"
	SearchTags       SearchType = "tag"
)

// SearchResult is a single search hit. Which fields are set depends on the
// search type; Key, URL and Name are always present.
type SearchResult struct {
	Cloudcast
	Username string `json:"username,omitempty"`
}

// Search iterates over search results for query.
func (c *Client) Search(ctx context.Context, query string, typ SearchType, opts *ListOptions) iter.Seq2[SearchResult, error] {
	if strings.TrimSpace(query) == "" {
		return func(yield func(SearchResult, error) bool) {
			yield(SearchResult{}, errors.New("mixcloud: search query is required"))
		}
	}
	if typ == "" {
		typ = SearchCloudcasts
	}
	q := opts.values()
	q.Set("q", query)
	q.Set("type", string(typ))
	if q.Get("limit") == "" {
		q.Set("limit", "100")
	}
	return func(yield func(SearchResult, error) bool) {
		var page struct {
			Data   []SearchResult `json:"data"`
			Paging Paging         `json:"paging"`
		}
		err := c.get(ctx, "search/", q, &page)
		for {
			if err != nil {
				yield(SearchResult{}, err)
				return
			}
			for _, r := range page.Data {
				if !yield(r, nil) {
					return
				}
			}
			if page.Paging.Next == "" || len(page.Data) == 0 {
				return
			}
			next := page.Paging.Next
			page.Data, page.Paging = nil, Paging{}
			err = c.getURL(ctx, next, &page)
		}
	}
}

func (c *Client) iterate(ctx context.Context, path string, q url.Values) iter.Seq2[Cloudcast, error] {
	return func(yield func(Cloudcast, error) bool) {
		var page CloudcastPage
		err := c.get(ctx, path, q, &page)
		seen := map[string]bool{}
		for {
			if err != nil {
				yield(Cloudcast{}, err)
				return
			}
			fresh := 0
			for _, cc := range page.Data {
				if seen[cc.Key] {
					continue
				}
				seen[cc.Key] = true
				fresh++
				if !yield(cc, nil) {
					return
				}
			}
			if page.Paging.Next == "" || fresh == 0 {
				return
			}
			next := page.Paging.Next
			page = CloudcastPage{}
			err = c.getURL(ctx, next, &page)
		}
	}
}

func (c *Client) uploadsPath(username string) (string, error) {
	if username == "" {
		if !c.HasAccessToken() {
			return "", errNoToken
		}
		return "me/cloudcasts/", nil
	}
	return url.PathEscape(username) + "/cloudcasts/", nil
}

func errSeq(err error) iter.Seq2[Cloudcast, error] {
	return func(yield func(Cloudcast, error) bool) { yield(Cloudcast{}, err) }
}

var errNoToken = errors.New("mixcloud: an access token is required for this request")
