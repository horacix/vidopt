package detector

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// HardwareCapabilities describes the encoders and hardware acceleration available in FFmpeg.
type HardwareCapabilities struct {
	AvailableHwaccels []string
	SupportedEncoders map[string]bool
	HasDriRenderNode  bool // Intel QSV / VAAPI on Linux
	HasCudaGPU        bool // NVIDIA NVENC on Linux & Windows
}

// DetectHardware queries FFmpeg for available hardware accelerators and encoders.
func DetectHardware() (*HardwareCapabilities, error) {
	caps := &HardwareCapabilities{
		SupportedEncoders: make(map[string]bool),
	}

	// 1. Check for Linux /dev/dri render node (standard for Intel QSV / VAAPI on NAS)
	if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
		caps.HasDriRenderNode = true
	} else if _, err := os.Stat("/dev/dri"); err == nil {
		caps.HasDriRenderNode = true
	}

	// 2. Check for NVIDIA GPU presence (nvidia-smi works on Windows and Linux workstations)
	if err := exec.Command("nvidia-smi").Run(); err == nil {
		caps.HasCudaGPU = true
	}

	// 3. Check ffmpeg -hwaccels
	hwCmd := exec.Command("ffmpeg", "-hwaccels")
	var hwOut bytes.Buffer
	hwCmd.Stdout = &hwOut
	if err := hwCmd.Run(); err == nil {
		lines := strings.Split(hwOut.String(), "\n")
		for _, line := range lines {
			accel := strings.TrimSpace(line)
			if accel != "" && !strings.Contains(accel, "Hardware acceleration methods") {
				caps.AvailableHwaccels = append(caps.AvailableHwaccels, accel)
				if accel == "cuda" {
					caps.HasCudaGPU = true
				}
			}
		}
	}

	// 4. Check ffmpeg -encoders
	encCmd := exec.Command("ffmpeg", "-encoders")
	var encOut bytes.Buffer
	encCmd.Stdout = &encOut
	if err := encCmd.Run(); err == nil {
		output := encOut.String()
		checkEncoders := []string{
			// NVIDIA NVENC (Workstation RTX 40/50-series, etc.)
			"av1_nvenc", "hevc_nvenc", "h264_nvenc",
			// Intel Quick Sync (NAS Intel N-series, Core, Arc)
			"av1_qsv", "hevc_qsv", "h264_qsv",
			// VAAPI
			"av1_vaapi", "hevc_vaapi", "h264_vaapi",
			// Software CPU encoders
			"libsvtav1", "libx265", "libx264",
		}
		for _, enc := range checkEncoders {
			if strings.Contains(output, enc) {
				if isHardwareEncoder(enc) {
					if verifyHardwareEncoder(enc, caps) {
						caps.SupportedEncoders[enc] = true
					}
				} else {
					caps.SupportedEncoders[enc] = true
				}
			}
		}
	}

	return caps, nil
}

// EncoderSelection encapsulates the chosen encoder, hardware device arguments, and whether it is HW-accelerated.
type EncoderSelection struct {
	CodecName   string   // "av1" or "hevc"
	EncoderName string   // e.g. "av1_nvenc", "av1_qsv", "hevc_qsv", "libsvtav1", "libx265"
	HwAccelArgs []string // e.g. ["-hwaccel", "cuda"], ["-init_hw_device", "qsv=hw..."]
	IsHardware  bool
	ExtraParams []string // default encoder-specific flags
}

