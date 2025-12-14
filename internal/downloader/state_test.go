package downloader

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	// Use a temp file-based database for better isolation
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}

	// Create required tables - execute separately to avoid multi-statement issues
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS downloads (
			id TEXT PRIMARY KEY,
			url TEXT NOT NULL,
			expected_size INTEGER NOT NULL,
			completed_size INTEGER DEFAULT 0,
			chunk_size INTEGER NOT NULL,
			total_chunks INTEGER NOT NULL,
			status TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			error TEXT
		)`)
	if err != nil {
		t.Fatalf("Failed to create downloads table: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS download_chunks (
			download_id TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			start_offset INTEGER NOT NULL,
			end_offset INTEGER NOT NULL,
			status TEXT NOT NULL,
			completed_at INTEGER,
			PRIMARY KEY (download_id, chunk_index),
			FOREIGN KEY (download_id) REFERENCES downloads(id) ON DELETE CASCADE
		)`)
	if err != nil {
		t.Fatalf("Failed to create download_chunks table: %v", err)
	}

	t.Cleanup(func() { db.Close() })
	return db
}

func TestNewStateManager(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	if sm == nil {
		t.Fatal("NewStateManager returned nil")
	}
	if sm.db != db {
		t.Error("StateManager db reference mismatch")
	}
}

func TestStateManager_CreateDownload(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "abc123def456"
	url := "http://example.com/package.deb"
	size := int64(10 * 1024 * 1024) // 10MB
	chunkSize := int64(4 * 1024 * 1024) // 4MB

	err := sm.CreateDownload(hash, url, size, chunkSize)
	if err != nil {
		t.Fatalf("CreateDownload failed: %v", err)
	}

	// Verify download was created
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM downloads WHERE id = ?", hash).Scan(&count)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 download record, got %d", count)
	}

	// Verify chunks were created (10MB / 4MB = 3 chunks)
	err = db.QueryRow("SELECT COUNT(*) FROM download_chunks WHERE download_id = ?", hash).Scan(&count)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 chunk records, got %d", count)
	}
}

