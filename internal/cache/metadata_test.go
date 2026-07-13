package cache

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
)

// putMeta stores a body the way the proxy does: streaming through a MultiWriter
// so the client copy and the cache copy are written together. It returns the
// bytes the "client" would have seen.
func putMeta(t *testing.T, c *Cache, url, etag, lm, ct string, body []byte) []byte {
	t.Helper()
	mw, err := c.NewMetadataWriter(url, etag, lm, ct)
	if err != nil {
		t.Fatalf("NewMetadataWriter: %v", err)
	}
	var client bytes.Buffer
	if _, err := io.Copy(io.MultiWriter(&client, mw), bytes.NewReader(body)); err != nil {
		mw.Abort()
		t.Fatalf("copy: %v", err)
	}
	if err := mw.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return client.Bytes()
}

func enabledCache(t *testing.T, budget int64) *Cache {
	t.Helper()
	c, _ := testCache(t)
	c.SetMetadataMaxSize(budget)
	return c
}

func getMetaBody(t *testing.T, c *Cache, url string) ([]byte, *MetadataEntry) {
	t.Helper()
	entry, rc, err := c.GetMetadata(url)
	if err != nil {
		t.Fatalf("GetMetadata(%s): %v", url, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b, entry
}

func TestMetadata_ListMetadataURLs(t *testing.T) {
	c := enabledCache(t, 1<<20)
	// Disabled cache lists nothing.
	if urls, err := (func() ([]string, error) {
		d, _ := testCache(t)
		return d.ListMetadataURLs()
	})(); err != nil || len(urls) != 0 {
		t.Fatalf("disabled cache: urls=%v err=%v, want empty", urls, err)
	}

	want := []string{
		"http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages",
		"http://deb.debian.org/debian/dists/bookworm/InRelease",
	}
	for _, u := range want {
		putMeta(t, c, u, "", "", "", []byte("body-"+u))
	}

	got, err := c.ListMetadataURLs()
	if err != nil {
		t.Fatalf("ListMetadataURLs: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d urls, want %d (%v)", len(got), len(want), got)
	}
	seen := map[string]bool{}
	for _, u := range got {
		seen[u] = true
	}
	for _, u := range want {
		if !seen[u] {
			t.Errorf("missing url %q in %v", u, got)
		}
	}
}

func TestMetadata_DisabledByDefault(t *testing.T) {
	c, _ := testCache(t) // New() leaves metadata budget at 0
	if c.MetadataEnabled() {
		t.Fatal("metadata cache should be disabled by default")
	}
	if _, _, err := c.GetMetadata("http://x/Packages"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled GetMetadata should ErrNotFound, got %v", err)
	}
	// A commit while disabled must be a clean no-op (client copy still flowed).
	body := []byte("hello")
	got := putMeta(t, c, "http://x/Packages", "", "", "", body)
	if !bytes.Equal(got, body) {
		t.Fatal("client copy mangled while disabled")
	}
	if _, _, err := c.GetMetadata("http://x/Packages"); !errors.Is(err, ErrNotFound) {
		t.Fatal("nothing should have been stored while disabled")
	}
}

func TestMetadata_PutGetRoundTrip(t *testing.T) {
	c := enabledCache(t, 10*1024*1024)
	url := "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages"
	body := bytes.Repeat([]byte("pkg-data\n"), 500)

	client := putMeta(t, c, url, `"abc123"`, "Wed, 01 Jan 2026 00:00:00 GMT", "application/octet-stream", body)
	if !bytes.Equal(client, body) {
		t.Fatal("client copy differs from source")
	}

	got, entry := getMetaBody(t, c, url)
	if !bytes.Equal(got, body) {
		t.Fatal("cached body differs from source")
	}
	if entry.ETag != `"abc123"` || entry.LastModified != "Wed, 01 Jan 2026 00:00:00 GMT" {
		t.Fatalf("validators not preserved: %+v", entry)
	}
	if entry.ContentType != "application/octet-stream" {
		t.Fatalf("content type not preserved: %q", entry.ContentType)
	}
	if entry.Size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", entry.Size, len(body))
	}

	etag, lm, ok := c.MetadataValidators(url)
	if !ok || etag != `"abc123"` || lm == "" {
		t.Fatalf("MetadataValidators = %q %q %v", etag, lm, ok)
	}
	if !c.HasMetadata(url) {
		t.Fatal("HasMetadata should be true")
	}
	if c.MetadataCount() != 1 {
		t.Fatalf("count = %d, want 1", c.MetadataCount())
	}
	if c.MetadataSize() != int64(len(body)) {
		t.Fatalf("MetadataSize = %d, want %d", c.MetadataSize(), len(body))
	}
}

func TestMetadata_ReplaceUpdatesSizeAccounting(t *testing.T) {
	c := enabledCache(t, 10*1024*1024)
	url := "http://x/dists/stable/InRelease"

	putMeta(t, c, url, "e1", "", "", bytes.Repeat([]byte("a"), 1000))
	if c.MetadataSize() != 1000 {
		t.Fatalf("size after first put = %d", c.MetadataSize())
	}
	// Refresh with a smaller body — size must reflect the new body, not the sum.
	putMeta(t, c, url, "e2", "", "", bytes.Repeat([]byte("b"), 200))
	if c.MetadataSize() != 200 {
		t.Fatalf("size after replace = %d, want 200", c.MetadataSize())
	}
	if c.MetadataCount() != 1 {
		t.Fatalf("count after replace = %d, want 1", c.MetadataCount())
	}
	got, entry := getMetaBody(t, c, url)
	if len(got) != 200 || entry.ETag != "e2" {
		t.Fatalf("replace not applied: len=%d etag=%q", len(got), entry.ETag)
	}
}

func TestMetadata_RevalidatePreservesValidators(t *testing.T) {
	c := enabledCache(t, 1024*1024)
	url := "http://x/dists/stable/InRelease"
	putMeta(t, c, url, "orig-etag", "orig-lm", "", []byte("body"))

	// A 304 with no headers must NOT blank the known validators.
	c.RevalidateMetadata(url, "", "")
	etag, lm, ok := c.MetadataValidators(url)
	if !ok || etag != "orig-etag" || lm != "orig-lm" {
		t.Fatalf("empty revalidate blanked validators: %q %q", etag, lm)
	}

	// A 304 that does carry a fresh ETag should update it.
	c.RevalidateMetadata(url, "new-etag", "")
	etag, _, _ = c.MetadataValidators(url)
	if etag != "new-etag" {
		t.Fatalf("etag not updated on revalidate: %q", etag)
	}
}

func TestMetadata_ByHashVerification(t *testing.T) {
	c := enabledCache(t, 1024*1024)
	body := []byte("immutable index bytes")
	h := hashData(body)
	url := "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/by-hash/SHA256/" + h

	if !IsImmutableMetadataURL(url) {
		t.Fatal("by-hash URL should be immutable")
	}
	putMeta(t, c, url, "", "", "", body)
	got, _ := getMetaBody(t, c, url)
	if !bytes.Equal(got, body) {
		t.Fatal("by-hash body round-trip failed")
	}

	// A body that does not match the hash in the URL must be rejected.
	badURL := "http://x/dists/b/main/binary-amd64/by-hash/SHA256/" + hashData([]byte("something-else"))
	mw, err := c.NewMetadataWriter(badURL, "", "", "")
	if err != nil {
		t.Fatalf("NewMetadataWriter: %v", err)
	}
	if _, err := io.Copy(mw, bytes.NewReader(body)); err != nil { // body != hash in badURL
		t.Fatalf("copy: %v", err)
	}
	if err := mw.Commit(); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("mismatched by-hash Commit = %v, want ErrHashMismatch", err)
	}
	if c.HasMetadata(badURL) {
		t.Fatal("mismatched by-hash body should not be cached")
	}
}

