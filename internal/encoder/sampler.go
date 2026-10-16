package encoder

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/horacix/vidopt/internal/detector"
)

const (
	// DefaultMinSearchCRF is the lowest CRF (highest quality ceiling) tested during perceptual search.
	DefaultMinSearchCRF = 14
	// DefaultMaxSearchCRF is the highest CRF (maximum compression floor) tested during perceptual search.
	DefaultMaxSearchCRF = 40
	// MaxCRFSearchProbes limits the number of sample encoding iterations during root-finding.
	MaxCRFSearchProbes = 5
)

var (
	vmafScoreRegex = regexp.MustCompile(`VMAF score:\s*([0-9]+(?:\.[0-9]+)?)`)
	ssimAllRegex   = regexp.MustCompile(`All:\s*([0-9]+(?:\.[0-9]+)?)`)
)

// SampleClip represents a time window within a video.
type SampleClip struct {
	StartTime float64
	Duration  float64
}

// SamplePlan contains the list of sample clips to extract and test.
type SamplePlan struct {
	Clips               []SampleClip
	TotalSampleDuration float64
}

// CalculateSamplePlan determines sample windows adaptively based on video duration.
// Inspired by ab-av1's sampling strategy, with robust short clip handling.
func CalculateSamplePlan(duration float64) SamplePlan {
	if duration <= 0 {
		// Fallback if duration is unknown: sample first 10s
		return SamplePlan{
			Clips:               []SampleClip{{StartTime: 0, Duration: 10}},
			TotalSampleDuration: 10,
		}
	}

	// 1. Very short clips (<= 30 seconds): test the whole video
	if duration <= 30 {
		return SamplePlan{
			Clips:               []SampleClip{{StartTime: 0, Duration: duration}},
			TotalSampleDuration: duration,
		}
	}

	// 2. Medium clips (30 to 90 seconds): 2 clips of 5 seconds each at 25% and 75%
	if duration <= 90 {
		c1Start := duration * 0.25
		c2Start := duration * 0.75
		clipDur := 5.0
		if c1Start+clipDur > duration {
			clipDur = math.Max(1.0, duration-c1Start)
		}
		return SamplePlan{
			Clips: []SampleClip{
				{StartTime: c1Start, Duration: clipDur},
				{StartTime: c2Start, Duration: clipDur},
			},
			TotalSampleDuration: clipDur * 2,
		}
	}

	// 3. Standard videos (> 90 seconds): 3 clips of 10 seconds each at 20%, 50%, and 80%
	clipDur := 10.0
	c1 := duration * 0.20
	c2 := duration * 0.50
	c3 := duration * 0.80

	return SamplePlan{
		Clips: []SampleClip{
			{StartTime: c1, Duration: clipDur},
			{StartTime: c2, Duration: clipDur},
			{StartTime: c3, Duration: clipDur},
		},
		TotalSampleDuration: clipDur * 3,
	}
}

// SampleAttempt records a single CRF probe iteration during perceptual quality search.
type SampleAttempt struct {
	CRF              int     `json:"crf"`
	VMAFScore        float64 `json:"vmaf_score"`
	QualityMetric    string  `json:"quality_metric"`
	SavingsPercent   float64 `json:"savings_percent"`
	EstimatedBitrate int64   `json:"estimated_bitrate"`
	QualityMet       bool    `json:"quality_met"`
}

// SampleResult holds the outcome of a sample compression and perceptual quality test.
type SampleResult struct {
	CRF              int             `json:"crf"`
	VMAFScore        float64         `json:"vmaf_score"`
	QualityMetric    string          `json:"quality_metric"`
	OriginalBytes    int64           `json:"original_bytes"`
	TranscodedBytes  int64           `json:"transcoded_bytes"`
	SavingsRatio     float64         `json:"savings_ratio"`
	SavingsPercent   float64         `json:"savings_percent"`
	EstimatedBitrate int64           `json:"estimated_bitrate"`
	QualityMet       bool            `json:"quality_met"`
	Qualifies        bool            `json:"qualifies"`
	History          []SampleAttempt `json:"history,omitempty"`
}

