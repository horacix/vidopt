package database

import "time"

// Status constants for file processing lifecycle.
const (
	StatusPending              = "pending"
	StatusProcessing           = "processing"
	StatusCompleted            = "completed"
	StatusSkippedAlreadyTarget = "skipped_already_target"
	StatusSkippedLowSavings    = "skipped_low_savings"
	StatusSkippedError         = "skipped_error"
)

// ProcessedFile represents a video file record in the SQLite database.
type ProcessedFile struct {
	ID                int64     `json:"id"`
	FilePath          string    `json:"file_path"`
	OutputPath        string    `json:"output_path"`
	FileSizeBytes     int64     `json:"file_size_bytes"`
	ModTime           time.Time `json:"mod_time"`
	DurationSeconds   float64   `json:"duration_seconds"`
	SourceCodec       string    `json:"source_codec"`
	SourceBitrate     int64     `json:"source_bitrate"`
	SourceResolution  string    `json:"source_resolution"`
	Status            string    `json:"status"`
	ProbeSavingsRatio float64   `json:"probe_savings_ratio"`
	TargetCodec       string    `json:"target_codec"`
	ChosenCRF         int       `json:"chosen_crf"`
	VMAFScore         float64   `json:"vmaf_score"`
	NewSizeBytes      int64     `json:"new_size_bytes"`
	SavedBytes        int64     `json:"saved_bytes"`
	EncoderUsed       string    `json:"encoder_used"`
	ProcessedAt       time.Time `json:"processed_at"`
	ErrorMessage      string    `json:"error_message"`
}

// StatsSummary holds aggregate metrics for the entire library.
type StatsSummary struct {
	TotalFilesCompleted int            `json:"total_files_completed"`
	TotalFilesSkipped   int            `json:"total_files_skipped"`
	TotalFilesPending   int            `json:"total_files_pending"`
	TotalOriginalBytes  int64          `json:"total_original_bytes"`
	TotalNewBytes       int64          `json:"total_new_bytes"`
	TotalSavedBytes     int64          `json:"total_saved_bytes"`
	OverallSavingsPct   float64        `json:"overall_savings_percent"`
	CodecBreakdown      map[string]int `json:"codec_breakdown"`
}