func TestMetadata_SelfHealMissingFile(t *testing.T) {
	c := enabledCache(t, 1024*1024)
	url := "http://x/Packages"
	putMeta(t, c, url, "e", "", "", []byte("data"))

	// Delete the file out from under the cache (simulating corruption recovery).
	if err := os.Remove(c.metadataPath(url)); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if _, _, err := c.GetMetadata(url); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphaned row should miss, got %v", err)
	}
	// The stale row must have been dropped, restoring size accounting.
	if c.MetadataCount() != 0 {
		t.Fatalf("stale row not dropped, count = %d", c.MetadataCount())
	}
	if c.MetadataSize() != 0 {
		t.Fatalf("size not reclaimed, = %d", c.MetadataSize())
	}
}

func TestMetadata_EvictionRespectsBudget(t *testing.T) {
	budget := int64(2500)
	c := enabledCache(t, budget)
	block := bytes.Repeat([]byte("x"), 1000)

	for i, u := range []string{"http://x/a", "http://x/b", "http://x/c", "http://x/d"} {
		putMeta(t, c, u, "", "", "", block)
		if c.MetadataSize() > budget {
			t.Fatalf("after put %d, size %d exceeds budget %d", i, c.MetadataSize(), budget)
		}
	}
	// 4 * 1000B into a 2500B budget: at most 2 survive.
	if c.MetadataCount() > 2 {
		t.Fatalf("count = %d, expected <= 2 after eviction", c.MetadataCount())
	}
}

func TestMetadata_EvictsLeastRecentlyAccessed(t *testing.T) {
	budget := int64(2500)
	c := enabledCache(t, budget)
	block := bytes.Repeat([]byte("y"), 1000)

	putMeta(t, c, "http://x/keep", "", "", "", block)
	putMeta(t, c, "http://x/drop", "", "", "", block)

	// Access "keep" so it outranks "drop" for retention.
	_, rc, err := c.GetMetadata("http://x/keep")
	if err != nil {
		t.Fatalf("touch keep: %v", err)
	}
	_ = rc.Close()

	// A third entry forces one eviction; "drop" (never re-accessed) should go.
	putMeta(t, c, "http://x/new", "", "", "", block)

	if !c.HasMetadata("http://x/keep") {
		t.Error("recently-accessed entry was evicted")
	}
	if c.HasMetadata("http://x/drop") {
		t.Error("least-recently-accessed entry survived eviction")
	}
}
