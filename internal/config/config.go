package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config represents all operational settings for vidopt.
type Config struct {
	// LibraryPath is the source folder containing video files to monitor and optimize.
	LibraryPath string `mapstructure:"library_path" yaml:"library_path" json:"library_path"`

	// OutputPath is the optional destination folder.
	// When empty, operates in-place (replaces original in LibraryPath).
	// When set, mirrors directory structure and places optimized files in OutputPath.
	OutputPath string `mapstructure:"output_path" yaml:"output_path" json:"output_path"`

	// ScratchPath is the directory used for temporary encoding and sample clips.
	// Isolates intensive writes from RAID arrays.
	ScratchPath string `mapstructure:"scratch_path" yaml:"scratch_path" json:"scratch_path"`

	// DatabasePath is the path to the SQLite tracking database.
	DatabasePath string `mapstructure:"database_path" yaml:"database_path" json:"database_path"`

	// MinSavingsPercent is the minimum percentage of file size reduction required (default: 20.0).
	MinSavingsPercent float64 `mapstructure:"min_savings_percent" yaml:"min_savings_percent" json:"min_savings_percent"`

	// TargetCodec specifies target video codec: "auto", "av1", or "hevc" (default: "auto").
	TargetCodec string `mapstructure:"target_codec" yaml:"target_codec" json:"target_codec"`

	// EncoderPreset specifies preset parameter for the encoder (e.g., "preset 6", "medium", "quality").
	EncoderPreset string `mapstructure:"encoder_preset" yaml:"encoder_preset" json:"encoder_preset"`

	// TargetCRF is the Constant Rate Factor.
	// 0 means auto-tune CRF dynamically on the fly based on resolution, interlace status, and sample probe.
	TargetCRF int `mapstructure:"target_crf" yaml:"target_crf" json:"target_crf"`

	// CRFTiers specifies custom baseline CRFs per resolution tier when target_crf is 0.
	CRFTiers CRFTiersConfig `mapstructure:"crf_tiers" yaml:"crf_tiers" json:"crf_tiers"`

	// TargetVMAF is the optional target VMAF quality score (e.g., 93.0). 0 disables VMAF targeting.
	TargetVMAF float64 `mapstructure:"target_vmaf" yaml:"target_vmaf" json:"target_vmaf"`

	// HardwareAccel specifies hardware acceleration: "auto", "qsv", "vaapi", or "cpu".
	HardwareAccel string `mapstructure:"hardware_accel" yaml:"hardware_accel" json:"hardware_accel"`

	// ScanSchedule is a cron expression or shortcut ("@daily", "@hourly", "0 2 * * *").
	ScanSchedule string `mapstructure:"scan_schedule" yaml:"scan_schedule" json:"scan_schedule"`

	// WatchRealtime enables continuous filesystem watching with fsnotify (default: false).
	WatchRealtime bool `mapstructure:"watch_realtime" yaml:"watch_realtime" json:"watch_realtime"`

	// DebounceSeconds is the settling time in seconds before processing a newly written file (default: 60).
	DebounceSeconds int `mapstructure:"debounce_seconds" yaml:"debounce_seconds" json:"debounce_seconds"`

	// PreserveTimestamps preserves original file modification time on optimized files (default: true).
	PreserveTimestamps bool `mapstructure:"preserve_timestamps" yaml:"preserve_timestamps" json:"preserve_timestamps"`

	// KeepBackup keeps original files in in-place mode (.backup or .original) (default: true).
	KeepBackup bool `mapstructure:"keep_backup" yaml:"keep_backup" json:"keep_backup"`

	// ExcludedPaths lists directories or glob patterns to skip.
	ExcludedPaths []string `mapstructure:"excluded_paths" yaml:"excluded_paths" json:"excluded_paths"`

	// VideoExtensions lists video file extensions to scan (default: .mkv, .mp4, .avi, .mov, .ts, .m4v, .webm).
	VideoExtensions []string `mapstructure:"video_extensions" yaml:"video_extensions" json:"video_extensions"`

	// Deinterlace controls deinterlacing: "auto" (default), "on", "off", or custom filter string like "bwdif=mode=0".
	Deinterlace string `mapstructure:"deinterlace" yaml:"deinterlace" json:"deinterlace"`

	// Reprocess re-evaluates and re-transcodes files even if already processed or already in target codec.
	Reprocess bool `mapstructure:"reprocess" yaml:"reprocess" json:"reprocess"`

	// MaxWorkers is the number of concurrent transcoding jobs (default: 1 for hardware safety).
	MaxWorkers int `mapstructure:"max_workers" yaml:"max_workers" json:"max_workers"`
}

