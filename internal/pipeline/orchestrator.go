package pipeline

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/horacix/vidopt/internal/config"
	"github.com/horacix/vidopt/internal/database"
	"github.com/horacix/vidopt/internal/detector"
	"github.com/horacix/vidopt/internal/encoder"
)

// Orchestrator coordinates media discovery, evaluation, transcoding, and database tracking.
type Orchestrator struct {
	cfg        *config.Config
	db         *database.DB
	caps       *detector.HardwareCapabilities
	sampler    *encoder.Sampler
	transcoder *encoder.Transcoder
	workQueue  chan string
	wg         sync.WaitGroup
	mu         sync.Mutex
	closed     bool
	inFlight   map[string]bool
}

// NewOrchestrator constructs a ready-to-run Orchestrator.
func NewOrchestrator(cfg *config.Config, db *database.DB) (*Orchestrator, error) {
	caps, err := detector.DetectHardware()
	if err != nil {
		log.Printf("[orchestrator] warning: hardware detection encountered error: %v", err)
	}

	return &Orchestrator{
		cfg:        cfg,
		db:         db,
		caps:       caps,
		sampler:    encoder.NewSampler(cfg.ScratchPath),
		transcoder: encoder.NewTranscoder(),
		workQueue:  make(chan string, 1000),
		inFlight:   make(map[string]bool),
	}, nil
}

// StartWorkers launches the transcoding worker pool.
func (o *Orchestrator) StartWorkers(ctx context.Context) {
	workers := o.cfg.MaxWorkers
	if workers < 1 {
		workers = 1
	}

	for i := 0; i < workers; i++ {
		o.wg.Add(1)
		go func(workerID int) {
			defer o.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case filePath, ok := <-o.workQueue:
					if !ok {
						return
					}
					log.Printf("[worker-%d] processing: %s", workerID, filePath)
					if err := o.ProcessFile(filePath); err != nil {
						log.Printf("[worker-%d] error processing %s: %v", workerID, filePath, err)
					}
					o.mu.Lock()
					delete(o.inFlight, filePath)
					o.mu.Unlock()
				}
			}
		}(i + 1)
	}
}

// Enqueue safely adds a file to the processing queue if not already queued or closed.
func (o *Orchestrator) Enqueue(filePath string) bool {
	o.mu.Lock()
	if o.closed || o.inFlight[filePath] {
		o.mu.Unlock()
		return false
	}
	o.inFlight[filePath] = true
	o.mu.Unlock()

	select {
	case o.workQueue <- filePath:
		return true
	default:
		// If buffer is currently full, send in background so scanner/watcher never deadlocks
		go func() {
			o.mu.Lock()
			if o.closed {
				delete(o.inFlight, filePath)
				o.mu.Unlock()
				return
			}
			o.mu.Unlock()
			defer func() { _ = recover() }()
			o.workQueue <- filePath
		}()
		return true
	}
}

// Close gracefully waits for queue to empty and workers to stop.
func (o *Orchestrator) Close() {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.closed = true
	close(o.workQueue)
	o.mu.Unlock()
	o.wg.Wait()
}

// ScanLibrary walks LibraryPath and enqueues eligible unprocessed files.
func (o *Orchestrator) ScanLibrary() (int, error) {
	var count int
	log.Printf("[scanner] scanning library: %s", o.cfg.LibraryPath)

	err := filepath.WalkDir(o.cfg.LibraryPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == o.cfg.LibraryPath {
				log.Printf("[scanner] error accessing root library path %q: %v", path, err)
				return err
			}
			log.Printf("[scanner] warning: skipping inaccessible path %q: %v", path, err)
			return nil
		}
		if d.IsDir() {
			if path != o.cfg.LibraryPath && o.cfg.IsExcluded(path) {
				return filepath.SkipDir
			}
			return nil
		}

		if !o.cfg.IsVideoFile(path) || o.cfg.IsExcluded(path) {
			return nil
		}

		// Check if file is already up to date in DB (unless Reprocess is true)
		if !o.cfg.Reprocess {
			stat, err := d.Info()
			if err != nil {
				return nil
			}

			existing, err := o.db.GetFile(path)
			if err == nil && existing != nil {
				// If completed or intentionally skipped and unmodified since last evaluation
				if (existing.Status == database.StatusCompleted ||
					existing.Status == database.StatusSkippedAlreadyTarget ||
					existing.Status == database.StatusSkippedLowSavings) &&
					existing.FileSizeBytes == stat.Size() &&
					existing.ModTime.Equal(stat.ModTime()) {
					return nil
				}
			}
		}

		if o.Enqueue(path) {
			count++
		}
		return nil
	})

	log.Printf("[scanner] scan finished, queued %d candidate files", count)
	return count, err
}