// Sampler executes sample extractions, perceptual quality scoring, and test encodings in the scratch directory.
type Sampler struct {
	ScratchDir string
}

// NewSampler creates a new Sampler instance.
func NewSampler(scratchDir string) *Sampler {
	return &Sampler{ScratchDir: scratchDir}
}

// EvaluateSavings runs adaptive sample encodings to find the highest compression (highest CRF)
// that preserves perceived visual quality (VMAF >= targetVMAF), and then verifies whether that
// optimal CRF meets minSavingsPercent.
func (s *Sampler) EvaluateSavings(
	media *detector.MediaInfo,
	selection detector.EncoderSelection,
	candidates []int,
	preset string,
	minSavingsPercent float64,
	targetVMAF float64,
	deinterlaceFilter string,
) (*SampleResult, error) {
	plan := CalculateSamplePlan(media.Duration)

	if len(candidates) == 0 {
		candidates = []int{24}
	}

	// 1. Dynamic Perceptual Quality Search Mode:
	// When targetVMAF > 0 and dynamic CRF auto-tuning is active (len(candidates) > 1),
	// find the highest CRF (maximum compression) whose perceptual quality >= targetVMAF.
	if targetVMAF > 0 && len(candidates) > 1 {
		seedCRF := candidates[len(candidates)/2]
		return SearchOptimalCRF(
			seedCRF,
			DefaultMinSearchCRF,
			DefaultMaxSearchCRF,
			targetVMAF,
			minSavingsPercent,
			func(crf int) (*SampleResult, error) {
				return s.testCRF(media, selection, plan, crf, preset, minSavingsPercent, targetVMAF, deinterlaceFilter)
			},
		)
	}

	// 2. Fixed CRF or Legacy Threshold Mode:
	var bestResult *SampleResult
	var history []SampleAttempt

	for _, crf := range candidates {
		res, err := s.testCRF(media, selection, plan, crf, preset, minSavingsPercent, targetVMAF, deinterlaceFilter)
		if err != nil {
			return nil, err
		}

		history = append(history, SampleAttempt{
			CRF:              res.CRF,
			VMAFScore:        res.VMAFScore,
			QualityMetric:    res.QualityMetric,
			SavingsPercent:   res.SavingsPercent,
			EstimatedBitrate: res.EstimatedBitrate,
			QualityMet:       res.QualityMet,
		})

		if res.Qualifies {
			bestResult = res
			break
		}

		if bestResult == nil || res.SavingsRatio > bestResult.SavingsRatio {
			bestResult = res
		}
	}

	if bestResult == nil {
		return nil, fmt.Errorf("no valid sample result obtained")
	}

	bestResult.History = history
	return bestResult, nil
}