func TestStateManager_CreateDownload_ChunkBoundaries(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "test_chunks"
	size := int64(10 * 1024 * 1024) // 10MB
	chunkSize := int64(4 * 1024 * 1024) // 4MB

	err := sm.CreateDownload(hash, "http://example.com/test.deb", size, chunkSize)
	if err != nil {
		t.Fatalf("CreateDownload failed: %v", err)
	}

	// Get chunks and verify boundaries
	rows, err := db.Query("SELECT chunk_index, start_offset, end_offset FROM download_chunks WHERE download_id = ? ORDER BY chunk_index", hash)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	expected := []struct {
		index int
		start int64
		end   int64
	}{
		{0, 0, 4 * 1024 * 1024},
		{1, 4 * 1024 * 1024, 8 * 1024 * 1024},
		{2, 8 * 1024 * 1024, 10 * 1024 * 1024}, // Last chunk is smaller
	}

	i := 0
	for rows.Next() {
		var index int
		var start, end int64
		if err := rows.Scan(&index, &start, &end); err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		if i >= len(expected) {
			t.Errorf("More chunks than expected")
			break
		}
		if index != expected[i].index || start != expected[i].start || end != expected[i].end {
			t.Errorf("Chunk %d: got (%d, %d, %d), want (%d, %d, %d)",
				i, index, start, end, expected[i].index, expected[i].start, expected[i].end)
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("Got %d chunks, expected %d", i, len(expected))
	}
}

func TestStateManager_GetDownload(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "get_test"
	url := "http://example.com/get.deb"
	size := int64(8 * 1024 * 1024)
	chunkSize := int64(4 * 1024 * 1024)

	err := sm.CreateDownload(hash, url, size, chunkSize)
	if err != nil {
		t.Fatalf("CreateDownload failed: %v", err)
	}

	state, err := sm.GetDownload(hash)
	if err != nil {
		t.Fatalf("GetDownload failed: %v", err)
	}
	if state == nil {
		t.Fatal("GetDownload returned nil")
	}

	if state.ID != hash {
		t.Errorf("ID = %q, want %q", state.ID, hash)
	}
	if state.URL != url {
		t.Errorf("URL = %q, want %q", state.URL, url)
	}
	if state.ExpectedSize != size {
		t.Errorf("ExpectedSize = %d, want %d", state.ExpectedSize, size)
	}
	if state.ChunkSize != chunkSize {
		t.Errorf("ChunkSize = %d, want %d", state.ChunkSize, chunkSize)
	}
	if state.TotalChunks != 2 {
		t.Errorf("TotalChunks = %d, want 2", state.TotalChunks)
	}
	if state.Status != "pending" {
		t.Errorf("Status = %q, want pending", state.Status)
	}
	if len(state.Chunks) != 2 {
		t.Errorf("Chunks length = %d, want 2", len(state.Chunks))
	}
}

func TestStateManager_GetDownload_NotFound(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	state, err := sm.GetDownload("nonexistent")
	if err != nil {
		t.Fatalf("GetDownload failed: %v", err)
	}
	if state != nil {
		t.Errorf("Expected nil for nonexistent download, got %+v", state)
	}
}

func TestStateManager_GetPendingDownloads(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	// Create multiple downloads with different statuses
	if err := sm.CreateDownload("pending1", "http://example.com/1.deb", 1024, 512); err != nil {
		t.Fatalf("CreateDownload pending1 failed: %v", err)
	}
	if err := sm.CreateDownload("pending2", "http://example.com/2.deb", 1024, 512); err != nil {
		t.Fatalf("CreateDownload pending2 failed: %v", err)
	}
	if err := sm.CreateDownload("completed", "http://example.com/3.deb", 1024, 512); err != nil {
		t.Fatalf("CreateDownload completed failed: %v", err)
	}

	// Mark one as in_progress and one as completed
	if err := sm.UpdateDownloadStatus("pending2", "in_progress"); err != nil {
		t.Fatalf("UpdateDownloadStatus failed: %v", err)
	}
	if err := sm.CompleteDownload("completed"); err != nil {
		t.Fatalf("CompleteDownload failed: %v", err)
	}

	pending, err := sm.GetPendingDownloads()
	if err != nil {
		t.Fatalf("GetPendingDownloads failed: %v", err)
	}

	if len(pending) != 2 {
		t.Errorf("Expected 2 pending downloads, got %d", len(pending))
	}

	// Verify completed is not in the list
	for _, p := range pending {
		if p.ID == "completed" {
			t.Error("Completed download should not be in pending list")
		}
	}
}

func TestStateManager_UpdateDownloadStatus(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "status_test"
	sm.CreateDownload(hash, "http://example.com/test.deb", 1024, 512)

	err := sm.UpdateDownloadStatus(hash, "in_progress")
	if err != nil {
		t.Fatalf("UpdateDownloadStatus failed: %v", err)
	}

	state, _ := sm.GetDownload(hash)
	if state.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress", state.Status)
	}
}

func TestStateManager_UpdateChunk(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "chunk_test"
	size := int64(8 * 1024 * 1024)
	chunkSize := int64(4 * 1024 * 1024)
	sm.CreateDownload(hash, "http://example.com/test.deb", size, chunkSize)

	// Complete first chunk
	err := sm.UpdateChunk(hash, 0, "completed")
	if err != nil {
		t.Fatalf("UpdateChunk failed: %v", err)
	}

	state, _ := sm.GetDownload(hash)

	// First chunk should be completed
	if state.Chunks[0].Status != "completed" {
		t.Errorf("Chunk 0 status = %q, want completed", state.Chunks[0].Status)
	}
	if state.Chunks[0].CompletedAt.IsZero() {
		t.Error("Chunk 0 CompletedAt should be set")
	}

	// Second chunk should still be pending
	if state.Chunks[1].Status != "pending" {
		t.Errorf("Chunk 1 status = %q, want pending", state.Chunks[1].Status)
	}

	// CompletedSize should be updated (first chunk = 4MB)
	if state.CompletedSize != chunkSize {
		t.Errorf("CompletedSize = %d, want %d", state.CompletedSize, chunkSize)
	}
}