// ProcessFile executes the full probe -> sample optimize -> transcode pipeline for a single file.
func (o *Orchestrator) ProcessFile(filePath string) error {
	sourcePath := filePath
	var targetDestination string

	// If re-transcoding and an uncompressed original backup exists, use the backup as source!
	if o.cfg.Reprocess {
		if origBackup := FindOriginalBackup(filePath); origBackup != "" && origBackup != filePath {
			log.Printf("[pipeline] found pristine original backup %q for %s; using original source for re-encoding",
				filepath.Base(origBackup), filepath.Base(filePath))
			sourcePath = origBackup
			targetDestination = filePath
		}
	}

	stat, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("cannot stat file %q: %w", sourcePath, err)
	}

	// 1. Probe input file metadata
	media, err := detector.ProbeMedia(sourcePath)
	if err != nil {
		_ = o.db.UpsertFile(&database.ProcessedFile{
			FilePath:      filePath,
			FileSizeBytes: stat.Size(),
			ModTime:       stat.ModTime(),
			Status:        database.StatusSkippedError,
			ErrorMessage:  fmt.Sprintf("ffprobe failed: %v", err),
			ProcessedAt:   time.Now(),
		})
		return err
	}

	// 2. Select optimal encoder
	selection := detector.SelectOptimalEncoder(o.caps, o.cfg.TargetCodec, o.cfg.HardwareAccel)

	// 3. Check if file is already encoded with target codec
	if !o.cfg.Reprocess && isAlreadyTargetCodec(media.VideoCodec, selection.CodecName) {
		log.Printf("[pipeline] skipping %q: already in target format (%s)", filePath, media.VideoCodec)
		return o.db.UpsertFile(&database.ProcessedFile{
			FilePath:         filePath,
			FileSizeBytes:    media.Size,
			ModTime:          stat.ModTime(),
			DurationSeconds:  media.Duration,
			SourceCodec:      media.VideoCodec,
			SourceBitrate:    media.Bitrate,
			SourceResolution: media.Resolution,
			Status:           database.StatusSkippedAlreadyTarget,
			TargetCodec:      selection.CodecName,
			ProcessedAt:      time.Now(),
		})
	}

	// Deinterlace filter
	deintFilter := o.cfg.GetDeinterlaceFilter(media.IsInterlaced)
	if deintFilter != "" {
		log.Printf("[pipeline] deinterlacing enabled (%s) for %s (codec: %s, field_order: %s)",
			deintFilter, filepath.Base(sourcePath), media.VideoCodec, media.FieldOrder)
	}

	// 4. Determine candidate CRFs (dynamic per-file perceptual search or fixed target CRF)
	candidates := o.cfg.GetCRFCandidates(media.Height, media.IsInterlaced, media.VideoCodec)
	if len(candidates) > 1 && o.cfg.TargetVMAF > 0 {
		log.Printf("[pipeline] running perceptual quality CRF search on %s (%s, %s, target VMAF: %.1f, seed CRF: %d)...",
			filepath.Base(filePath), media.Resolution, media.VideoCodec, o.cfg.TargetVMAF, candidates[len(candidates)/2])
	} else if len(candidates) > 1 {
		log.Printf("[pipeline] dynamic CRF auto-tuning for %s (%s, %s): candidates %v",
			filepath.Base(filePath), media.Resolution, media.VideoCodec, candidates)
	} else {
		log.Printf("[pipeline] running adaptive sample probe on %s (CRF: %d, encoder: %s)...",
			filepath.Base(filePath), candidates[0], selection.EncoderName)
	}

	sampleRes, err := o.sampler.EvaluateSavings(
		media,
		selection,
		candidates,
		o.cfg.EncoderPreset,
		o.cfg.MinSavingsPercent,
		o.cfg.TargetVMAF,
		deintFilter,
	)
	if err != nil {
		_ = o.db.UpsertFile(&database.ProcessedFile{
			FilePath:         filePath,
			FileSizeBytes:    media.Size,
			ModTime:          stat.ModTime(),
			DurationSeconds:  media.Duration,
			SourceCodec:      media.VideoCodec,
			SourceBitrate:    media.Bitrate,
			SourceResolution: media.Resolution,
			Status:           database.StatusSkippedError,
			ErrorMessage:     fmt.Sprintf("sample evaluation failed: %v", err),
			ProcessedAt:      time.Now(),
		})
		return fmt.Errorf("sample evaluation failed: %w", err)
	}

	// If sample evaluation does not qualify, skip
	if !sampleRes.Qualifies {
		var reason string
		if !sampleRes.QualityMet && o.cfg.TargetVMAF > 0 {
			reason = fmt.Sprintf("perceptual quality %.1f (%s) below target %.1f at CRF %d",
				sampleRes.VMAFScore, sampleRes.QualityMetric, o.cfg.TargetVMAF, sampleRes.CRF)
		} else {
			reason = fmt.Sprintf("projected savings %.1f%% < %.1f%% at quality-preserving CRF %d (%s %.1f)",
				sampleRes.SavingsPercent, o.cfg.MinSavingsPercent, sampleRes.CRF, sampleRes.QualityMetric, sampleRes.VMAFScore)
		}
		log.Printf("[pipeline] skipping %q: %s", filepath.Base(filePath), reason)

		return o.db.UpsertFile(&database.ProcessedFile{
			FilePath:          filePath,
			FileSizeBytes:     media.Size,
			ModTime:           stat.ModTime(),
			DurationSeconds:   media.Duration,
			SourceCodec:       media.VideoCodec,
			SourceBitrate:     media.Bitrate,
			SourceResolution:  media.Resolution,
			Status:            database.StatusSkippedLowSavings,
			ProbeSavingsRatio: sampleRes.SavingsRatio,
			TargetCodec:       selection.CodecName,
			ChosenCRF:         sampleRes.CRF,
			VMAFScore:         sampleRes.VMAFScore,
			ErrorMessage:      reason,
			ProcessedAt:       time.Now(),
		})
	}

	// 5. File qualifies: perform full transcode
	if sampleRes.VMAFScore > 0 {
		log.Printf("[pipeline] file qualifies! Selected CRF: %d (%s: %.2f, projected savings: %.1f%%). Starting full transcode...",
			sampleRes.CRF, sampleRes.QualityMetric, sampleRes.VMAFScore, sampleRes.SavingsPercent)
	} else {
		log.Printf("[pipeline] file qualifies! Projected savings: %.1f%% (CRF: %d). Starting full transcode...",
			sampleRes.SavingsPercent, sampleRes.CRF)
	}

	_ = o.db.UpsertFile(&database.ProcessedFile{
		FilePath:          filePath,
		FileSizeBytes:     media.Size,
		ModTime:           stat.ModTime(),
		DurationSeconds:   media.Duration,
		SourceCodec:       media.VideoCodec,
		SourceBitrate:     media.Bitrate,
		SourceResolution:  media.Resolution,
		Status:            database.StatusProcessing,
		ProbeSavingsRatio: sampleRes.SavingsRatio,
		TargetCodec:       selection.CodecName,
		ChosenCRF:         sampleRes.CRF,
		VMAFScore:         sampleRes.VMAFScore,
		EncoderUsed:       selection.EncoderName,
		ProcessedAt:       time.Now(),
	})

	opts := encoder.TranscodeOptions{
		ScratchDir:         o.cfg.ScratchPath,
		LibraryPath:        o.cfg.LibraryPath,
		OutputPath:         o.cfg.OutputPath,
		KeepBackup:         o.cfg.KeepBackup,
		PreserveTimestamps: o.cfg.PreserveTimestamps,
		CRF:                sampleRes.CRF,
		Preset:             o.cfg.EncoderPreset,
		DeinterlaceFilter:  deintFilter,
		TargetDestination:  targetDestination,
	}

	result, err := o.transcoder.Transcode(media, selection, opts)
	if err != nil {
		log.Printf("[pipeline] transcode failed for %s: %v", filepath.Base(filePath), err)
		_ = o.db.UpdateStatus(filePath, database.StatusSkippedError, err.Error())
		return err
	}

	// 6. Record completion in database
	log.Printf("[pipeline] successfully optimized %s! Saved %s (%.1f%% reduction). Output: %s",
		filepath.Base(filePath), formatBytes(result.SavedBytes), result.SavingsPercent, result.FinalPath)

	return o.db.RecordCompleted(&database.ProcessedFile{
		FilePath:          filePath,
		OutputPath:        result.FinalPath,
		FileSizeBytes:     result.OriginalSize,
		ModTime:           stat.ModTime(),
		DurationSeconds:   media.Duration,
		SourceCodec:       media.VideoCodec,
		SourceBitrate:     media.Bitrate,
		SourceResolution:  media.Resolution,
		ProbeSavingsRatio: sampleRes.SavingsRatio,
		TargetCodec:       selection.CodecName,
		ChosenCRF:         sampleRes.CRF,
		VMAFScore:         sampleRes.VMAFScore,
		NewSizeBytes:      result.NewSize,
		SavedBytes:        result.SavedBytes,
		EncoderUsed:       result.EncoderUsed,
	})
}

// isAlreadyTargetCodec checks if source codec is already the target modern format.
func isAlreadyTargetCodec(srcCodec, targetCodec string) bool {
	src := strings.ToLower(srcCodec)
	tgt := strings.ToLower(targetCodec)

	if tgt == "av1" && (src == "av1" || src == "libsvtav1") {
		return true
	}
	if tgt == "hevc" && (src == "hevc" || src == "h265") {
		return true
	}
	return false
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func FindOriginalBackup(filePath string) string {
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	stem := base[:len(base)-len(ext)]

	candidates := []string{
		filePath + ".original",
		filepath.Join(dir, stem+".avi.original"),
		filepath.Join(dir, stem+".mkv.original"),
		filepath.Join(dir, stem+".mp4.original"),
		filepath.Join(dir, stem+".mov.original"),
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return c
		}
	}
	return ""
}
