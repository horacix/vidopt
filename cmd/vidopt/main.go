package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/horacix/vidopt/internal/config"
	"github.com/horacix/vidopt/internal/database"
	"github.com/horacix/vidopt/internal/detector"
	"github.com/horacix/vidopt/internal/encoder"
	"github.com/horacix/vidopt/internal/monitor"
	"github.com/horacix/vidopt/internal/pipeline"
	"github.com/spf13/cobra"
)

var (
	cfgFile           string
	flagLibraryPath   string
	flagOutputPath    string
	flagScratchPath   string
	flagDatabasePath  string
	flagTargetCodec   string
	flagMinSavings    float64
	flagHardwareAccel string
	flagTargetCRF     int
	flagTargetVMAF    float64
	flagKeepBackup    bool
	flagPreserveTimes bool
	flagWatchRealtime bool
	flagDeinterlace   string
	flagReprocess     bool
	flagForceAlias    bool
	flagPreset        string
	flagSchedule      string
	flagDebounce      int
	flagMaxWorkers    int
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "vidopt",
		Short: "NAS Video Storage Optimizer with hardware-accelerated transcoding and adaptive sampling",
		Long: `vidopt is a lightweight, high-performance background daemon and CLI utility
designed for NAS devices (Synology, Intel N-series) to optimize video libraries.
It uses adaptive sample probing (inspired by ab-av1) to verify space savings
before committing to full re-encodes, keeping all intermediate files in a scratch directory.`,
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "Path to configuration file")
	rootCmd.PersistentFlags().StringVar(&flagLibraryPath, "library-path", "", "Path to video library folder")
	rootCmd.PersistentFlags().StringVar(&flagOutputPath, "output-path", "", "Optional destination folder (leaves input untouched)")
	rootCmd.PersistentFlags().StringVar(&flagScratchPath, "scratch-path", "", "Path to scratch directory for temporary encodes")
	rootCmd.PersistentFlags().StringVar(&flagDatabasePath, "database-path", "", "Path to SQLite tracking database")
	rootCmd.PersistentFlags().StringVar(&flagTargetCodec, "target-codec", "", "Target video codec: auto, av1, or hevc")
	rootCmd.PersistentFlags().Float64Var(&flagMinSavings, "min-savings", 0, "Minimum required percentage space savings (e.g. 20.0)")
	rootCmd.PersistentFlags().StringVar(&flagHardwareAccel, "hw-accel", "", "Hardware acceleration: auto, nvenc, qsv, vaapi, or cpu")
	rootCmd.PersistentFlags().IntVar(&flagTargetCRF, "crf", -1, "Target CRF (-1/0 = dynamic perceptual quality search)")
	rootCmd.PersistentFlags().Float64Var(&flagTargetVMAF, "vmaf", -1, "Target VMAF perceptual quality score (default 93.0; 0 disables VMAF search)")
	rootCmd.PersistentFlags().StringVar(&flagPreset, "preset", "", "Encoder speed preset (e.g. 6 for SVT-AV1, p6 for NVENC)")
	rootCmd.PersistentFlags().StringVar(&flagSchedule, "schedule", "", "Cron expression for periodic scans (e.g. @daily, @hourly)")
	rootCmd.PersistentFlags().IntVar(&flagDebounce, "debounce", -1, "Settling time in seconds for in-progress file writes")
	rootCmd.PersistentFlags().IntVar(&flagMaxWorkers, "workers", 0, "Maximum concurrent transcoding jobs")
	rootCmd.PersistentFlags().BoolVar(&flagKeepBackup, "keep-backup", true, "Keep original file as .original in in-place mode")
	rootCmd.PersistentFlags().BoolVar(&flagPreserveTimes, "preserve-timestamps", true, "Preserve original file modification timestamps")
	rootCmd.PersistentFlags().BoolVar(&flagWatchRealtime, "watch", false, "Enable real-time fsnotify filesystem watching")
	rootCmd.PersistentFlags().StringVar(&flagDeinterlace, "deinterlace", "", "Deinterlacing mode: auto, on, off, or custom filter (e.g. bwdif=mode=0)")
	rootCmd.PersistentFlags().BoolVarP(&flagReprocess, "reprocess", "r", false, "Re-evaluate and re-transcode files even if already completed or in target format")
	rootCmd.PersistentFlags().BoolVarP(&flagForceAlias, "force", "f", false, "Alias for --reprocess")
	_ = rootCmd.PersistentFlags().MarkHidden("force")

	rootCmd.AddCommand(newRunCmd())
	rootCmd.AddCommand(newScanCmd())
	rootCmd.AddCommand(newProbeCmd())
	rootCmd.AddCommand(newProcessCmd())
	rootCmd.AddCommand(newStatsCmd())
	rootCmd.AddCommand(newHwCheckCmd())
	rootCmd.AddCommand(newConfigInitCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func getResolvedConfig(cmd *cobra.Command) (*config.Config, error) {
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		return nil, err
	}

	// CLI flags override config & env vars
	if flagLibraryPath != "" {
		cfg.LibraryPath = config.CleanCLIPath(flagLibraryPath)
	}
	if flagOutputPath != "" {
		cfg.OutputPath = config.CleanCLIPath(flagOutputPath)
	}
	if flagScratchPath != "" {
		cfg.ScratchPath = config.CleanCLIPath(flagScratchPath)
	}
	if flagDatabasePath != "" {
		cfg.DatabasePath = config.CleanCLIPath(flagDatabasePath)
	}
	if flagTargetCodec != "" {
		cfg.TargetCodec = flagTargetCodec
	}
	if flagMinSavings > 0 {
		cfg.MinSavingsPercent = flagMinSavings
	}
	if flagHardwareAccel != "" {
		cfg.HardwareAccel = flagHardwareAccel
	}
	if flagTargetCRF >= 0 {
		cfg.TargetCRF = flagTargetCRF
	}
	if flagTargetVMAF >= 0 {
		cfg.TargetVMAF = flagTargetVMAF
	}
	if flagPreset != "" {
		cfg.EncoderPreset = flagPreset
	}
	if flagSchedule != "" {
		cfg.ScanSchedule = flagSchedule
	}
	if flagDebounce >= 0 {
		cfg.DebounceSeconds = flagDebounce
	}
	if flagMaxWorkers > 0 {
		cfg.MaxWorkers = flagMaxWorkers
	}
	if cmd != nil {
		if cmd.Flags().Changed("keep-backup") {
			cfg.KeepBackup = flagKeepBackup
		}
		if cmd.Flags().Changed("preserve-timestamps") {
			cfg.PreserveTimestamps = flagPreserveTimes
		}
		if cmd.Flags().Changed("watch") {
			cfg.WatchRealtime = flagWatchRealtime
		}
	} else if flagWatchRealtime {
		cfg.WatchRealtime = true
	}
	if flagDeinterlace != "" {
		cfg.Deinterlace = flagDeinterlace
	}
	if flagReprocess || flagForceAlias {
		cfg.Reprocess = true
	}

	if err := cfg.EnsureDirectories(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the background optimization service (scheduler and optional watcher)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getResolvedConfig(cmd)
			if err != nil {
				return err
			}

			db, err := database.Open(cfg.DatabasePath)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			orch, err := pipeline.NewOrchestrator(cfg, db)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			orch.StartWorkers(ctx)

			// Setup filesystem watcher if enabled
			var watcher *monitor.FileWatcher
			if cfg.WatchRealtime {
				eventsChan := make(chan string, 100)
				watcher, err = monitor.NewFileWatcher(cfg, eventsChan)
				if err != nil {
					return fmt.Errorf("failed to start filesystem watcher: %w", err)
				}
				if err := watcher.Start(); err != nil {
					return err
				}
				defer watcher.Stop()

				go func() {
					for path := range eventsChan {
						orch.Enqueue(path)
					}
				}()
				log.Println("[vidopt] real-time filesystem watcher active")
			}

			// Setup cron scheduler
			sched := monitor.NewScheduler(cfg.ScanSchedule, func() {
				_, _ = orch.ScanLibrary()
			})
			if err := sched.Start(); err != nil {
				return err
			}
			defer sched.Stop()

			// Perform initial library scan on startup
			go func() {
				_, _ = orch.ScanLibrary()
			}()

			log.Printf("[vidopt] service running. Monitoring %q (schedule: %s)", cfg.LibraryPath, cfg.ScanSchedule)

			// Wait for termination signal
			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
			<-sigChan

			log.Println("[vidopt] shutting down cleanly...")
			cancel()
			orch.Close()
			return nil
		},
	}
}

func newScanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scan",
		Short: "Perform a one-time scan and optimization pass, then exit",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getResolvedConfig(cmd)
			if err != nil {
				return err
			}

			db, err := database.Open(cfg.DatabasePath)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			orch, err := pipeline.NewOrchestrator(cfg, db)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			orch.StartWorkers(ctx)

			count, err := orch.ScanLibrary()
			if err != nil {
				return err
			}

			fmt.Printf("Discovered %d candidate video files. Processing queue...\n", count)
			orch.Close()
			fmt.Println("Scan pass complete.")
			return nil
		},
	}
}

func newProbeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "probe <file>",
		Short: "Inspect a video file and run an adaptive sample probe (ab-av1 style)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := config.CleanCLIPath(args[0])
			cfg, err := getResolvedConfig(cmd)
			if err != nil {
				return err
			}

			probePath := filePath
			if cfg.Reprocess {
				if orig := pipeline.FindOriginalBackup(filePath); orig != "" && orig != filePath {
					fmt.Printf("[probe] Detected pristine master backup: %s (using untouched source for probe)\n", filepath.Base(orig))
					probePath = orig
				}
			}

			media, err := detector.ProbeMedia(probePath)
			if err != nil {
				return fmt.Errorf("probe failed: %w", err)
			}

			caps, _ := detector.DetectHardware()
			selection := detector.SelectOptimalEncoder(caps, cfg.TargetCodec, cfg.HardwareAccel)

			deintFilter := cfg.GetDeinterlaceFilter(media.IsInterlaced)

			fmt.Printf("\n--- File Information ---\n")
			fmt.Printf("File:        %s\n", media.FilePath)
			fmt.Printf("Size:        %s\n", formatBytes(media.Size))
			fmt.Printf("Duration:    %.2f seconds (%.1f mins)\n", media.Duration, media.Duration/60.0)
			fmt.Printf("Codec:       %s\n", media.VideoCodec)
			fmt.Printf("Resolution:  %s (%.2f fps)\n", media.Resolution, media.FrameRate)
			scanType := "Progressive"
			if media.IsInterlaced {
				scanType = fmt.Sprintf("Interlaced (%s)", media.FieldOrder)
				if deintFilter != "" {
					scanType += fmt.Sprintf(" -> Deinterlacing with %s", deintFilter)
				}
			}
			fmt.Printf("Scan Type:   %s\n", scanType)
			fmt.Printf("Bitrate:     %s\n", formatBitrate(media.Bitrate))
			fmt.Printf("Audio/Subs:  %d audio tracks, %d subtitle tracks\n", media.AudioTracks, media.SubtitleTracks)

			fmt.Printf("\n--- Target Encoder ---\n")
			fmt.Printf("Codec:       %s\n", selection.CodecName)
			fmt.Printf("Encoder:     %s (Hardware: %v)\n", selection.EncoderName, selection.IsHardware)

			sampler := encoder.NewSampler(cfg.ScratchPath)
			plan := encoder.CalculateSamplePlan(media.Duration)
			fmt.Printf("Sample Plan: %d sample segments (total %.1fs tested)\n", len(plan.Clips), plan.TotalSampleDuration)

			candidates := cfg.GetCRFCandidates(media.Height, media.IsInterlaced, media.VideoCodec)
			if len(candidates) > 1 && cfg.TargetVMAF > 0 {
				fmt.Printf("CRF Search:  Perceptual quality optimization (Target VMAF >= %.1f, seed CRF %d, range [%d..%d])\n",
					cfg.TargetVMAF, candidates[len(candidates)/2], encoder.DefaultMinSearchCRF, encoder.DefaultMaxSearchCRF)
			} else if len(candidates) > 1 {
				fmt.Printf("Dynamic CRF: Auto-tuned candidates %v based on %s (%s)\n",
					candidates, media.Resolution, media.VideoCodec)
			} else {
				fmt.Printf("Target CRF:  %d\n", candidates[0])
			}

			fmt.Printf("\nRunning adaptive sample probe...")
			res, err := sampler.EvaluateSavings(media, selection, candidates, cfg.EncoderPreset, cfg.MinSavingsPercent, cfg.TargetVMAF, deintFilter)
			if err != nil {
				return fmt.Errorf("sample evaluation failed: %w", err)
			}

			if len(res.History) > 0 && cfg.TargetVMAF > 0 {
				fmt.Printf("\n\n--- Perceptual Quality Search Trajectory (Target >= %.1f) ---\n", cfg.TargetVMAF)
				for i, h := range res.History {
					status := "below target quality"
					if h.QualityMet {
						status = "quality met ✓"
					}
					metricName := h.QualityMetric
					if metricName == "" {
						metricName = "VMAF"
					}
					fmt.Printf("  Probe %d: CRF %2d -> %s %5.2f (%s) | Savings: %5.1f%% | Est. Bitrate: %s\n",
						i+1, h.CRF, metricName, h.VMAFScore, status, h.SavingsPercent, formatBitrate(h.EstimatedBitrate))
				}
			}

			fmt.Printf("\n--- Sample Results (ab-av1 Style) ---\n")
			fmt.Printf("Selected CRF:        %d\n", res.CRF)
			if res.VMAFScore > 0 {
				metricName := res.QualityMetric
				if metricName == "" {
					metricName = "VMAF"
				}
				fmt.Printf("Perceptual Quality:  %s %.2f (Target >= %.1f)\n", metricName, res.VMAFScore, cfg.TargetVMAF)
			}
			fmt.Printf("Projected Bitrate:   %s\n", formatBitrate(res.EstimatedBitrate))
			fmt.Printf("Projected Savings:   %.1f%%\n", res.SavingsPercent)
			fmt.Printf("Threshold Required:  %.1f%%\n", cfg.MinSavingsPercent)

			if res.Qualifies {
				fmt.Printf("Recommendation:      QUALIFIES FOR TRANSCODING (Preserves quality & meets >= %.1f%% savings threshold)\n", cfg.MinSavingsPercent)
			} else if !res.QualityMet && cfg.TargetVMAF > 0 {
				fmt.Printf("Recommendation:      SKIP (Perceptual quality %.2f is below target %.1f)\n", res.VMAFScore, cfg.TargetVMAF)
			} else {
				fmt.Printf("Recommendation:      SKIP (Projected savings %.1f%% at quality-preserving CRF %d is below %.1f%% threshold)\n",
					res.SavingsPercent, res.CRF, cfg.MinSavingsPercent)
			}

			return nil
		},
	}
}

func newProcessCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "process <file>",
		Short: "Directly process and transcode a single video file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := config.CleanCLIPath(args[0])
			cfg, err := getResolvedConfig(cmd)
			if err != nil {
				return err
			}

			db, err := database.Open(cfg.DatabasePath)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			orch, err := pipeline.NewOrchestrator(cfg, db)
			if err != nil {
				return err
			}

			return orch.ProcessFile(filePath)
		},
	}
}

func newStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Display storage savings and library statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getResolvedConfig(cmd)
			if err != nil {
				return err
			}

			db, err := database.Open(cfg.DatabasePath)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			stats, err := db.GetStats()
			if err != nil {
				return err
			}

			fmt.Println("\n================= vidopt Library Statistics =================")
			fmt.Printf("Completed Files:     %d\n", stats.TotalFilesCompleted)
			fmt.Printf("Skipped Files:       %d\n", stats.TotalFilesSkipped)
			fmt.Printf("Pending Files:       %d\n", stats.TotalFilesPending)
			fmt.Printf("Original Size:       %s\n", formatBytes(stats.TotalOriginalBytes))
			fmt.Printf("Optimized Size:      %s\n", formatBytes(stats.TotalNewBytes))
			fmt.Printf("Total Space Saved:   %s (%.1f%% overall reduction)\n", formatBytes(stats.TotalSavedBytes), stats.OverallSavingsPct)

			if len(stats.CodecBreakdown) > 0 {
				fmt.Println("\n--- Source Codecs Processed ---")
				for codec, count := range stats.CodecBreakdown {
					fmt.Printf("  • %-12s: %d files\n", codec, count)
				}
			}
			fmt.Println("=============================================================")

			return nil
		},
	}
}

func newHwCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hwcheck",
		Short: "Check system hardware acceleration capabilities (QSV, VAAPI, encoders)",
		Run: func(cmd *cobra.Command, args []string) {
			caps, err := detector.DetectHardware()
			if err != nil {
				fmt.Printf("Error detecting hardware: %v\n", err)
				return
			}

			fmt.Println("\n--- Hardware Acceleration Diagnostic ---")
			fmt.Printf("Linux Render Node (/dev/dri): %v\n", caps.HasDriRenderNode)
			fmt.Printf("NVIDIA GPU (CUDA/NVENC):     %v\n", caps.HasCudaGPU)

			fmt.Println("\nAvailable FFmpeg Hardware Accelerators:")
			if len(caps.AvailableHwaccels) == 0 {
				fmt.Println("  (none found)")
			} else {
				for _, a := range caps.AvailableHwaccels {
					fmt.Printf("  • %s\n", a)
				}
			}

			fmt.Println("\nSupported Modern Encoders:")
			encoders := []string{
				"av1_nvenc", "hevc_nvenc", "h264_nvenc",
				"av1_qsv", "hevc_qsv", "h264_qsv",
				"av1_vaapi", "hevc_vaapi",
				"libsvtav1", "libx265",
			}
			for _, enc := range encoders {
				supported := caps.SupportedEncoders[enc]
				status := "Not available"
				if supported {
					status = "Available ✓"
				}
				fmt.Printf("  • %-14s: %s\n", enc, status)
			}

			selAuto := detector.SelectOptimalEncoder(caps, "auto", "auto")
			fmt.Printf("\nDefault Auto-Selection: %s (hardware: %v)\n", selAuto.EncoderName, selAuto.IsHardware)
		},
	}
}