// CRFTiersConfig defines baseline CRFs per resolution tier when target_crf is 0 (auto-tune).
type CRFTiersConfig struct {
	SD  int `mapstructure:"sd" yaml:"sd" json:"sd"`    // <= 576p (DV, VCR, VHS, DVD, e.g. 22)
	HD  int `mapstructure:"hd" yaml:"hd" json:"hd"`    // <= 720p (e.g. 24)
	FHD int `mapstructure:"fhd" yaml:"fhd" json:"fhd"` // <= 1080p (e.g. 26)
	UHD int `mapstructure:"uhd" yaml:"uhd" json:"uhd"` // > 1080p (4K+, e.g. 28)
}

// DefaultConfig returns a Config struct populated with safe, production-ready defaults.
func DefaultConfig() *Config {
	return &Config{
		LibraryPath:       "./library",
		OutputPath:        "",
		ScratchPath:       "./scratch",
		DatabasePath:      "./data/vidopt.db",
		MinSavingsPercent: 20.0,
		TargetCodec:       "auto",
		EncoderPreset:     "6",
		TargetCRF:         0, // 0 = dynamic auto-tuning on the fly based on file characteristics
		CRFTiers: CRFTiersConfig{
			SD:  22,
			HD:  24,
			FHD: 26,
			UHD: 28,
		},
		TargetVMAF:         93.0,
		HardwareAccel:      "auto",
		Deinterlace:        "auto",
		Reprocess:          false,
		ScanSchedule:       "@daily",
		WatchRealtime:      false,
		DebounceSeconds:    60,
		PreserveTimestamps: true,
		KeepBackup:         true,
		ExcludedPaths:      []string{"@eaDir", "#recycle", ".grab", ".tmp", ".backup"},
		VideoExtensions:    []string{".mkv", ".mp4", ".avi", ".mov", ".ts", ".m4v", ".webm"},
		MaxWorkers:         1,
	}
}

