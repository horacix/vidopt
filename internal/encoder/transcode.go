package encoder

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/horacix/vidopt/internal/detector"
)

// TranscodeOptions encapsulates settings for a full video transcode job.
type TranscodeOptions struct {
	ScratchDir         string
	LibraryPath        string
	OutputPath         string
	KeepBackup         bool
	PreserveTimestamps bool
	CRF                int
	Preset             string
	DeinterlaceFilter  string
	TargetDestination  string
}

// TranscodeResult reports metrics upon completion of a transcode.
type TranscodeResult struct {
	FinalPath      string
	OriginalSize   int64
	NewSize        int64
	SavedBytes     int64
	SavingsPercent float64
	EncoderUsed    string
}

// Transcoder executes full video transcodes and manages file placement.
type Transcoder struct{}

// NewTranscoder creates a new Transcoder.
func NewTranscoder() *Transcoder {
	return &Transcoder{}
}

// Transcode executes the full transcode pipeline according to options.
func (t *Transcoder) Transcode(
	media *detector.MediaInfo,
	selection detector.EncoderSelection,
	opts TranscodeOptions,
) (*TranscodeResult, error) {
	origStat, err := os.Stat(media.FilePath)
	if err != nil {
		return nil, fmt.Errorf("cannot stat source file %q: %w", media.FilePath, err)
	}
	origMtime := origStat.ModTime()

	// Generate unique temp filename in scratch directory
	rnd := make([]byte, 4)
	_, _ = rand.Read(rnd)
	tempFilename := fmt.Sprintf("transcode_%s_%s.mkv", hex.EncodeToString(rnd), filepath.Base(media.FilePath))
	scratchOutputPath := filepath.Join(opts.ScratchDir, tempFilename)

	// Ensure cleanup on failure
	defer func() {
		if _, err := os.Stat(scratchOutputPath); err == nil {
			_ = os.Remove(scratchOutputPath)
		}
	}()

	// Build FFmpeg command
	args := []string{"-y", "-hide_banner", "-loglevel", "warning"}

	if len(selection.HwAccelArgs) > 0 {
		args = append(args, selection.HwAccelArgs...)
	}

	args = append(args, "-i", media.FilePath)

	// Stream mapping: primary video stream
	args = append(args, "-map", "0:v:0")

	if opts.DeinterlaceFilter != "" {
		args = append(args, "-vf", opts.DeinterlaceFilter)
	}

	args = append(args, "-c:v", selection.EncoderName)

	// Set pixel format for hardware & player compatibility
	pixFmt := detector.ChoosePixelFormat(media.PixelFormat, selection.EncoderName)
	if pixFmt != "" {
		args = append(args, "-pix_fmt", pixFmt)
	}

	// CRF / Quality
	if strings.Contains(selection.EncoderName, "nvenc") {
		args = append(args, "-rc:v", "vbr", "-cq:v", strconv.Itoa(opts.CRF), "-b:v", "0")
	} else if selection.IsHardware {
		args = append(args, "-global_quality", strconv.Itoa(opts.CRF))
	} else {
		args = append(args, "-crf", strconv.Itoa(opts.CRF))
	}

	// Preset
	normPreset := normalizePreset(selection.EncoderName, opts.Preset)
	if normPreset != "" {
		args = append(args, "-preset", normPreset)
	} else if len(selection.ExtraParams) > 0 {
		args = append(args, selection.ExtraParams...)
	}

	// Audio, Subtitles, Attachments copy losslessly
	args = append(args,
		"-map", "0:a?", "-c:a", "copy",
		"-map", "0:s?", "-c:s", "copy",
		"-map", "0:d?", "-c:d", "copy",
		"-map_metadata", "0",
		scratchOutputPath,
	)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr // Stream warnings/errors directly

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg transcoding failed: %w", err)
	}

	// Verify temp output exists and has valid size
	tempStat, err := os.Stat(scratchOutputPath)
	if err != nil || tempStat.Size() == 0 {
		return nil, fmt.Errorf("transcoded output file is missing or empty")
	}

	// Verify that output file size is actually smaller than original
	if tempStat.Size() >= origStat.Size() {
		return nil, fmt.Errorf("transcoded file (%d bytes) is larger than original (%d bytes); aborting replacement", tempStat.Size(), origStat.Size())
	}

	// Verify integrity and duration using ffprobe
	outInfo, err := detector.ProbeMedia(scratchOutputPath)
	if err != nil {
		return nil, fmt.Errorf("integrity probe failed on transcoded file: %w", err)
	}

	// Ensure duration matches original within 2%
	if media.Duration > 0 && outInfo.Duration > 0 {
		durationDiff := math.Abs(media.Duration - outInfo.Duration)
		if durationDiff > 3.0 && (durationDiff/media.Duration) > 0.02 {
			return nil, fmt.Errorf("transcoded duration (%.2fs) differs significantly from original (%.2fs)", outInfo.Duration, media.Duration)
		}
	}

	// File Placement / Finalization
	var finalPath string
	if opts.TargetDestination != "" {
		finalPath = opts.TargetDestination
		if finalPath != media.FilePath {
			_ = os.Remove(finalPath)
		}
		if err := moveFile(scratchOutputPath, finalPath); err != nil {
			return nil, fmt.Errorf("failed to move transcoded file into place %q: %w", finalPath, err)
		}
	} else if opts.OutputPath == "" {
		// IN-PLACE MODE:
		finalPath = replaceExt(media.FilePath, ".mkv")
		if opts.KeepBackup {
			backupPath := media.FilePath + ".original"
			if err := moveFile(media.FilePath, backupPath); err != nil {
				return nil, fmt.Errorf("failed to create backup file %q: %w", backupPath, err)
			}
		} else {
			// Remove original file before moving new file in place
			_ = os.Remove(media.FilePath)
		}

		if err := moveFile(scratchOutputPath, finalPath); err != nil {
			return nil, fmt.Errorf("failed to move transcoded file into place %q: %w", finalPath, err)
		}
	} else {
		// SEPARATE OUTPUT FOLDER MODE (Unmanic-style):
		relPath, err := filepath.Rel(opts.LibraryPath, media.FilePath)
		if err != nil || strings.HasPrefix(relPath, "..") {
			relPath = filepath.Base(media.FilePath)
		}
		finalPath = filepath.Join(opts.OutputPath, relPath)

		// Change extension to .mkv if container changed
		finalPath = replaceExt(finalPath, ".mkv")

		destDir := filepath.Dir(finalPath)
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create destination directory %q: %w", destDir, err)
		}

		if err := moveFile(scratchOutputPath, finalPath); err != nil {
			return nil, fmt.Errorf("failed to move transcoded file to output destination %q: %w", finalPath, err)
		}
	}

	// Restore original modification timestamps if requested
	if opts.PreserveTimestamps {
		_ = os.Chtimes(finalPath, time.Now(), origMtime)
	}

	savedBytes := origStat.Size() - tempStat.Size()
	savingsPercent := (float64(savedBytes) / float64(origStat.Size())) * 100.0

	return &TranscodeResult{
		FinalPath:      finalPath,
		OriginalSize:   origStat.Size(),
		NewSize:        tempStat.Size(),
		SavedBytes:     savedBytes,
		SavingsPercent: savingsPercent,
		EncoderUsed:    selection.EncoderName,
	}, nil
}

// moveFile attempts atomic rename, falling back to copy+delete across filesystem boundaries.
func moveFile(src, dst string) error {
	// Try fast atomic rename first
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Fallback to copy and remove (e.g. when moving between different mount points/scratch drive)
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}

	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}

	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}

	_ = in.Close()
	return os.Remove(src)
}

func replaceExt(path, newExt string) string {
	ext := filepath.Ext(path)
	return path[:len(path)-len(ext)] + newExt
}

func normalizePreset(encoderName, preset string) string {
	preset = strings.TrimSpace(preset)
	if encoderName == "libsvtav1" {
		preset = strings.TrimPrefix(preset, "preset")
		preset = strings.TrimPrefix(preset, "preset ")
		preset = strings.TrimSpace(preset)
		if preset == "" {
			return "6"
		}
	}
	if strings.Contains(encoderName, "nvenc") {
		if len(preset) == 1 && preset >= "1" && preset <= "7" {
			return "p" + preset
		}
		if preset == "" || preset == "6" {
			return "p6"
		}
	}
	return preset
}
