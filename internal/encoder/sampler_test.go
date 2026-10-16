package encoder

import (
	"testing"
)

func TestCalculateSamplePlan(t *testing.T) {
	// 1. Short clip (15 seconds)
	plan15 := CalculateSamplePlan(15.0)
	if len(plan15.Clips) != 1 {
		t.Fatalf("expected 1 clip for 15s video, got %d", len(plan15.Clips))
	}
	if plan15.Clips[0].StartTime != 0 || plan15.Clips[0].Duration != 15.0 {
		t.Errorf("expected sample of entire 15s clip, got start: %f dur: %f", plan15.Clips[0].StartTime, plan15.Clips[0].Duration)
	}

	// 2. Medium clip (60 seconds)
	plan60 := CalculateSamplePlan(60.0)
	if len(plan60.Clips) != 2 {
		t.Fatalf("expected 2 clips for 60s video, got %d", len(plan60.Clips))
	}
	if plan60.TotalSampleDuration != 10.0 {
		t.Errorf("expected total duration 10s for medium clip, got %f", plan60.TotalSampleDuration)
	}
	if plan60.Clips[0].StartTime != 15.0 || plan60.Clips[1].StartTime != 45.0 {
		t.Errorf("expected clips at 15s and 45s, got %f and %f", plan60.Clips[0].StartTime, plan60.Clips[1].StartTime)
	}

	// 3. Long video (7200 seconds / 2 hours)
	plan7200 := CalculateSamplePlan(7200.0)
	if len(plan7200.Clips) != 3 {
		t.Fatalf("expected 3 clips for 2h video, got %d", len(plan7200.Clips))
	}
	if plan7200.TotalSampleDuration != 30.0 {
		t.Errorf("expected total sample duration 30s for long video, got %f", plan7200.TotalSampleDuration)
	}
	// 20% = 1440s, 50% = 3600s, 80% = 5760s
	if plan7200.Clips[0].StartTime != 1440.0 || plan7200.Clips[1].StartTime != 3600.0 || plan7200.Clips[2].StartTime != 5760.0 {
		t.Errorf("expected clips at 1440s, 3600s, 5760s; got %f, %f, %f",
			plan7200.Clips[0].StartTime, plan7200.Clips[1].StartTime, plan7200.Clips[2].StartTime)
	}
}

func TestSearchOptimalCRF_FindsHighestCompressionMeetingVMAF(t *testing.T) {
	// Synthetic monotonic VMAF curve: VMAF(crf) = 100 - (crf - 14) * 0.5
	// At CRF 28: VMAF = 100 - 14*0.5 = 93.0 (exact highest CRF where VMAF >= 93.0)
	// At CRF 29: VMAF = 92.5 (< 93.0)
	mockEval := func(crf int) (*SampleResult, error) {
		vmaf := 100.0 - float64(crf-14)*0.5
		savings := float64(crf) * 2.0 // Higher CRF -> higher compression savings
		return &SampleResult{
			CRF:              crf,
			VMAFScore:        vmaf,
			QualityMetric:    "VMAF",
			SavingsPercent:   savings,
			SavingsRatio:     savings / 100.0,
			EstimatedBitrate: int64(5000000 - crf*100000),
		}, nil
	}

	res, err := SearchOptimalCRF(22, 14, 40, 93.0, 20.0, mockEval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.CRF != 28 {
		t.Errorf("expected optimal highest CRF 28 for target VMAF 93.0, got CRF %d (VMAF %.2f)", res.CRF, res.VMAFScore)
	}
	if !res.QualityMet {
		t.Errorf("expected QualityMet to be true")
	}
	if !res.Qualifies {
		t.Errorf("expected Qualifies to be true")
	}
	if len(res.History) == 0 || len(res.History) > MaxCRFSearchProbes {
		t.Errorf("expected 1..%d search probes, got %d", MaxCRFSearchProbes, len(res.History))
	}
}

func TestSearchOptimalCRF_StepsDownWhenInitialSeedTooAggressive(t *testing.T) {
	// Noisy/complex source where VMAF drops faster:
	// VMAF(crf) = 99.0 - (crf - 14) * 1.0
	// Target 93.0 -> exact optimal CRF is 20 (VMAF = 93.0)
	// Starting seed at 26 has VMAF = 87.0 (< 93.0), so search must step down to CRF 20.
	mockEval := func(crf int) (*SampleResult, error) {
		vmaf := 99.0 - float64(crf-14)*1.0
		savings := float64(crf) * 1.5
		return &SampleResult{
			CRF:            crf,
			VMAFScore:      vmaf,
			QualityMetric:  "VMAF",
			SavingsPercent: savings,
			SavingsRatio:   savings / 100.0,
		}, nil
	}

	res, err := SearchOptimalCRF(26, 14, 40, 93.0, 20.0, mockEval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.CRF != 20 {
		t.Errorf("expected search to step down to CRF 20, got CRF %d (VMAF %.2f)", res.CRF, res.VMAFScore)
	}
	if !res.Qualifies {
		t.Errorf("expected Qualifies=true at CRF 20 (savings %.1f%% >= 20%%)", res.SavingsPercent)
	}
}

func TestSearchOptimalCRF_RejectsWhenSavingsBelowThresholdAtMaxCompression(t *testing.T) {
	// Even at the highest CRF that preserves VMAF >= 93.0 (CRF 25), savings are only 12% (< 20%)
	mockEval := func(crf int) (*SampleResult, error) {
		vmaf := 98.0 - float64(crf-15)*0.5 // CRF 25 -> VMAF 93.0
		savings := 12.0
		return &SampleResult{
			CRF:            crf,
			VMAFScore:      vmaf,
			QualityMetric:  "VMAF",
			SavingsPercent: savings,
			SavingsRatio:   savings / 100.0,
		}, nil
	}

	res, err := SearchOptimalCRF(22, 14, 40, 93.0, 20.0, mockEval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.CRF != 25 {
		t.Errorf("expected perceptually optimal CRF 25, got %d", res.CRF)
	}
	if !res.QualityMet {
		t.Errorf("expected QualityMet=true at CRF 25")
	}
	if res.Qualifies {
		t.Errorf("expected Qualifies=false when savings (12%%) < minSavingsPercent (20%%)")
	}
}

func TestParseVMAFAndSSIMOutput(t *testing.T) {
	vmafLog := `[Parsed_libvmaf_2 @ 000001cb2e36ee40] VMAF score: 94.628190`
	score, ok := ParseVMAFOutput(vmafLog)
	if !ok || score != 94.628190 {
		t.Errorf("ParseVMAFOutput failed: got %f, %v", score, ok)
	}

	ssimLog := `[Parsed_ssim_0 @ 0x55] SSIM Y:0.981234 (17.26) U:0.990000 (20.00) V:0.991000 (20.45) All:0.984500 (18.09)`
	ssimScore, ssimOk := ParseSSIMOutput(ssimLog)
	if !ssimOk || ssimScore != 98.45 {
		t.Errorf("ParseSSIMOutput failed: got %f, %v; want 98.45, true", ssimScore, ssimOk)
	}
}