// SelectOptimalEncoder selects the best encoder based on capabilities, preferences, and hardware.
func SelectOptimalEncoder(caps *HardwareCapabilities, targetCodec string, hwPref string) EncoderSelection {
	targetCodec = strings.ToLower(targetCodec)
	hwPref = strings.ToLower(hwPref)

	hasIntelGPU := (caps != nil && caps.HasDriRenderNode)
	hasNvidiaGPU := (caps != nil && caps.HasCudaGPU)

	allowNVENC := (hwPref == "nvenc") || (hwPref == "auto" && hasNvidiaGPU)
	allowQSV := (hwPref == "qsv") || (hwPref == "auto" && hasIntelGPU)
	allowVAAPI := (hwPref == "vaapi") || (hwPref == "auto" && hasIntelGPU)
	forceCPU := (hwPref == "cpu")

	// Helper to check if encoder is available
	hasEnc := func(name string) bool {
		return caps != nil && caps.SupportedEncoders[name]
	}

	// 1. AUTO mode:
	if targetCodec == "" || targetCodec == "auto" {
		if !forceCPU {
			// Workstation NVIDIA RTX 40-series: Prioritize av1_nvenc if supported
			if allowNVENC && hasEnc("av1_nvenc") {
				return nvencEncoder("av1", "av1_nvenc")
			}
			// Intel Arc / Meteor Lake / Lunar Lake: av1_qsv
			if allowQSV && hasEnc("av1_qsv") {
				return qsvEncoder("av1", "av1_qsv")
			}
			// Workstation NVIDIA RTX 20/30/40-series: hevc_nvenc
			if allowNVENC && hasEnc("hevc_nvenc") {
				return nvencEncoder("hevc", "hevc_nvenc")
			}
			// Intel N-series (N100, N95) & Synology: hevc_qsv
			if allowQSV && hasEnc("hevc_qsv") {
				return qsvEncoder("hevc", "hevc_qsv")
			}
			// VAAPI fallbacks
			if allowVAAPI && hasEnc("av1_vaapi") {
				return vaapiEncoder("av1", "av1_vaapi")
			}
			if allowVAAPI && hasEnc("hevc_vaapi") {
				return vaapiEncoder("hevc", "hevc_vaapi")
			}
		}
		// Software fallbacks: prefer SVT-AV1
		if hasEnc("libsvtav1") {
			return cpuSvtAv1Encoder()
		}
		if hasEnc("libx265") {
			return cpuX265Encoder()
		}
		// Default fallback
		return cpuSvtAv1Encoder()
	}

	// 2. AV1 target:
	if targetCodec == "av1" {
		if !forceCPU {
			if allowNVENC && hasEnc("av1_nvenc") {
				return nvencEncoder("av1", "av1_nvenc")
			}
			if allowQSV && hasEnc("av1_qsv") {
				return qsvEncoder("av1", "av1_qsv")
			}
			if allowVAAPI && hasEnc("av1_vaapi") {
				return vaapiEncoder("av1", "av1_vaapi")
			}
		}
		return cpuSvtAv1Encoder()
	}

	// 3. HEVC target:
	if targetCodec == "hevc" || targetCodec == "h265" {
		if !forceCPU {
			if allowNVENC && hasEnc("hevc_nvenc") {
				return nvencEncoder("hevc", "hevc_nvenc")
			}
			if allowQSV && hasEnc("hevc_qsv") {
				return qsvEncoder("hevc", "hevc_qsv")
			}
			if allowVAAPI && hasEnc("hevc_vaapi") {
				return vaapiEncoder("hevc", "hevc_vaapi")
			}
		}
		return cpuX265Encoder()
	}

	// Default fallback to SVT-AV1
	return cpuSvtAv1Encoder()
}

func nvencEncoder(codec, encoderName string) EncoderSelection {
	return EncoderSelection{
		CodecName:   codec,
		EncoderName: encoderName,
		IsHardware:  true,
		HwAccelArgs: []string{"-hwaccel", "cuda"},
	}
}

func qsvEncoder(codec, encoderName string) EncoderSelection {
	sel := EncoderSelection{
		CodecName:   codec,
		EncoderName: encoderName,
		IsHardware:  true,
		HwAccelArgs: []string{"-init_hw_device", "qsv=hw", "-filter_hw_device", "hw"},
	}
	if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
		sel.HwAccelArgs = []string{"-init_hw_device", "qsv=hw,child_device=/dev/dri/renderD128", "-filter_hw_device", "hw"}
	}
	return sel
}