func TestStateManager_UpdateChunk_AllCompleted(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "all_chunks_test"
	size := int64(8 * 1024 * 1024)
	chunkSize := int64(4 * 1024 * 1024)
	sm.CreateDownload(hash, "http://example.com/test.deb", size, chunkSize)

	// Complete both chunks
	sm.UpdateChunk(hash, 0, "completed")
	sm.UpdateChunk(hash, 1, "completed")

	state, _ := sm.GetDownload(hash)

	// CompletedSize should equal total size
	if state.CompletedSize != size {
		t.Errorf("CompletedSize = %d, want %d", state.CompletedSize, size)
	}
}

func TestStateManager_CompleteDownload(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "complete_test"
	sm.CreateDownload(hash, "http://example.com/test.deb", 1024, 512)

	err := sm.CompleteDownload(hash)
	if err != nil {
		t.Fatalf("CompleteDownload failed: %v", err)
	}

	state, _ := sm.GetDownload(hash)
	if state.Status != "completed" {
		t.Errorf("Status = %q, want completed", state.Status)
	}
}

func TestStateManager_FailDownload(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "fail_test"
	sm.CreateDownload(hash, "http://example.com/test.deb", 1024, 512)

	errMsg := "connection timeout"
	err := sm.FailDownload(hash, errMsg)
	if err != nil {
		t.Fatalf("FailDownload failed: %v", err)
	}

	state, _ := sm.GetDownload(hash)
	if state.Status != "failed" {
		t.Errorf("Status = %q, want failed", state.Status)
	}
	if state.Error != errMsg {
		t.Errorf("Error = %q, want %q", state.Error, errMsg)
	}
}

func TestStateManager_DeleteDownload(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "delete_test"
	sm.CreateDownload(hash, "http://example.com/test.deb", 1024, 512)

	// Verify it exists
	state, _ := sm.GetDownload(hash)
	if state == nil {
		t.Fatal("Download should exist before delete")
	}

	err := sm.DeleteDownload(hash)
	if err != nil {
		t.Fatalf("DeleteDownload failed: %v", err)
	}

	// Verify it's gone
	state, _ = sm.GetDownload(hash)
	if state != nil {
		t.Error("Download should be nil after delete")
	}

	// Verify chunks are also deleted
	var count int
	db.QueryRow("SELECT COUNT(*) FROM download_chunks WHERE download_id = ?", hash).Scan(&count)
	if count != 0 {
		t.Errorf("Expected 0 chunks after delete, got %d", count)
	}
}

func TestStateManager_CleanupStale(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	// Create downloads
	sm.CreateDownload("old_pending", "http://example.com/1.deb", 1024, 512)
	sm.CreateDownload("old_failed", "http://example.com/2.deb", 1024, 512)
	sm.CreateDownload("recent_pending", "http://example.com/3.deb", 1024, 512)
	sm.CreateDownload("completed", "http://example.com/4.deb", 1024, 512)

	sm.FailDownload("old_failed", "error")
	sm.CompleteDownload("completed")

	// Make old downloads stale by updating their timestamp
	oldTime := time.Now().Add(-48 * time.Hour).Unix()
	db.Exec("UPDATE downloads SET updated_at = ? WHERE id IN ('old_pending', 'old_failed')", oldTime)

	cleaned, err := sm.CleanupStale(24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupStale failed: %v", err)
	}

	if cleaned != 2 {
		t.Errorf("Cleaned = %d, want 2", cleaned)
	}

	// Verify old ones are gone
	state, _ := sm.GetDownload("old_pending")
	if state != nil {
		t.Error("old_pending should be deleted")
	}
	state, _ = sm.GetDownload("old_failed")
	if state != nil {
		t.Error("old_failed should be deleted")
	}

	// Verify recent and completed are still there
	state, _ = sm.GetDownload("recent_pending")
	if state == nil {
		t.Error("recent_pending should still exist")
	}
	state, _ = sm.GetDownload("completed")
	if state == nil {
		t.Error("completed should still exist")
	}
}

