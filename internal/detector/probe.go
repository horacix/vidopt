package detector

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// FFProbeOutput matches ffprobe's JSON output structure.
type FFProbeOutput struct {
	Streams []FFProbeStream `json:"streams"`
	Format  FFProbeFormat   `json:"format"`
}

type FFProbeStream struct {
	Index      int    `json:"index"`
	CodecName  string `json:"codec_name"`
	CodecType  string `json:"codec_type"` // "video", "audio", "subtitle"
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	BitRate    string `json:"bit_rate,omitempty"`
	RFrameRate string `json:"r_frame_rate,omitempty"`
	PixFmt     string `json:"pix_fmt,omitempty"`
	Duration   string `json:"duration,omitempty"`
	FieldOrder string `json:"field_order,omitempty"`
}

type FFProbeFormat struct {
	Filename   string `json:"filename"`
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	Size       string `json:"size"`
	BitRate    string `json:"bit_rate"`
}

// MediaInfo contains parsed and normalized metadata for a video file.
type MediaInfo struct {
	FilePath       string  `json:"file_path"`
	FormatName     string  `json:"format_name"`
	Duration       float64 `json:"duration_seconds"`
	Bitrate        int64   `json:"bitrate"`
	Size           int64   `json:"size_bytes"`
	VideoCodec     string  `json:"video_codec"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	Resolution     string  `json:"resolution"`
	FrameRate      float64 `json:"frame_rate"`
	PixelFormat    string  `json:"pixel_format"`
	FieldOrder     string  `json:"field_order"`
	IsInterlaced   bool    `json:"is_interlaced"`
	AudioTracks    int     `json:"audio_tracks"`
	SubtitleTracks int     `json:"subtitle_tracks"`
}

// ProbeMedia executes ffprobe on the given file path and returns MediaInfo.
func ProbeMedia(filePath string) (*MediaInfo, error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		filePath,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed on %q: %w", filePath, err)
	}

	return ParseProbeJSON(output, filePath)
}

// ParseProbeJSON converts raw ffprobe JSON output into MediaInfo.
// Separated to enable unit testing with mock ffprobe payloads.
func ParseProbeJSON(jsonData []byte, filePath string) (*MediaInfo, error) {
	var probe FFProbeOutput
	if err := json.Unmarshal(jsonData, &probe); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ffprobe JSON: %w", err)
	}

	info := &MediaInfo{
		FilePath:   filePath,
		FormatName: probe.Format.FormatName,
	}

	// Parse duration
	if dur, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil {
		info.Duration = dur
	}

	// Parse overall file size
	if size, err := strconv.ParseInt(probe.Format.Size, 10, 64); err == nil {
		info.Size = size
	}

	// Parse overall bitrate
	if br, err := strconv.ParseInt(probe.Format.BitRate, 10, 64); err == nil {
		info.Bitrate = br
	}

	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			// Select first video stream
			if info.VideoCodec == "" {
				info.VideoCodec = strings.ToLower(stream.CodecName)
				info.Width = stream.Width
				info.Height = stream.Height
				if stream.Width > 0 && stream.Height > 0 {
					info.Resolution = fmt.Sprintf("%dx%d", stream.Width, stream.Height)
				}
				info.PixelFormat = stream.PixFmt
				info.FrameRate = parseFrameRate(stream.RFrameRate)
				info.FieldOrder = stream.FieldOrder
				info.IsInterlaced = IsInterlacedVideo(info.VideoCodec, stream.FieldOrder)

				// Fallback duration if format duration was missing
				if info.Duration == 0 {
					if dur, err := strconv.ParseFloat(stream.Duration, 64); err == nil {
						info.Duration = dur
					}
				}
			}
		case "audio":
			info.AudioTracks++
		case "subtitle":
			info.SubtitleTracks++
		}
	}

	if info.VideoCodec == "" {
		return nil, fmt.Errorf("no video stream found in %q", filePath)
	}

	return info, nil
}

// parseFrameRate handles fraction representations like "24000/1001" or "30/1".
func parseFrameRate(fr string) float64 {
	if fr == "" || fr == "0/0" {
		return 0
	}
	parts := strings.Split(fr, "/")
	if len(parts) == 1 {
		val, _ := strconv.ParseFloat(parts[0], 64)
		return val
	}
	if len(parts) == 2 {
		num, err1 := strconv.ParseFloat(parts[0], 64)
		den, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 == nil && err2 == nil && den > 0 {
			return num / den
		}
	}
	return 0
}

// IsInterlacedVideo determines if a video is interlaced based on codec and field order.
func IsInterlacedVideo(codec, fieldOrder string) bool {
	c := strings.ToLower(codec)
	fo := strings.ToLower(fieldOrder)
	if fo == "tt" || fo == "bb" || fo == "tb" || fo == "bt" {
		return true
	}
	// DV video is always interlaced (IEC 61834 / SMPTE 314M standard)
	if c == "dvvideo" {
		return true
	}
	return false
}