func vaapiEncoder(codec, encoderName string) EncoderSelection {
	dev := "/dev/dri/renderD128"
	if _, err := os.Stat(dev); err != nil {
		dev = ""
	}
	args := []string{}
	if dev != "" {
		args = []string{"-vaapi_device", dev}
	}
	return EncoderSelection{
		CodecName:   codec,
		EncoderName: encoderName,
		IsHardware:  true,
		HwAccelArgs: args,
	}
}

func cpuSvtAv1Encoder() EncoderSelection {
	return EncoderSelection{
		CodecName:   "av1",
		EncoderName: "libsvtav1",
		IsHardware:  false,
		ExtraParams: []string{"-preset", "6"},
	}
}

func cpuX265Encoder() EncoderSelection {
	return EncoderSelection{
		CodecName:   "hevc",
		EncoderName: "libx265",
		IsHardware:  false,
		ExtraParams: []string{"-preset", "medium"},
	}
}

func isHardwareEncoder(enc string) bool {
	return strings.HasSuffix(enc, "_nvenc") || strings.HasSuffix(enc, "_qsv") || strings.HasSuffix(enc, "_vaapi")
}

func verifyHardwareEncoder(enc string, caps *HardwareCapabilities) bool {
	var hwArgs []string
	pixFmt := "yuv420p"

	if strings.HasSuffix(enc, "_nvenc") {
		if !caps.HasCudaGPU {
			return false
		}
		hwArgs = []string{"-hwaccel", "cuda"}
	} else if strings.HasSuffix(enc, "_qsv") {
		if !caps.HasDriRenderNode {
			hwArgs = []string{"-init_hw_device", "qsv=hw", "-filter_hw_device", "hw"}
		} else {
			dev := "/dev/dri/renderD128"
			if _, err := os.Stat(dev); err == nil {
				hwArgs = []string{"-init_hw_device", "qsv=hw,child_device=" + dev, "-filter_hw_device", "hw"}
			} else {
				hwArgs = []string{"-init_hw_device", "qsv=hw", "-filter_hw_device", "hw"}
			}
		}
		pixFmt = "nv12"
	} else if strings.HasSuffix(enc, "_vaapi") {
		if !caps.HasDriRenderNode {
			return false
		}
		dev := "/dev/dri/renderD128"
		if _, err := os.Stat(dev); err == nil {
			hwArgs = []string{"-vaapi_device", dev}
		}
		pixFmt = "nv12"
	}

	args := []string{"-y", "-hide_banner", "-loglevel", "error"}
	if len(hwArgs) > 0 {
		args = append(args, hwArgs...)
	}
	args = append(args,
		"-f", "lavfi",
		"-i", "nullsrc=s=256x256:d=0.04",
		"-pix_fmt", pixFmt,
		"-c:v", enc,
		"-f", "null",
		"-",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	return cmd.Run() == nil
}

// ChoosePixelFormat selects the optimal output pixel format for encoding
// based on the source video's pixel format and the target encoder.
// It ensures compatibility with hardware encoders (e.g., NVENC, QSV, VAAPI)
// and player ecosystem compatibility (standard 8-bit YUV 4:2:0 or 10-bit YUV 4:2:0/P010).
func ChoosePixelFormat(srcPixFmt, encoderName string) string {
	src := strings.ToLower(srcPixFmt)
	enc := strings.ToLower(encoderName)

	is10Bit := strings.Contains(src, "10") ||
		strings.Contains(src, "12") ||
		strings.Contains(src, "16") ||
		strings.Contains(src, "p010")

	if is10Bit {
		// Hardware encoders prefer P010 semi-planar
		if strings.Contains(enc, "nvenc") || strings.Contains(enc, "qsv") || strings.Contains(enc, "vaapi") {
			return "p010le"
		}
		// Software encoders (SVT-AV1, x265) use planar yuv420p10le
		return "yuv420p10le"
	}

	// 8-bit QSV and VAAPI prefer NV12
	if strings.Contains(enc, "qsv") || strings.Contains(enc, "vaapi") {
		return "nv12"
	}

	// Standard 8-bit YUV 4:2:0 for NVENC and software encoders
	return "yuv420p"
}