// SearchOptimalCRF solves the constrained perceptual optimization problem:
//
//	max CRF in [minCRF, maxCRF]  subject to  Quality(CRF) >= targetQuality
//
// using interpolated secant / binary search. Since perceived quality is monotonically
// decreasing with CRF, this converges on the highest-compression perceptually transparent
// CRF in 3-5 sample evaluations.
func SearchOptimalCRF(
	initialCRF int,
	minCRF int,
	maxCRF int,
	targetQuality float64,
	minSavingsPercent float64,
	evalFunc func(crf int) (*SampleResult, error),
) (*SampleResult, error) {
	if minCRF < 1 {
		minCRF = DefaultMinSearchCRF
	}
	if maxCRF < minCRF {
		maxCRF = DefaultMaxSearchCRF
	}
	curCRF := clampInt(initialCRF, minCRF, maxCRF)

	results := make(map[int]*SampleResult)
	var history []SampleAttempt

	lowPass := 0           // Highest CRF tested so far that achieved VMAFScore >= targetQuality
	highFail := maxCRF + 1 // Lowest CRF tested so far that fell below targetQuality
	var bestPass *SampleResult
	var bestOverall *SampleResult

	for probe := 0; probe < MaxCRFSearchProbes; probe++ {
		if _, alreadyTested := results[curCRF]; alreadyTested {
			break
		}

		res, err := evalFunc(curCRF)
		if err != nil {
			return nil, err
		}
		res.QualityMet = (res.VMAFScore >= targetQuality)
		res.Qualifies = res.QualityMet && (res.SavingsPercent >= minSavingsPercent)
		results[curCRF] = res

		history = append(history, SampleAttempt{
			CRF:              res.CRF,
			VMAFScore:        res.VMAFScore,
			QualityMetric:    res.QualityMetric,
			SavingsPercent:   res.SavingsPercent,
			EstimatedBitrate: res.EstimatedBitrate,
			QualityMet:       res.QualityMet,
		})

		if bestOverall == nil || res.VMAFScore > bestOverall.VMAFScore {
			bestOverall = res
		}

		if res.QualityMet {
			if curCRF > lowPass {
				lowPass = curCRF
				bestPass = res
			}
			// Early exit if score is right on the perceptual threshold bull's-eye [target, target + 0.15]
			if res.VMAFScore <= targetQuality+0.15 {
				break
			}
			// If we've bracketed the exact integer boundary, lowPass is the exact maximum CRF
			if lowPass+1 >= highFail || lowPass >= maxCRF {
				break
			}

			// Choose next higher CRF (more compression)
			if highFail <= maxCRF {
				curCRF = interpolateCRF(lowPass, results[lowPass].VMAFScore, highFail, results[highFail].VMAFScore, targetQuality)
			} else {
				// Step upward proportionally to perceptual headroom (~1.2 CRF per VMAF point)
				step := int(math.Round((res.VMAFScore - targetQuality) * 1.2))
				step = clampInt(step, 2, 8)
				curCRF = clampInt(lowPass+step, lowPass+1, maxCRF)
			}
		} else {
			if curCRF < highFail {
				highFail = curCRF
			}
			// If we've bracketed the exact integer boundary or hit minCRF, stop
			if (lowPass > 0 && lowPass+1 >= highFail) || highFail <= minCRF {
				break
			}

			// Choose next lower CRF (higher visual quality)
			if lowPass > 0 {
				curCRF = interpolateCRF(lowPass, results[lowPass].VMAFScore, highFail, results[highFail].VMAFScore, targetQuality)
			} else {
				// Step downward proportionally to quality deficit (~1.2 CRF per VMAF point)
				step := int(math.Round((targetQuality - res.VMAFScore) * 1.2))
				step = clampInt(step, 2, 8)
				curCRF = clampInt(highFail-step, minCRF, highFail-1)
			}
		}
	}

	winner := bestPass
	if winner == nil {
		winner = bestOverall
	}
	if winner == nil {
		return nil, fmt.Errorf("no valid sample result obtained during CRF search")
	}

	winner.History = history
	return winner, nil
}

// interpolateCRF uses the secant method (linear interpolation) between a passing (lowPass, vLow)
// and failing (highFail, vHigh) CRF to estimate the integer CRF where quality equals targetQuality.
func interpolateCRF(lowPass int, vLow float64, highFail int, vHigh float64, targetQuality float64) int {
	if highFail-lowPass <= 1 {
		return lowPass
	}
	if vLow <= vHigh {
		return (lowPass + highFail) / 2
	}
	ratio := (vLow - targetQuality) / (vLow - vHigh)
	interp := lowPass + int(math.Round(float64(highFail-lowPass)*ratio))
	return clampInt(interp, lowPass+1, highFail-1)
}

