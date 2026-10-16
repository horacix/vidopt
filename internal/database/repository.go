package database

import (
	"database/sql"
	"fmt"
	"time"
)

// GetFile retrieves a record by its original file path. Returns nil, nil if not found.
func (db *DB) GetFile(filePath string) (*ProcessedFile, error) {
	query := `
	SELECT id, file_path, output_path, file_size_bytes, mod_time, duration_seconds,
	       source_codec, source_bitrate, source_resolution, status, probe_savings_ratio,
	       target_codec, chosen_crf, vmaf_score, new_size_bytes, saved_bytes,
	       encoder_used, processed_at, error_message
	FROM processed_files
	WHERE file_path = ?;`

	row := db.conn.QueryRow(query, filePath)

	var f ProcessedFile
	var processedAt sql.NullTime

	err := row.Scan(
		&f.ID, &f.FilePath, &f.OutputPath, &f.FileSizeBytes, &f.ModTime, &f.DurationSeconds,
		&f.SourceCodec, &f.SourceBitrate, &f.SourceResolution, &f.Status, &f.ProbeSavingsRatio,
		&f.TargetCodec, &f.ChosenCRF, &f.VMAFScore, &f.NewSizeBytes, &f.SavedBytes,
		&f.EncoderUsed, &processedAt, &f.ErrorMessage,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query file %q: %w", filePath, err)
	}

	if processedAt.Valid {
		f.ProcessedAt = processedAt.Time
	}

	return &f, nil
}

// UpsertFile inserts or updates basic metadata and status for a file.
func (db *DB) UpsertFile(f *ProcessedFile) error {
	query := `
	INSERT INTO processed_files (
		file_path, output_path, file_size_bytes, mod_time, duration_seconds,
		source_codec, source_bitrate, source_resolution, status, probe_savings_ratio,
		target_codec, chosen_crf, vmaf_score, new_size_bytes, saved_bytes,
		encoder_used, processed_at, error_message
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(file_path) DO UPDATE SET
		output_path = excluded.output_path,
		file_size_bytes = excluded.file_size_bytes,
		mod_time = excluded.mod_time,
		duration_seconds = excluded.duration_seconds,
		source_codec = excluded.source_codec,
		source_bitrate = excluded.source_bitrate,
		source_resolution = excluded.source_resolution,
		status = excluded.status,
		probe_savings_ratio = excluded.probe_savings_ratio,
		target_codec = excluded.target_codec,
		chosen_crf = excluded.chosen_crf,
		vmaf_score = excluded.vmaf_score,
		new_size_bytes = excluded.new_size_bytes,
		saved_bytes = excluded.saved_bytes,
		encoder_used = excluded.encoder_used,
		processed_at = excluded.processed_at,
		error_message = excluded.error_message;`

	var processedAt interface{}
	if !f.ProcessedAt.IsZero() {
		processedAt = f.ProcessedAt
	}

	_, err := db.conn.Exec(query,
		f.FilePath, f.OutputPath, f.FileSizeBytes, f.ModTime, f.DurationSeconds,
		f.SourceCodec, f.SourceBitrate, f.SourceResolution, f.Status, f.ProbeSavingsRatio,
		f.TargetCodec, f.ChosenCRF, f.VMAFScore, f.NewSizeBytes, f.SavedBytes,
		f.EncoderUsed, processedAt, f.ErrorMessage,
	)

	if err != nil {
		return fmt.Errorf("failed to upsert file %q: %w", f.FilePath, err)
	}

	return nil
}

// UpdateStatus updates just the status and error message for a given file path.
func (db *DB) UpdateStatus(filePath string, status string, errorMsg string) error {
	query := `UPDATE processed_files SET status = ?, error_message = ?, processed_at = ? WHERE file_path = ?;`
	_, err := db.conn.Exec(query, status, errorMsg, time.Now(), filePath)
	return err
}

// RecordCompleted marks a transcode job as successfully completed with final metrics.
func (db *DB) RecordCompleted(f *ProcessedFile) error {
	f.Status = StatusCompleted
	f.ProcessedAt = time.Now()
	f.SavedBytes = f.FileSizeBytes - f.NewSizeBytes
	return db.UpsertFile(f)
}

// GetStats compiles summary statistics across all processed and discovered files.
func (db *DB) GetStats() (*StatsSummary, error) {
	summary := &StatsSummary{
		CodecBreakdown: make(map[string]int),
	}

	// Completed files metrics
	row := db.conn.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(file_size_bytes), 0), COALESCE(SUM(new_size_bytes), 0), COALESCE(SUM(saved_bytes), 0)
		FROM processed_files
		WHERE status = ?;`, StatusCompleted)

	err := row.Scan(&summary.TotalFilesCompleted, &summary.TotalOriginalBytes, &summary.TotalNewBytes, &summary.TotalSavedBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate completed stats: %w", err)
	}

	if summary.TotalOriginalBytes > 0 {
		summary.OverallSavingsPct = (float64(summary.TotalSavedBytes) / float64(summary.TotalOriginalBytes)) * 100.0
	}

	// Skipped files count
	err = db.conn.QueryRow(`SELECT COUNT(*) FROM processed_files WHERE status LIKE 'skipped_%';`).Scan(&summary.TotalFilesSkipped)
	if err != nil {
		return nil, fmt.Errorf("failed to count skipped files: %w", err)
	}

	// Pending files count
	err = db.conn.QueryRow(`SELECT COUNT(*) FROM processed_files WHERE status = ?;`, StatusPending).Scan(&summary.TotalFilesPending)
	if err != nil {
		return nil, fmt.Errorf("failed to count pending files: %w", err)
	}

	// Codec breakdown
	rows, err := db.conn.Query(`SELECT source_codec, COUNT(*) FROM processed_files GROUP BY source_codec;`)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var codec string
			var count int
			if err := rows.Scan(&codec, &count); err == nil && codec != "" {
				summary.CodecBreakdown[codec] = count
			}
		}
	}

	return summary, nil
}

// ListRecentFiles returns the most recently processed files up to limit.
func (db *DB) ListRecentFiles(limit int) ([]*ProcessedFile, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
	SELECT id, file_path, output_path, file_size_bytes, mod_time, duration_seconds,
	       source_codec, source_bitrate, source_resolution, status, probe_savings_ratio,
	       target_codec, chosen_crf, vmaf_score, new_size_bytes, saved_bytes,
	       encoder_used, processed_at, error_message
	FROM processed_files
	ORDER BY id DESC
	LIMIT ?;`

	rows, err := db.conn.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []*ProcessedFile
	for rows.Next() {
		var f ProcessedFile
		var processedAt sql.NullTime

		err := rows.Scan(
			&f.ID, &f.FilePath, &f.OutputPath, &f.FileSizeBytes, &f.ModTime, &f.DurationSeconds,
			&f.SourceCodec, &f.SourceBitrate, &f.SourceResolution, &f.Status, &f.ProbeSavingsRatio,
			&f.TargetCodec, &f.ChosenCRF, &f.VMAFScore, &f.NewSizeBytes, &f.SavedBytes,
			&f.EncoderUsed, &processedAt, &f.ErrorMessage,
		)
		if err != nil {
			return nil, err
		}
		if processedAt.Valid {
			f.ProcessedAt = processedAt.Time
		}
		results = append(results, &f)
	}

	return results, nil
}
