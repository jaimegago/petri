package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// The directory sync keeps the record cache warm. Every syncInterval it pulls
// the next page of changed user records and renders each into the cache under
// its id and revision, so a reader holding an older revision can still be
// served the body it validated. Superseded revisions are left for the cache's
// LRU bound to evict.
const (
	syncInterval   = 250 * time.Millisecond
	syncBatch      = 64
	recordSize     = 16 << 10
	directoryUsers = 50000
	statsInterval  = 5 * time.Second
)

// directory is the service's view of the user directory: the latest revision
// seen per user, and the cache their rendered records live in.
type directory struct {
	cache *recordCache
	log   *slog.Logger

	mu     sync.Mutex
	latest map[int]int // user id -> latest revision
	cursor int         // next user id the sync pulls
	rev    int         // revision counter of the upstream change feed
}

func newDirectory(cache *recordCache, log *slog.Logger) *directory {
	return &directory{cache: cache, log: log, latest: make(map[int]int)}
}

// Run syncs until ctx is done.
func (d *directory) Run(ctx context.Context) {
	pull := time.NewTicker(syncInterval)
	defer pull.Stop()
	stats := time.NewTicker(statsInterval)
	defer stats.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pull.C:
			d.syncPage()
		case <-stats.C:
			entries, size := d.cache.Stats()
			d.log.Info("record cache", "entries", entries, "bytes", size)
		}
	}
}

// syncPage pulls one page of changed records and caches them.
func (d *directory) syncPage() {
	d.mu.Lock()
	start, rev := d.cursor, d.rev
	d.cursor = (d.cursor + syncBatch) % directoryUsers
	if d.cursor < start {
		d.rev++
	}
	d.mu.Unlock()

	for i := 0; i < syncBatch; i++ {
		id := (start + i) % directoryUsers
		// Each page is a new revision of the users it carries: the change
		// feed only returns records that changed.
		r := rev*directoryUsers + id + 1
		d.cache.Put(recordKey(id, r), renderRecord(id, r))
		d.mu.Lock()
		d.latest[id] = r
		d.mu.Unlock()
	}
}

// Lookup returns the latest cached record for a user.
func (d *directory) Lookup(id int) ([]byte, bool) {
	d.mu.Lock()
	r, ok := d.latest[id]
	d.mu.Unlock()
	if !ok {
		return nil, false
	}
	return d.cache.Get(recordKey(id, r))
}

func recordKey(id, rev int) string {
	return fmt.Sprintf("user/%d@%d", id, rev)
}

// renderRecord renders a user record as the JSON body the service serves:
// the profile and the preferences blob clients sync, padded to the record
// size the directory delivers.
func renderRecord(id, rev int) []byte {
	head := fmt.Sprintf(`{"id":%d,"revision":%d,"name":"user-%d","preferences":"`, id, rev, id)
	tail := `"}`
	pad := recordSize - len(head) - len(tail)
	if pad < 0 {
		pad = 0
	}
	var b bytes.Buffer
	b.Grow(recordSize)
	b.WriteString(head)
	fill := []byte(fmt.Sprintf("%08x", id^rev))
	for b.Len() < len(head)+pad {
		n := len(head) + pad - b.Len()
		if n > len(fill) {
			n = len(fill)
		}
		b.Write(fill[:n])
	}
	b.WriteString(tail)
	return b.Bytes()
}
