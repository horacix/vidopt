package database

import (
	"testing"
	"time"
)

func TestDatabaseOperations(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()

	testFile := &ProcessedFile{
		FilePath:          "/library/movies/test.mkv",
		FileSizeBytes:     1_000_000_000,
		ModTime:           time.Now().Truncate(time.Second),
		DurationSeconds:   7200,
		SourceCodec:       "h264",
		SourceBitrate:     8_000_000,
		SourceResolution:  "1920x1080",
		Status:            StatusPending,
		ProbeSavingsRatio: 0.35,
		TargetCodec:       "hevc",
		ChosenCRF:         24,
	}

	// 1. Test Upsert
	if err := db.UpsertFile(testFile); err != nil {
		t.Fatalf("failed to upsert file: %v", err)
	}

	// 2. Test Get
	retrieved, err := db.GetFile(testFile.FilePath)
	if err != nil {
		t.Fatalf("failed to get file: %v", err)
	}
	if retrieved == nil {
		t.Fatalf("expected file to be found, got nil")
	}
	if retrieved.SourceCodec != "h264" {
		t.Errorf("expected source codec h264, got %s", retrieved.SourceCodec)
	}
	if retrieved.Status != StatusPending {
		t.Errorf("expected status pending, got %s", retrieved.Status)
	}

	// 3. Test RecordCompleted
	testFile.NewSizeBytes = 600_000_000
	testFile.EncoderUsed = "hevc_qsv"
	if err := db.RecordCompleted(testFile); err != nil {
		t.Fatalf("failed to record completion: %v", err)
	}

	retrieved, err = db.GetFile(testFile.FilePath)
	if err != nil {
		t.Fatalf("failed to get completed file: %v", err)
	}
	if retrieved.Status != StatusCompleted {
		t.Errorf("expected status completed, got %s", retrieved.Status)
	}
	if retrieved.SavedBytes != 400_000_000 {
		t.Errorf("expected saved bytes 400000000, got %d", retrieved.SavedBytes)
	}

	// 4. Test Stats Summary
	stats, err := db.GetStats()
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if stats.TotalFilesCompleted != 1 {
		t.Errorf("expected 1 completed file, got %d", stats.TotalFilesCompleted)
	}
	if stats.TotalSavedBytes != 400_000_000 {
		t.Errorf("expected 400MB saved, got %d", stats.TotalSavedBytes)
	}
	if stats.OverallSavingsPct != 40.0 {
		t.Errorf("expected 40%% savings, got %.2f", stats.OverallSavingsPct)
	}
	if stats.CodecBreakdown["h264"] != 1 {
		t.Errorf("expected 1 h264 file in breakdown, got %d", stats.CodecBreakdown["h264"])
	}

	// 5. Test ListRecentFiles
	recent, err := db.ListRecentFiles(10)
	if err != nil {
		t.Fatalf("failed to list recent files: %v", err)
	}
	if len(recent) != 1 {
		t.Errorf("expected 1 recent file, got %d", len(recent))
	}
}