// LoadConfig initializes Viper, loads the specified config file (if any),
// binds environment variables with prefix VIDOPT_, and returns the parsed Config.
func LoadConfig(cfgFile string) (*Config, error) {
	v := viper.New()

	defaults := DefaultConfig()
	v.SetDefault("library_path", defaults.LibraryPath)
	v.SetDefault("output_path", defaults.OutputPath)
	v.SetDefault("scratch_path", defaults.ScratchPath)
	v.SetDefault("database_path", defaults.DatabasePath)
	v.SetDefault("min_savings_percent", defaults.MinSavingsPercent)
	v.SetDefault("target_codec", defaults.TargetCodec)
	v.SetDefault("encoder_preset", defaults.EncoderPreset)
	v.SetDefault("target_crf", defaults.TargetCRF)
	v.SetDefault("crf_tiers.sd", defaults.CRFTiers.SD)
	v.SetDefault("crf_tiers.hd", defaults.CRFTiers.HD)
	v.SetDefault("crf_tiers.fhd", defaults.CRFTiers.FHD)
	v.SetDefault("crf_tiers.uhd", defaults.CRFTiers.UHD)
	v.SetDefault("target_vmaf", defaults.TargetVMAF)
	v.SetDefault("hardware_accel", defaults.HardwareAccel)
	v.SetDefault("deinterlace", defaults.Deinterlace)
	v.SetDefault("reprocess", defaults.Reprocess)
	v.SetDefault("scan_schedule", defaults.ScanSchedule)
	v.SetDefault("watch_realtime", defaults.WatchRealtime)
	v.SetDefault("debounce_seconds", defaults.DebounceSeconds)
	v.SetDefault("preserve_timestamps", defaults.PreserveTimestamps)
	v.SetDefault("keep_backup", defaults.KeepBackup)
	v.SetDefault("excluded_paths", defaults.ExcludedPaths)
	v.SetDefault("video_extensions", defaults.VideoExtensions)
	v.SetDefault("max_workers", defaults.MaxWorkers)

	// Environment variable overrides: VIDOPT_LIBRARY_PATH, VIDOPT_TARGET_CODEC, etc.
	v.SetEnvPrefix("VIDOPT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("failed to read config file %q: %w", cfgFile, err)
		}
	} else {
		// Look for config.yaml in working directory or /data
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("./data")
		v.AddConfigPath("/data")
		_ = v.ReadInConfig() // ignore if not found; defaults + env vars are valid
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse configuration: %w", err)
	}

	// Backward compatibility alias: if legacy "force" was set in YAML/env, honor it
	if v.IsSet("force") && !v.IsSet("reprocess") {
		cfg.Reprocess = v.GetBool("force")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// CleanCLIPath sanitizes command-line path inputs, trimming surrounding quotes,
// trailing slashes, whitespace, and resolving PowerShell/Windows argument-escaping
// artifacts (such as 'C:\path\' turning into C:\path" when passed to executables).
func CleanCLIPath(p string) string {
	if p == "" {
		return ""
	}
	p = strings.TrimSpace(p)
	p = strings.Trim(p, "\"'`")
	p = strings.TrimRight(p, "\"'\t\r\n ")

	// Trim trailing slashes or backslashes, while preserving roots like "C:\" or "/"
	for len(p) > 1 && (strings.HasSuffix(p, "/") || strings.HasSuffix(p, "\\")) {
		if len(p) == 3 && p[1] == ':' {
			break
		}
		p = p[:len(p)-1]
	}

	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// Validate checks that required paths and configuration fields are sound.
func (c *Config) Validate() error {
	c.LibraryPath = CleanCLIPath(c.LibraryPath)
	if c.OutputPath != "" {
		c.OutputPath = CleanCLIPath(c.OutputPath)
	}
	if c.ScratchPath != "" {
		c.ScratchPath = CleanCLIPath(c.ScratchPath)
	}
	if c.DatabasePath != "" {
		c.DatabasePath = CleanCLIPath(c.DatabasePath)
	}

	if c.LibraryPath == "" {
		return fmt.Errorf("library_path must be specified")
	}
	if c.MinSavingsPercent < 0 || c.MinSavingsPercent > 100 {
		return fmt.Errorf("min_savings_percent must be between 0 and 100")
	}
	if c.TargetVMAF < 0 || c.TargetVMAF > 100 {
		return fmt.Errorf("target_vmaf must be between 0 and 100")
	}
	if c.DebounceSeconds < 0 {
		return fmt.Errorf("debounce_seconds must be non-negative")
	}
	if c.MaxWorkers < 1 {
		c.MaxWorkers = 1
	}

	// Normalize video extensions to lowercase with dot
	for i, ext := range c.VideoExtensions {
		if !strings.HasPrefix(ext, ".") {
			c.VideoExtensions[i] = "." + strings.ToLower(ext)
		} else {
			c.VideoExtensions[i] = strings.ToLower(ext)
		}
	}

	return nil
}

// EnsureDirectories ensures that the scratch and database directories exist.
func (c *Config) EnsureDirectories() error {
	if c.ScratchPath != "" {
		if err := os.MkdirAll(c.ScratchPath, 0755); err != nil {
			return fmt.Errorf("cannot create scratch directory %q: %w", c.ScratchPath, err)
		}
	}
	if c.DatabasePath != "" {
		dbDir := filepath.Dir(c.DatabasePath)
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			return fmt.Errorf("cannot create database directory %q: %w", dbDir, err)
		}
	}
	if c.OutputPath != "" {
		if err := os.MkdirAll(c.OutputPath, 0755); err != nil {
			return fmt.Errorf("cannot create output directory %q: %w", c.OutputPath, err)
		}
	}
	return nil
}

// IsVideoFile returns true if the filename has an eligible video extension.
func (c *Config) IsVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	for _, validExt := range c.VideoExtensions {
		if ext == validExt {
			return true
		}
	}
	return false
}

