package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MinSavingsPercent != 20.0 {
		t.Errorf("expected default min savings 20.0, got %f", cfg.MinSavingsPercent)
	}
	if cfg.TargetCodec != "auto" {
		t.Errorf("expected default target codec 'auto', got %s", cfg.TargetCodec)
	}
	if cfg.TargetVMAF != 93.0 {
		t.Errorf("expected default target VMAF 93.0, got %f", cfg.TargetVMAF)
	}
	if !cfg.PreserveTimestamps {
		t.Errorf("expected preserve_timestamps default to be true")
	}
	if !cfg.KeepBackup {
		t.Errorf("expected keep_backup default to be true")
	}
	if cfg.WatchRealtime {
		t.Errorf("expected watch_realtime default to be false")
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected default config to be valid, got: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LibraryPath = ""
	if err := cfg.Validate(); err == nil {
		t.Errorf("expected error for empty library_path, got nil")
	}

	cfg = DefaultConfig()
	cfg.MinSavingsPercent = 150.0
	if err := cfg.Validate(); err == nil {
		t.Errorf("expected error for min_savings > 100, got nil")
	}

	cfg = DefaultConfig()
	cfg.TargetVMAF = 105.0
	if err := cfg.Validate(); err == nil {
		t.Errorf("expected error for target_vmaf > 100, got nil")
	}
}

func TestIsVideoFileAndExcluded(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.IsVideoFile("movie.mkv") {
		t.Errorf("expected .mkv to be recognized as video file")
	}
	if !cfg.IsVideoFile("clip.MP4") {
		t.Errorf("expected .MP4 (uppercase) to be recognized as video file")
	}
	if cfg.IsVideoFile("subtitles.srt") {
		t.Errorf("expected .srt to NOT be recognized as video file")
	}

	if !cfg.IsExcluded("/library/@eaDir/metadata.json") {
		t.Errorf("expected @eaDir path to be excluded")
	}
	if !cfg.IsExcluded("/library/#recycle/old.mkv") {
		t.Errorf("expected #recycle path to be excluded")
	}
	if cfg.IsExcluded("/library/movies/action/movie.mkv") {
		t.Errorf("expected valid movie path to NOT be excluded")
	}
	if cfg.IsExcluded("/library/movies/movie.tmp.1080p.mkv") {
		t.Errorf("expected filename containing .tmp in middle to NOT be falsely excluded")
	}
}

func TestEnvOverride(t *testing.T) {
	_ = os.Setenv("VIDOPT_TARGET_CODEC", "av1")
	_ = os.Setenv("VIDOPT_MIN_SAVINGS_PERCENT", "35.5")
	defer func() {
		_ = os.Unsetenv("VIDOPT_TARGET_CODEC")
		_ = os.Unsetenv("VIDOPT_MIN_SAVINGS_PERCENT")
	}()

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.TargetCodec != "av1" {
		t.Errorf("expected target codec 'av1' from env, got %s", cfg.TargetCodec)
	}
	if cfg.MinSavingsPercent != 35.5 {
		t.Errorf("expected min savings 35.5 from env, got %f", cfg.MinSavingsPercent)
	}
}

func TestCleanCLIPath(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{`P:\Videos\IPC C1"`, `P:\Videos\IPC C1`},
		{`'P:\Videos\IPC C1\'`, `P:\Videos\IPC C1`},
		{`"P:\Videos\IPC C1\"`, `P:\Videos\IPC C1`},
		{`  "C:\Movies\"  `, `C:\Movies`},
		{`/var/media/`, `/var/media`},
		{`""`, ``},
	}
	for _, tc := range cases {
		actual := CleanCLIPath(tc.input)
		exp := tc.expected
		if exp != "" {
			exp = filepath.Clean(exp)
		}
		if actual != exp {
			t.Errorf("CleanCLIPath(%q) = %q; want %q", tc.input, actual, exp)
		}
	}
}

func TestGetDeinterlaceFilter(t *testing.T) {
	cfg := DefaultConfig() // Deinterlace: "auto"

	if cfg.GetDeinterlaceFilter(false) != "" {
		t.Errorf("expected empty filter for progressive video with auto")
	}
	if cfg.GetDeinterlaceFilter(true) != "bwdif=mode=0" {
		t.Errorf("expected bwdif=mode=0 for interlaced video with auto")
	}

	cfg.Deinterlace = "off"
	if cfg.GetDeinterlaceFilter(true) != "" {
		t.Errorf("expected empty filter when deinterlace is off")
	}

	cfg.Deinterlace = "on"
	if cfg.GetDeinterlaceFilter(false) != "bwdif=mode=0" {
		t.Errorf("expected bwdif=mode=0 when deinterlace is on")
	}

	cfg.Deinterlace = "bwdif=mode=1"
	if cfg.GetDeinterlaceFilter(false) != "bwdif=mode=1" {
		t.Errorf("expected custom filter expression when specified")
	}

	cfg.Deinterlace = "bwdif=mode=0;movie=/etc/passwd"
	if cfg.GetDeinterlaceFilter(true) != "bwdif=mode=0" {
		t.Errorf("expected unsafe filtergraph expression to be sanitized to bwdif=mode=0")
	}
}

func TestCalculateBaselineCRF(t *testing.T) {
	cfg := DefaultConfig() // TargetCRF: 0, SD: 22, HD: 24, FHD: 26, UHD: 28

	// SD progressive
	if b := cfg.CalculateBaselineCRF(480, false, "h264"); b != 22 {
		t.Errorf("expected 480p progressive baseline 22, got %d", b)
	}

	// SD interlaced (should apply -2 penalty for extra quality)
	if b := cfg.CalculateBaselineCRF(480, true, "h264"); b != 20 {
		t.Errorf("expected 480i interlaced baseline 20, got %d", b)
	}

	// DV video (codec dvvideo automatically gets -2)
	if b := cfg.CalculateBaselineCRF(480, false, "dvvideo"); b != 20 {
		t.Errorf("expected dvvideo baseline 20, got %d", b)
	}

	// 720p HD
	if b := cfg.CalculateBaselineCRF(720, false, "h264"); b != 24 {
		t.Errorf("expected 720p baseline 24, got %d", b)
	}

	// 1080p FHD
	if b := cfg.CalculateBaselineCRF(1080, false, "h264"); b != 26 {
		t.Errorf("expected 1080p baseline 26, got %d", b)
	}

	// 4K UHD
	if b := cfg.CalculateBaselineCRF(2160, false, "hevc"); b != 28 {
		t.Errorf("expected 4K baseline 28, got %d", b)
	}

	// Fixed TargetCRF override
	cfg.TargetCRF = 21
	if b := cfg.CalculateBaselineCRF(2160, false, "hevc"); b != 21 {
		t.Errorf("expected fixed TargetCRF 21, got %d", b)
	}
}

func TestGetCRFCandidates(t *testing.T) {
	cfg := DefaultConfig()

	// 480i interlaced DV -> baseline 20 -> candidates [18, 20, 22]
	dvCands := cfg.GetCRFCandidates(480, true, "dvvideo")
	if len(dvCands) != 3 || dvCands[0] != 18 || dvCands[1] != 20 || dvCands[2] != 22 {
		t.Errorf("expected DV candidates [18, 20, 22], got %v", dvCands)
	}

	// Fixed TargetCRF -> single candidate
	cfg.TargetCRF = 24
	fixedCands := cfg.GetCRFCandidates(480, true, "dvvideo")
	if len(fixedCands) != 1 || fixedCands[0] != 24 {
		t.Errorf("expected fixed candidates [24], got %v", fixedCands)
	}
}
