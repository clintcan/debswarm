// Package downloader - download state persistence
package downloader

import (
	"database/sql"
	"fmt"
	"time"
)

// DownloadState represents a resumable download
type DownloadState struct {
	ID            string
	URL           string
	ExpectedSize  int64
	CompletedSize int64
	ChunkSize     int64
	TotalChunks   int
	Status        string // pending, in_progress, completed, failed
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Error         string
	Chunks        []*ChunkState
}

// ChunkState represents a single chunk's state
type ChunkState struct {
	Index       int
	Start       int64
	End         int64
	Status      string // pending, in_progress, completed
	CompletedAt time.Time
}

// StateManager handles download state persistence
type StateManager struct {
	db *sql.DB
}

// NewStateManager creates a new state manager
func NewStateManager(db *sql.DB) *StateManager {
	return &StateManager{db: db}
}

// CreateDownload creates a new download record with chunks
func (sm *StateManager) CreateDownload(hash, url string, size, chunkSize int64) error {
	now := time.Now().Unix()
	totalChunks := int((size + chunkSize - 1) / chunkSize)

	tx, err := sm.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Insert download record
	_, err = tx.Exec(`
		INSERT INTO downloads (id, url, expected_size, chunk_size, total_chunks, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`,
		hash, url, size, chunkSize, totalChunks, now, now)
	if err != nil {
		return err
	}

	// Insert chunk records
	stmt, err := tx.Prepare(`
		INSERT INTO download_chunks (download_id, chunk_index, start_offset, end_offset, status)
		VALUES (?, ?, ?, ?, 'pending')`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i := 0; i < totalChunks; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize
		if end > size {
			end = size
		}
		if _, err := stmt.Exec(hash, i, start, end); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetDownload returns a download state by hash
func (sm *StateManager) GetDownload(hash string) (*DownloadState, error) {
	var state DownloadState
	var createdAt, updatedAt int64
	var errStr sql.NullString

	err := sm.db.QueryRow(`
		SELECT id, url, expected_size, completed_size, chunk_size, total_chunks, status, created_at, updated_at, error
		FROM downloads WHERE id = ?`, hash).Scan(
		&state.ID, &state.URL, &state.ExpectedSize, &state.CompletedSize,
		&state.ChunkSize, &state.TotalChunks, &state.Status,
		&createdAt, &updatedAt, &errStr)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	state.CreatedAt = time.Unix(createdAt, 0)
	state.UpdatedAt = time.Unix(updatedAt, 0)
	if errStr.Valid {
		state.Error = errStr.String
	}

	// Load chunks
	rows, err := sm.db.Query(`
		SELECT chunk_index, start_offset, end_offset, status, completed_at
		FROM download_chunks WHERE download_id = ? ORDER BY chunk_index`, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var cs ChunkState
		var completedAt sql.NullInt64
		if err := rows.Scan(&cs.Index, &cs.Start, &cs.End, &cs.Status, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			cs.CompletedAt = time.Unix(completedAt.Int64, 0)
		}
		state.Chunks = append(state.Chunks, &cs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating chunks: %w", err)
	}

	return &state, nil
}

// GetPendingDownloads returns incomplete downloads
func (sm *StateManager) GetPendingDownloads() ([]*DownloadState, error) {
	rows, err := sm.db.Query(`
		SELECT id FROM downloads WHERE status IN ('pending', 'in_progress')
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []*DownloadState
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		state, err := sm.GetDownload(hash)
		if err != nil {
			return nil, err
		}
		if state != nil {
			states = append(states, state)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating downloads: %w", err)
	}

	return states, nil
}

// UpdateDownloadStatus updates the download status
func (sm *StateManager) UpdateDownloadStatus(hash, status string) error {
	_, err := sm.db.Exec(`
		UPDATE downloads SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now().Unix(), hash)
	return err
}

// UpdateChunk marks a chunk as completed
func (sm *StateManager) UpdateChunk(hash string, index int, status string) error {
	now := time.Now().Unix()

	tx, err := sm.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Update chunk
	var completedAt interface{}
	if status == "completed" {
		completedAt = now
	}
	_, err = tx.Exec(`
		UPDATE download_chunks SET status = ?, completed_at = ?
		WHERE download_id = ? AND chunk_index = ?`,
		status, completedAt, hash, index)
	if err != nil {
		return err
	}

	// Update completed size if chunk completed
	if status == "completed" {
		_, err = tx.Exec(`
			UPDATE downloads SET
				completed_size = (SELECT SUM(end_offset - start_offset) FROM download_chunks WHERE download_id = ? AND status = 'completed'),
				updated_at = ?
			WHERE id = ?`,
			hash, now, hash)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// CompleteDownload marks download as completed and cleans up
func (sm *StateManager) CompleteDownload(hash string) error {
	_, err := sm.db.Exec(`
		UPDATE downloads SET status = 'completed', updated_at = ? WHERE id = ?`,
		time.Now().Unix(), hash)
	return err
}

// FailDownload marks download as failed
func (sm *StateManager) FailDownload(hash string, errMsg string) error {
	_, err := sm.db.Exec(`
		UPDATE downloads SET status = 'failed', error = ?, updated_at = ? WHERE id = ?`,
		errMsg, time.Now().Unix(), hash)
	return err
}

// DeleteDownload removes a download and its chunks
func (sm *StateManager) DeleteDownload(hash string) error {
	tx, err := sm.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(`DELETE FROM download_chunks WHERE download_id = ?`, hash)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`DELETE FROM downloads WHERE id = ?`, hash)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// CleanupStale removes old incomplete downloads
func (sm *StateManager) CleanupStale(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).Unix()

	result, err := sm.db.Exec(`
		DELETE FROM downloads WHERE status IN ('pending', 'in_progress', 'failed') AND updated_at < ?`,
		cutoff)
	if err != nil {
		return 0, err
	}

	count, _ := result.RowsAffected()
	return int(count), nil
}

// GetPendingChunks returns chunks that need to be downloaded
func (sm *StateManager) GetPendingChunks(hash string) ([]*ChunkState, error) {
	rows, err := sm.db.Query(`
		SELECT chunk_index, start_offset, end_offset, status
		FROM download_chunks
		WHERE download_id = ? AND status != 'completed'
		ORDER BY chunk_index`, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []*ChunkState
	for rows.Next() {
		var cs ChunkState
		if err := rows.Scan(&cs.Index, &cs.Start, &cs.End, &cs.Status); err != nil {
			return nil, err
		}
		chunks = append(chunks, &cs)
	}

	return chunks, nil
}