func newConfigInitCmd() *cobra.Command {
	cfgCmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management utilities",
	}
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a sample config.yaml with full documentation",
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "config.yaml"
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("config.yaml already exists in current directory")
			}

			exampleConfig := `# vidopt Configuration
# All options can be overridden with environment variables (e.g. VIDOPT_LIBRARY_PATH=/media)
# or command-line flags (e.g. --library-path /media).

# Source media library to scan and optimize
library_path: "/library"

# Optional separate output folder (Unmanic-style).
# If left empty (""), vidopt operates in-place, replacing original files.
output_path: ""

# Fast temporary scratch path (isolated from RAID parity drives)
scratch_path: "/scratch"

# SQLite database file path
database_path: "/data/vidopt.db"

# Minimum space savings required to justify re-encoding (percent)
min_savings_percent: 20.0

# Target modern video codec: "auto", "av1", or "hevc"
# "auto" detects best hardware acceleration available (QSV/VAAPI) or falls back to SVT-AV1
target_codec: "auto"

# Encoder preset (e.g. "6" for svt-av1, "p6" for nvenc, "quality" for qsv)
encoder_preset: "6"

# Constant Rate Factor (CRF). 0 = auto-tune using perceptual quality (VMAF) sample search
target_crf: 0

# Target VMAF perceptual quality score (0-100).
# 93.0 is the standard threshold for perceptually transparent (indistinguishable) compression.
target_vmaf: 93.0

# Hardware acceleration mode: "auto", "nvenc", "qsv", "vaapi", or "cpu"
hardware_accel: "auto"

# Deinterlacing mode: "auto" (default: detect interlaced video and apply bwdif),
# "on" (always deinterlace), "off" (never), or custom filter expression (e.g. "bwdif=mode=0", "bwdif=mode=1")
deinterlace: "auto"

# Re-evaluate and re-transcode files even if already processed or already in target codec
reprocess: false

# Periodic scan schedule (cron syntax: "@daily", "@hourly", "0 2 * * *")
scan_schedule: "@daily"

# Real-time filesystem watching with fsnotify
watch_realtime: false

# Settling time in seconds before processing a newly written file
debounce_seconds: 60

# Preserve original file modification timestamps (avoids media server re-indexing)
preserve_timestamps: true

# In-place mode: keep original files with .original suffix
keep_backup: true

# Directories or patterns to ignore
excluded_paths:
  - "@eaDir"
  - "#recycle"
  - ".grab"
  - ".tmp"
  - ".backup"

# Video extensions to monitor
video_extensions:
  - ".mkv"
  - ".mp4"
  - ".avi"
  - ".mov"
  - ".ts"
  - ".m4v"
  - ".webm"

# Maximum concurrent transcode jobs (1 is recommended for NAS hardware GPUs)
max_workers: 1
`
			if err := os.WriteFile(target, []byte(exampleConfig), 0644); err != nil {
				return err
			}

			fmt.Println("Generated config.yaml successfully.")
			return nil
		},
	}
	cfgCmd.AddCommand(initCmd)
	return cfgCmd
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

func formatBitrate(br int64) string {
	if br <= 0 {
		return "unknown"
	}
	if br >= 1_000_000 {
		return fmt.Sprintf("%.2f Mbps", float64(br)/1_000_000.0)
	}
	return fmt.Sprintf("%.0f kbps", float64(br)/1_000.0)
}