func clampInt(v, minVal, maxVal int) int {
	if v < minVal {
		return minVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

// testCRF tests a specific CRF against the sample clips and measures both compression savings
// and full-reference perceptual quality (VMAF, with automatic SSIM fallback).
func (s *Sampler) testCRF(
	media *detector.MediaInfo,
	selection detector.EncoderSelection,
	plan SamplePlan,
	crf int,
	preset string,
	minSavingsPercent float64,
	targetVMAF float64,
	deinterlaceFilter string,
) (*SampleResult, error) {
	var totalTranscodedBytes int64
	var weightedQualitySum float64
	var measuredDuration float64
	qualityMetric := ""

	// Estimate original bytes for these sample clips based on original overall bitrate
	// Bitrate is in bits per second, so bytes = (bitrate / 8) * duration
	origBytesForSamples := int64(float64(media.Bitrate) / 8.0 * plan.TotalSampleDuration)
	if origBytesForSamples <= 0 && media.Duration > 0 {
		origBytesForSamples = int64(float64(media.Size) * (plan.TotalSampleDuration / media.Duration))
	}

	_ = os.MkdirAll(s.ScratchDir, 0755)
	sampleTempDir, err := os.MkdirTemp(s.ScratchDir, fmt.Sprintf("sample_crf_%d_*", crf))
	if err != nil {
		return nil, fmt.Errorf("cannot create temporary sample directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(sampleTempDir) }()

	for i, clip := range plan.Clips {
		outClipPath := filepath.Join(sampleTempDir, fmt.Sprintf("clip_%d.mkv", i))

		args := []string{"-y", "-hide_banner", "-loglevel", "warning"}

		// Add hardware accel args
		if len(selection.HwAccelArgs) > 0 {
			args = append(args, selection.HwAccelArgs...)
		}

		// Fast seek before input
		args = append(args,
			"-ss", fmt.Sprintf("%.2f", clip.StartTime),
			"-t", fmt.Sprintf("%.2f", clip.Duration),
			"-i", media.FilePath,
			"-map", "0:v:0",
		)

		if deinterlaceFilter != "" {
			args = append(args, "-vf", deinterlaceFilter)
		}

		args = append(args, "-c:v", selection.EncoderName)

		// Set pixel format for hardware & player compatibility
		pixFmt := detector.ChoosePixelFormat(media.PixelFormat, selection.EncoderName)
		if pixFmt != "" {
			args = append(args, "-pix_fmt", pixFmt)
		}

		// Set CRF, CQ or Global Quality
		if strings.Contains(selection.EncoderName, "nvenc") {
			args = append(args, "-rc:v", "vbr", "-cq:v", strconv.Itoa(crf), "-b:v", "0")
		} else if selection.IsHardware {
			// For QSV/VAAPI, -global_quality or -q is used
			args = append(args, "-global_quality", strconv.Itoa(crf))
		} else {
			args = append(args, "-crf", strconv.Itoa(crf))
		}

		// Preset
		if strings.Contains(selection.EncoderName, "nvenc") {
			args = append(args, "-preset", "p4") // fast medium preset for sample probing
		} else if preset != "" {
			if selection.EncoderName == "libsvtav1" {
				args = append(args, "-preset", "7") // faster preset for sample probing
			} else {
				args = append(args, "-preset", preset)
			}
		}

		// Disable audio and subs for sample test to focus strictly on video compression ratio
		args = append(args, "-an", "-sn", outClipPath)

		cmd := exec.Command("ffmpeg", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("sample encoding clip %d failed: %w (output: %s)", i, err, string(out))
		}

		fi, err := os.Stat(outClipPath)
		if err != nil {
			return nil, fmt.Errorf("cannot stat sample output clip %d: %w", i, err)
		}
		totalTranscodedBytes += fi.Size()

		// Measure perceptual quality (VMAF / SSIM) of the encoded sample clip against the source clip
		if targetVMAF > 0 {
			score, metric, qErr := measureClipQuality(media.FilePath, outClipPath, clip, deinterlaceFilter)
			if qErr != nil {
				return nil, fmt.Errorf("perceptual quality measurement on clip %d failed: %w", i, qErr)
			}
			weightedQualitySum += score * clip.Duration
			measuredDuration += clip.Duration
			qualityMetric = metric
		}
	}

	// Calculate savings
	savingsRatio := 0.0
	if origBytesForSamples > 0 {
		savingsRatio = float64(origBytesForSamples-totalTranscodedBytes) / float64(origBytesForSamples)
	}

	savingsPercent := savingsRatio * 100.0

	var avgQuality float64
	qualityMet := true
	if targetVMAF > 0 && measuredDuration > 0 {
		avgQuality = weightedQualitySum / measuredDuration
		qualityMet = (avgQuality >= targetVMAF)
	}

	qualifies := qualityMet && (savingsPercent >= minSavingsPercent)

	var estimatedBitrate int64
	if plan.TotalSampleDuration > 0 {
		estimatedBitrate = int64((float64(totalTranscodedBytes) * 8.0) / plan.TotalSampleDuration)
	}

	return &SampleResult{
		CRF:              crf,
		VMAFScore:        avgQuality,
		QualityMetric:    qualityMetric,
		OriginalBytes:    origBytesForSamples,
		TranscodedBytes:  totalTranscodedBytes,
		SavingsRatio:     savingsRatio,
		SavingsPercent:   savingsPercent,
		EstimatedBitrate: estimatedBitrate,
		QualityMet:       qualityMet,
		Qualifies:        qualifies,
	}, nil
}

// measureClipQuality compares an encoded sample clip against the corresponding source clip
// using FFmpeg's libvmaf filter, falling back automatically to ssim if libvmaf is unavailable.
func measureClipQuality(sourcePath, encodedClipPath string, clip SampleClip, deinterlaceFilter string) (float64, string, error) {
	threads := runtime.NumCPU()
	if threads < 1 {
		threads = 1
	} else if threads > 8 {
		threads = 8
	}

	refChain := "setpts=PTS-STARTPTS,format=yuv420p"
	if deinterlaceFilter != "" {
		refChain = deinterlaceFilter + "," + refChain
	}

	// 1. Try VMAF (libvmaf) with frame subsampling (n_subsample=2) for rapid pre-flight scoring
	vmafFilter := fmt.Sprintf(
		"[0:v]setpts=PTS-STARTPTS,format=yuv420p[dist];[1:v]%s[ref];[dist][ref]libvmaf=n_threads=%d:n_subsample=2",
		refChain, threads,
	)

	args := []string{
		"-hide_banner",
		"-i", encodedClipPath,
		"-ss", fmt.Sprintf("%.2f", clip.StartTime),
		"-t", fmt.Sprintf("%.2f", clip.Duration),
		"-i", sourcePath,
		"-lavfi", vmafFilter,
		"-f", "null",
		"-",
	}

	cmd := exec.Command("ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	outputStr := string(out)
	if err == nil {
		if score, ok := ParseVMAFOutput(outputStr); ok {
			return score, "VMAF", nil
		}
	}

	// 2. Fallback to SSIM if libvmaf is not compiled into the system's FFmpeg
	ssimFilter := fmt.Sprintf(
		"[0:v]setpts=PTS-STARTPTS,format=yuv420p[dist];[1:v]%s[ref];[dist][ref]ssim",
		refChain,
	)
	ssimArgs := []string{
		"-hide_banner",
		"-i", encodedClipPath,
		"-ss", fmt.Sprintf("%.2f", clip.StartTime),
		"-t", fmt.Sprintf("%.2f", clip.Duration),
		"-i", sourcePath,
		"-lavfi", ssimFilter,
		"-f", "null",
		"-",
	}

	ssimCmd := exec.Command("ffmpeg", ssimArgs...)
	ssimOut, ssimErr := ssimCmd.CombinedOutput()
	if ssimErr == nil {
		if score, ok := ParseSSIMOutput(string(ssimOut)); ok {
			return score, "SSIM", nil
		}
	}

	return 0, "", fmt.Errorf("failed to compute VMAF/SSIM score (vmaf err: %v, output: %s)", err, outputStr)
}

// ParseVMAFOutput extracts the VMAF score (0-100) from FFmpeg's libvmaf log output.
func ParseVMAFOutput(output string) (float64, bool) {
	matches := vmafScoreRegex.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return 0, false
	}
	last := matches[len(matches)-1]
	if len(last) < 2 {
		return 0, false
	}
	val, err := strconv.ParseFloat(last[1], 64)
	if err != nil || math.IsNaN(val) {
		return 0, false
	}
	return val, true
}

// ParseSSIMOutput extracts the aggregate SSIM "All" score from FFmpeg's ssim log output
// and normalizes it to a 0-100 percentage scale.
func ParseSSIMOutput(output string) (float64, bool) {
	matches := ssimAllRegex.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return 0, false
	}
	last := matches[len(matches)-1]
	if len(last) < 2 {
		return 0, false
	}
	val, err := strconv.ParseFloat(last[1], 64)
	if err != nil || math.IsNaN(val) {
		return 0, false
	}
	return val * 100.0, true
}
