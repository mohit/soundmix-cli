package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"mixcloud-cli/mixcloud"
)

// The ledger is a local record of uploads made by this CLI. It exists
// because the API has no way to list private (unlisted) uploads or drafts,
// and newly published shows take a while to appear in a user's listing.
// It lives next to the .env file unless MIXCLOUD_LEDGER says otherwise.

const envLedger = "MIXCLOUD_LEDGER"

// Visibility states recorded in the ledger (the last state this CLI set).
const (
	stPublic    = "public"
	stUnlisted  = "unlisted"
	stDraft     = "draft"
	stScheduled = "scheduled"
)

type ledgerEntry struct {
	Key        string    `json:"key"`
	Name       string    `json:"name"`
	File       string    `json:"file,omitempty"`
	UploadedAt time.Time `json:"uploaded_at,omitzero"`
	Visibility string    `json:"visibility"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type ledger struct {
	path    string
	Entries []ledgerEntry `json:"uploads"`
}

func (g *globalOptions) ledgerPath() string {
	if p := os.Getenv(envLedger); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(g.envFile), ".mixcloud-uploads.json")
}

func (g *globalOptions) loadLedger() (*ledger, error) {
	l := &ledger{path: g.ledgerPath()}
	data, err := os.ReadFile(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, l); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *ledger) save() error {
	sort.Slice(l.Entries, func(i, j int) bool { return l.Entries[i].UpdatedAt.After(l.Entries[j].UpdatedAt) })
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.path, append(data, '\n'), 0o600)
}

func normKey(key string) string {
	if k, err := mixcloud.ParseShowKey(key); err == nil {
		return k.String()
	}
	return key
}

func (l *ledger) get(key string) *ledgerEntry {
	key = normKey(key)
	for i := range l.Entries {
		if l.Entries[i].Key == key {
			return &l.Entries[i]
		}
	}
	return nil
}

// set records the visibility of a show, adding it if needed.
func (l *ledger) set(key, name, visibility string) *ledgerEntry {
	e := l.get(key)
	if e == nil {
		l.Entries = append(l.Entries, ledgerEntry{Key: normKey(key)})
		e = &l.Entries[len(l.Entries)-1]
	}
	if name != "" {
		e.Name = name
	}
	if visibility != "" {
		e.Visibility = visibility
	}
	e.UpdatedAt = time.Now().UTC()
	return e
}

func (l *ledger) remove(key string) {
	key = normKey(key)
	out := l.Entries[:0]
	for _, e := range l.Entries {
		if e.Key != key {
			out = append(out, e)
		}
	}
	l.Entries = out
}

// updateLedger applies fn to the ledger and saves it. Ledger problems are
// reported as warnings: they must never fail an upload that succeeded.
func (g *globalOptions) updateLedger(fn func(*ledger)) {
	l, err := g.loadLedger()
	if err == nil {
		fn(l)
		err = l.save()
	}
	if err != nil {
		g.infof("Warning: could not update the local upload record %s: %v\n", g.ledgerPath(), err)
	}
}

// showStatus describes where a show stands, combining the API (listing and
// anonymous reachability) with what the ledger knows.
type showStatus struct {
	Status    string `json:"status"` // public, unlisted, draft, scheduled, deleted
	Reachable bool   `json:"publicly_reachable"`
	// Listed is whether the show is in the owner's profile listing; nil when
	// it wasn't checked.
	Listed *bool `json:"in_profile_listing,omitempty"`
}

func statusOf(ctx context.Context, client *mixcloud.Client, key string, listed bool, entry *ledgerEntry) (showStatus, error) {
	if listed {
		return showStatus{Status: stPublic, Reachable: true, Listed: ptr(true)}, nil
	}
	reachable, err := client.PubliclyReachable(ctx, key)
	if err != nil {
		return showStatus{}, err
	}
	st := showStatus{Reachable: reachable}
	recorded := ""
	if entry != nil {
		recorded = entry.Visibility
	}
	switch {
	case !reachable && recorded == stScheduled:
		st.Status = stScheduled
	case !reachable:
		st.Status = stDraft // includes private uploads that were never published
	case recorded == stPublic:
		st.Status = stPublic // published by this CLI; may not be listed yet
	default:
		// Reachable but not known to be public: it's public if it's in the
		// owner's listing, otherwise unlisted (link only).
		st.Status = stUnlisted
		if owner := ownerOf(key); owner != "" {
			for cc, err := range client.Uploads(ctx, owner, nil) {
				if err != nil {
					return showStatus{}, err
				}
				if cc.Key == normKey(key) {
					st.Status = stPublic
					st.Listed = ptr(true)
					break
				}
			}
			if st.Listed == nil {
				st.Listed = ptr(false)
			}
		}
	}
	return st, nil
}

func ownerOf(key string) string {
	k, err := mixcloud.ParseShowKey(key)
	if err != nil {
		return ""
	}
	return k.Username
}

func ptr[T any](v T) *T { return &v }

// effectiveQuota adds to Mixcloud's cloudcast_count the shows this CLI
// published that aren't in the profile listing yet: the count and the
// listing both lag behind recent publishes. It errs towards over-counting.
func effectiveQuota(ctx context.Context, g *globalOptions, client *mixcloud.Client) (*mixcloud.UploadQuota, error) {
	q, err := client.Quota(ctx)
	if err != nil {
		return nil, err
	}
	l, err := g.loadLedger()
	if err != nil || len(l.Entries) == 0 {
		return q, nil
	}
	listed := map[string]bool{}
	for cc, err := range client.Uploads(ctx, "", nil) {
		if err != nil {
			return nil, err
		}
		listed[cc.Key] = true
	}
	for _, e := range l.Entries {
		if listed[e.Key] || e.Visibility != stPublic {
			continue
		}
		if ok, err := client.PubliclyReachable(ctx, e.Key); err == nil && ok {
			q.Published++
		}
	}
	return q, nil
}

func visibilityName(v mixcloud.Visibility) string {
	switch v {
	case mixcloud.VisibilityPublish:
		return stPublic
	case mixcloud.VisibilityUnpublish:
		return stDraft
	case mixcloud.VisibilityUnlisted:
		return stUnlisted
	}
	return ""
}
