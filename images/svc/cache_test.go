package main

import (
	"fmt"
	"testing"
)

func TestRecordCache_BoundEvictsLeastRecentlyUsed(t *testing.T) {
	c := newRecordCache(2)
	c.Put("a", []byte("1"))
	c.Put("b", []byte("22"))
	if _, ok := c.Get("a"); !ok { // a is now most recently used
		t.Fatal("a missing")
	}
	c.Put("c", []byte("333"))
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted as least recently used")
	}
	entries, size := c.Stats()
	if entries != 2 || size != 4 {
		t.Errorf("stats = %d entries, %d bytes; want 2, 4", entries, size)
	}
}

func TestRecordCache_ZeroBoundNeverEvicts(t *testing.T) {
	c := newRecordCache(0)
	for i := 0; i < 5000; i++ {
		c.Put(fmt.Sprintf("k%d", i), []byte("x"))
	}
	if entries, _ := c.Stats(); entries != 5000 {
		t.Errorf("entries = %d, want 5000", entries)
	}
}

func TestRecordCache_PutReplacesInPlace(t *testing.T) {
	c := newRecordCache(1)
	c.Put("a", []byte("1"))
	c.Put("a", []byte("4444"))
	body, ok := c.Get("a")
	if !ok || string(body) != "4444" {
		t.Fatalf("Get(a) = %q, %v", body, ok)
	}
	if entries, size := c.Stats(); entries != 1 || size != 4 {
		t.Errorf("stats = %d, %d; want 1, 4", entries, size)
	}
}

func TestDirectory_BoundedCacheStaysFlat(t *testing.T) {
	d := newDirectory(newRecordCache(DefaultCacheMaxEntries), discardLogger())
	for i := 0; i < 200; i++ {
		d.syncPage()
	}
	entries, size := d.cache.Stats()
	if entries != DefaultCacheMaxEntries {
		t.Errorf("entries = %d, want the bound %d", entries, DefaultCacheMaxEntries)
	}
	if size > DefaultCacheMaxEntries*recordSize {
		t.Errorf("bytes = %d, above bound*recordSize", size)
	}
}

func TestDirectory_UnboundedCacheGrowsWithEverySync(t *testing.T) {
	d := newDirectory(newRecordCache(0), discardLogger())
	var last int
	for i := 0; i < 50; i++ {
		d.syncPage()
		_, size := d.cache.Stats()
		if size <= last {
			t.Fatalf("sync %d: cache bytes %d did not grow past %d", i, size, last)
		}
		last = size
	}
	if want := 50 * syncBatch * recordSize; last != want {
		t.Errorf("bytes = %d, want %d", last, want)
	}
}

func TestDirectory_LookupServesLatestRecord(t *testing.T) {
	d := newDirectory(newRecordCache(DefaultCacheMaxEntries), discardLogger())
	d.syncPage()
	body, ok := d.Lookup(3)
	if !ok {
		t.Fatal("user 3 not served after a sync")
	}
	if len(body) != recordSize {
		t.Errorf("record size = %d, want %d", len(body), recordSize)
	}
	if _, ok := d.Lookup(directoryUsers - 1); ok {
		t.Error("a user not yet synced must not be served")
	}
}