func TestStateManager_GetPendingChunks(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "pending_chunks_test"
	sm.CreateDownload(hash, "http://example.com/test.deb", 12*1024*1024, 4*1024*1024) // 3 chunks

	// Complete first chunk
	sm.UpdateChunk(hash, 0, "completed")

	chunks, err := sm.GetPendingChunks(hash)
	if err != nil {
		t.Fatalf("GetPendingChunks failed: %v", err)
	}

	if len(chunks) != 2 {
		t.Errorf("Expected 2 pending chunks, got %d", len(chunks))
	}

	// Verify chunk indices
	for _, c := range chunks {
		if c.Index == 0 {
			t.Error("Chunk 0 should not be in pending list")
		}
		if c.Status == "completed" {
			t.Error("Completed chunk should not be in pending list")
		}
	}
}

func TestStateManager_GetPendingChunks_AllCompleted(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "all_done_test"
	sm.CreateDownload(hash, "http://example.com/test.deb", 8*1024*1024, 4*1024*1024) // 2 chunks

	sm.UpdateChunk(hash, 0, "completed")
	sm.UpdateChunk(hash, 1, "completed")

	chunks, err := sm.GetPendingChunks(hash)
	if err != nil {
		t.Fatalf("GetPendingChunks failed: %v", err)
	}

	if len(chunks) != 0 {
		t.Errorf("Expected 0 pending chunks, got %d", len(chunks))
	}
}

func TestStateManager_SingleChunkDownload(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "single_chunk"
	size := int64(1024)
	chunkSize := int64(4 * 1024 * 1024) // Larger than file

	err := sm.CreateDownload(hash, "http://example.com/small.deb", size, chunkSize)
	if err != nil {
		t.Fatalf("CreateDownload failed: %v", err)
	}

	state, _ := sm.GetDownload(hash)
	if state.TotalChunks != 1 {
		t.Errorf("TotalChunks = %d, want 1", state.TotalChunks)
	}
	if len(state.Chunks) != 1 {
		t.Errorf("Chunks length = %d, want 1", len(state.Chunks))
	}
	if state.Chunks[0].End != size {
		t.Errorf("Chunk end = %d, want %d", state.Chunks[0].End, size)
	}
}

func TestStateManager_InProgressChunk(t *testing.T) {
	db := setupTestDB(t)
	sm := NewStateManager(db)

	hash := "in_progress_chunk"
	sm.CreateDownload(hash, "http://example.com/test.deb", 8*1024*1024, 4*1024*1024)

	// Mark chunk as in_progress (not completed)
	err := sm.UpdateChunk(hash, 0, "in_progress")
	if err != nil {
		t.Fatalf("UpdateChunk failed: %v", err)
	}

	state, _ := sm.GetDownload(hash)

	// CompletedSize should NOT be updated for in_progress
	if state.CompletedSize != 0 {
		t.Errorf("CompletedSize = %d, want 0 for in_progress", state.CompletedSize)
	}

	// Chunk should be in pending list
	chunks, _ := sm.GetPendingChunks(hash)
	found := false
	for _, c := range chunks {
		if c.Index == 0 && c.Status == "in_progress" {
			found = true
			break
		}
	}
	if !found {
		t.Error("In-progress chunk should be in pending list")
	}
}
