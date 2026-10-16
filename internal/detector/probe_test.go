package detector

import (
	"testing"
)

func TestParseProbeJSON(t *testing.T) {
	mockJSON := []byte(`{
		"streams": [
			{
				"index": 0,
				"codec_name": "h264",
				"codec_type": "video",
				"width": 1920,
				"height": 1080,
				"r_frame_rate": "24000/1001",
				"pix_fmt": "yuv420p",
				"duration": "7200.5"
			},
			{
				"index": 1,
				"codec_name": "aac",
				"codec_type": "audio"
			},
			{
				"index": 2,
				"codec_name": "ac3",
				"codec_type": "audio"
			},
			{
				"index": 3,
				"codec_name": "subrip",
				"codec_type": "subtitle"
			}
		],
		"format": {
			"filename": "/library/sample.mkv",
			"format_name": "matroska,webm",
			"duration": "7200.5",
			"size": "5368709120",
			"bit_rate": "5965232"
		}
	}`)

	info, err := ParseProbeJSON(mockJSON, "/library/sample.mkv")
	if err != nil {
		t.Fatalf("failed to parse probe JSON: %v", err)
	}

	if info.VideoCodec != "h264" {
		t.Errorf("expected video codec h264, got %s", info.VideoCodec)
	}
	if info.Width != 1920 || info.Height != 1080 {
		t.Errorf("expected 1920x1080, got %dx%d", info.Width, info.Height)
	}
	if info.Resolution != "1920x1080" {
		t.Errorf("expected resolution 1920x1080, got %s", info.Resolution)
	}
	if info.AudioTracks != 2 {
		t.Errorf("expected 2 audio tracks, got %d", info.AudioTracks)
	}
	if info.SubtitleTracks != 1 {
		t.Errorf("expected 1 subtitle track, got %d", info.SubtitleTracks)
	}
	if info.Duration != 7200.5 {
		t.Errorf("expected duration 7200.5, got %f", info.Duration)
	}
	if info.Size != 5368709120 {
		t.Errorf("expected size 5368709120, got %d", info.Size)
	}
	if info.FrameRate < 23.97 || info.FrameRate > 23.98 {
		t.Errorf("expected ~23.976 fps, got %f", info.FrameRate)
	}
}

func TestSelectOptimalEncoder(t *testing.T) {
	caps := &HardwareCapabilities{
		HasDriRenderNode: true,
		SupportedEncoders: map[string]bool{
			"hevc_qsv":  true,
			"libsvtav1": true,
			"libx265":   true,
		},
	}

	// 1. Auto mode with QSV available should pick hevc_qsv (ideal for N-series NAS)
	sel := SelectOptimalEncoder(caps, "auto", "auto")
	if sel.EncoderName != "hevc_qsv" {
		t.Errorf("expected hevc_qsv in auto mode, got %s", sel.EncoderName)
	}
	if !sel.IsHardware {
		t.Errorf("expected isHardware to be true for hevc_qsv")
	}

	// 2. AV1 target when only SVT-AV1 CPU encoder available
	sel = SelectOptimalEncoder(caps, "av1", "auto")
	if sel.EncoderName != "libsvtav1" {
		t.Errorf("expected libsvtav1, got %s", sel.EncoderName)
	}
	if sel.IsHardware {
		t.Errorf("expected isHardware to be false for libsvtav1")
	}

	// 3. Force CPU
	sel = SelectOptimalEncoder(caps, "hevc", "cpu")
	if sel.EncoderName != "libx265" {
		t.Errorf("expected libx265 with force CPU, got %s", sel.EncoderName)
	}

	// 4. Workstation with NVIDIA GPU (av1_nvenc)
	nvidiaCaps := &HardwareCapabilities{
		HasCudaGPU: true,
		SupportedEncoders: map[string]bool{
			"av1_nvenc":  true,
			"hevc_nvenc": true,
			"libsvtav1":  true,
		},
	}
	sel = SelectOptimalEncoder(nvidiaCaps, "auto", "auto")
	if sel.EncoderName != "av1_nvenc" {
		t.Errorf("expected av1_nvenc on workstation with NVIDIA GPU, got %s", sel.EncoderName)
	}
	if !sel.IsHardware {
		t.Errorf("expected isHardware to be true for av1_nvenc")
	}

	// 5. Workstation with explicit nvenc hardware flag
	sel = SelectOptimalEncoder(nvidiaCaps, "hevc", "nvenc")
	if sel.EncoderName != "hevc_nvenc" {
		t.Errorf("expected hevc_nvenc with --hw-accel nvenc, got %s", sel.EncoderName)
	}
}

func TestChoosePixelFormat(t *testing.T) {
	tests := []struct {
		srcPixFmt string
		encoder   string
		expected  string
	}{
		{"yuv411p", "av1_nvenc", "yuv420p"},
		{"yuv422p", "av1_nvenc", "yuv420p"},
		{"yuv420p", "av1_nvenc", "yuv420p"},
		{"yuvj420p", "libsvtav1", "yuv420p"},
		{"yuv411p", "hevc_qsv", "nv12"},
		{"yuv420p", "hevc_vaapi", "nv12"},
		{"yuv420p10le", "av1_nvenc", "p010le"},
		{"yuv420p10le", "hevc_nvenc", "p010le"},
		{"yuv420p10le", "av1_qsv", "p010le"},
		{"yuv420p10le", "libsvtav1", "yuv420p10le"},
		{"yuv420p10le", "libx265", "yuv420p10le"},
		{"", "av1_nvenc", "yuv420p"},
	}

	for _, tc := range tests {
		actual := ChoosePixelFormat(tc.srcPixFmt, tc.encoder)
		if actual != tc.expected {
			t.Errorf("ChoosePixelFormat(%q, %q) = %q; want %q", tc.srcPixFmt, tc.encoder, actual, tc.expected)
		}
	}
}

func TestIsInterlacedVideo(t *testing.T) {
	tests := []struct {
		codec      string
		fieldOrder string
		expected   bool
	}{
		{"dvvideo", "", true},
		{"dvvideo", "unknown", true},
		{"h264", "progressive", false},
		{"h264", "tt", true},
		{"mpeg2video", "bb", true},
		{"hevc", "", false},
	}

	for _, tc := range tests {
		actual := IsInterlacedVideo(tc.codec, tc.fieldOrder)
		if actual != tc.expected {
			t.Errorf("IsInterlacedVideo(%q, %q) = %v; want %v", tc.codec, tc.fieldOrder, actual, tc.expected)
		}
	}
}