// IsExcluded returns true if the path contains any excluded directory segment, filename, or suffix.
func (c *Config) IsExcluded(path string) bool {
	normalized := filepath.ToSlash(path)
	base := filepath.Base(normalized)
	for _, excl := range c.ExcludedPaths {
		if excl == "" {
			continue
		}
		if base == excl ||
			strings.Contains(normalized, "/"+excl+"/") ||
			strings.HasSuffix(normalized, "/"+excl) ||
			strings.HasSuffix(normalized, excl) {
			return true
		}
	}
	return false
}

// GetDeinterlaceFilter returns the FFmpeg video filter expression for deinterlacing,
// or an empty string if deinterlacing should not be applied.
func (c *Config) GetDeinterlaceFilter(isInterlaced bool) string {
	mode := strings.TrimSpace(strings.ToLower(c.Deinterlace))
	if mode == "" || mode == "auto" {
		if isInterlaced {
			return "bwdif=mode=0"
		}
		return ""
	}
	if mode == "off" || mode == "false" || mode == "no" || mode == "none" || mode == "0" {
		return ""
	}
	if mode == "on" || mode == "true" || mode == "yes" || mode == "1" {
		return "bwdif=mode=0"
	}
	// Reject filtergraph chain separators or shell metacharacters in custom filter expressions
	if strings.ContainsAny(c.Deinterlace, ";[]'\"`$\n\r") {
		return "bwdif=mode=0"
	}
	// Custom filter expression (e.g. "bwdif=mode=1", "yadif")
	return strings.TrimSpace(c.Deinterlace)
}

// CalculateBaselineCRF determines the optimal CRF baseline based on resolution,
// interlace status, and source characteristics.
func (c *Config) CalculateBaselineCRF(height int, isInterlaced bool, codec string) int {
	if c.TargetCRF > 0 {
		return c.TargetCRF
	}

	tiers := c.CRFTiers
	if tiers.SD <= 0 {
		tiers.SD = 22
	}
	if tiers.HD <= 0 {
		tiers.HD = 24
	}
	if tiers.FHD <= 0 {
		tiers.FHD = 26
	}
	if tiers.UHD <= 0 {
		tiers.UHD = 28
	}

	var baseCRF int
	switch {
	case height > 0 && height <= 576:
		baseCRF = tiers.SD
	case height > 576 && height <= 720:
		baseCRF = tiers.HD
	case height > 720 && height <= 1080:
		baseCRF = tiers.FHD
	case height > 1080:
		baseCRF = tiers.UHD
	default:
		baseCRF = tiers.FHD
	}

	// For analog tape / DV / interlaced sources, lower CRF by 2 for extra detail retention
	if isInterlaced || strings.ToLower(codec) == "dvvideo" {
		if baseCRF > 18 {
			baseCRF -= 2
		}
	}

	return baseCRF
}

// GetCRFCandidates returns the candidate CRFs to test during adaptive pre-flight sampling.
func (c *Config) GetCRFCandidates(height int, isInterlaced bool, codec string) []int {
	if c.TargetCRF > 0 {
		return []int{c.TargetCRF}
	}

	base := c.CalculateBaselineCRF(height, isInterlaced, codec)
	cands := []int{base - 2, base, base + 2}
	var res []int
	for _, v := range cands {
		if v < 16 {
			v = 16
		} else if v > 36 {
			v = 36
		}
		found := false
		for _, ex := range res {
			if ex == v {
				found = true
				break
			}
		}
		if !found {
			res = append(res, v)
		}
	}
	return res
}
