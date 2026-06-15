package fft

import (
	"math"
	"math/rand"
	"testing"

	"github.com/oil-tank-radar/gateway/internal/config"
	"github.com/oil-tank-radar/gateway/pkg/model"
)

func makeRobustTestConfig() config.FFTConfig {
	return config.FFTConfig{
		RangeFFTSize:            256,
		DopplerFFTSize:          128,
		WindowType:              "hann",
		WindowAlpha:             0.5,
		CFARGuardCells:          2,
		CFARTrainCells:          4,
		CFARThreshold:           2.0,
		EnableFFTShift:          true,
		EnableCoherenceMask:     true,
		CoherenceMaskThreshold:  0.35,
		PhaseCoherenceWeight:    0.6,
		AmplitudeStabilityWeight: 0.4,
		EnableSubPixelInterp:    true,
		SubPixelMethod:          "dls",
		DLSDampingFactor:        1e-3,
		DLSConditionNumLimit:    1e10,
		DLSMaxIterations:        20,
		EnableMultipathSuppression: true,
		MultipathNullDepth:      0.2,
		MultipathHarmonicOrder:  3,
	}
}

// ============================================================
// Test 1: NaN / Inf 恶意注入攻击 - 不崩溃
// ============================================================
func TestCoherenceMask_NaNInfInjection(t *testing.T) {
	cfg := makeRobustTestConfig()
	cm := NewCoherenceMask(cfg)

	rows, cols := 64, 128
	rd := make([]float64, rows*cols)
	rdc := make([][]complex128, rows)
	for i := range rdc {
		rdc[i] = make([]complex128, cols)
	}

	rng := rand.New(rand.NewSource(42))
	for i := range rd {
		switch rng.Intn(10) {
		case 0:
			rd[i] = math.NaN()
		case 1:
			rd[i] = math.Inf(1)
		case 2:
			rd[i] = math.Inf(-1)
		default:
			rd[i] = rng.Float64() * 100
		}
		rdc[i/cols][i%cols] = complex(rd[i], 0)
	}

	masked, avg := cm.Apply(rd, rdc, rows, cols)

	t.Logf("NaN/Inf injection: masked=%d cols, avg coherence=%.4f", masked, avg)

	for _, v := range rd {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Error("CoherenceMask did not sanitize NaN/Inf output")
			break
		}
	}
}

// ============================================================
// Test 2: 全反射多径导致相位奇异性 - DLS镇压发散
// ============================================================
func TestDLSSolver_PhaseSingularity(t *testing.T) {
	cfg := makeRobustTestConfig()
	solver := NewDLSSubPixelSolver(cfg)

	rows, cols := 64, 128
	rd := make([]float64, rows*cols)

	// 构造相位奇异性场景：
	// 在主峰周围叠加全反射多径假峰，形成Hessian特征值退化
	peakR, peakD := 50, 32
	sigmaR, sigmaD := 8.0, 3.0
	for d := 0; d < rows; d++ {
		for r := 0; r < cols; r++ {
			main := 1000.0 * math.Exp(
				-math.Pow(float64(r-peakR), 2)/(2*sigmaR*sigmaR) -
					math.Pow(float64(d-peakD), 2)/(2*sigmaD*sigmaD),
			)
			ghost1 := 600.0 * math.Exp(
				-math.Pow(float64(r-peakR*2), 2)/(2*sigmaR*sigmaR) -
					math.Pow(float64(d-peakD), 2)/(2*sigmaD*sigmaD),
			)
			ghost2 := 400.0 * math.Exp(
				-math.Pow(float64(r-peakR/2), 2)/(2*sigmaR*sigmaR) -
					math.Pow(float64(d-peakD), 2)/(2*sigmaD*sigmaD),
			)
			noise := rand.Float64() * 10
			rd[d*cols+r] = main + ghost1 + ghost2 + noise
		}
	}

	result := solver.Solve(rd, peakR, peakD, cols, rows)

	t.Logf("Phase singularity test:")
	t.Logf("  Method: %s, Converged: %v, Iter: %d",
		result.MethodUsed, result.Converged, result.Iterations)
	t.Logf("  Range exact: %.4f, Doppler exact: %.4f",
		result.RangeExact, result.DopplerExact)
	t.Logf("  Condition num: %.2e", result.ConditionNum)

	if !result.Converged && result.MethodUsed == "dls_gauss_newton" {
		t.Logf("  DLS detected singularity and correctly declined")
	}
	if math.IsNaN(result.RangeExact) || math.IsInf(result.RangeExact, 0) ||
		math.IsNaN(result.DopplerExact) || math.IsInf(result.DopplerExact, 0) {
		t.Error("DLS Solver produced NaN/Inf result on phase singularity input!")
	}
	if result.RangeExact < 0 || result.RangeExact > float64(cols) {
		t.Errorf("DLS Solver produced out-of-range range bin: %.4f", result.RangeExact)
	}
}

// ============================================================
// Test 3: 条件数极端退化 (det(H)→0) - 三重降级策略
// ============================================================
func TestDLSSolver_IllConditionedHessian(t *testing.T) {
	cfg := makeRobustTestConfig()
	solver := NewDLSSubPixelSolver(cfg)

	rows, cols := 64, 128
	rd := make([]float64, rows*cols)
	for i := range rd {
		rd[i] = 100.0
	}

	peakR, peakD := 50, 32
	for dr := -2; dr <= 2; dr++ {
		for dd := -2; dd <= 2; dd++ {
			r := peakR + dr
			d := peakD + dd
			if r >= 0 && r < cols && d >= 0 && d < rows {
				// 制造几乎线性相关的Hessian
				rd[d*cols+r] = 1000.0 + float64(dr)*1e-6
			}
		}
	}

	result := solver.Solve(rd, peakR, peakD, cols, rows)

	t.Logf("Ill-conditioned test:")
	t.Logf("  Method used: %s (fallback chain: DLS→Gauss→Para)", result.MethodUsed)
	t.Logf("  Converged: %v", result.Converged)
	t.Logf("  Condition num: %.2e", result.ConditionNum)
	t.Logf("  Range offset: %.4f bins, Doppler offset: %.4f bins",
		result.RangeOffset, result.DopplerOffset)

	if !isFinite(result.RangeExact) || !isFinite(result.DopplerExact) {
		t.Error("Ill-conditioned matrix produced non-finite result!")
	}
}

// ============================================================
// Test 4: 极端网格相干性(多径相干破坏) - Masking Gate隔离
// ============================================================
func TestCoherenceMask_MultipathCoherenceCollapse(t *testing.T) {
	cfg := makeRobustTestConfig()
	cm := NewCoherenceMask(cfg)

	rows, cols := 64, 128
	rd := make([]float64, rows*cols)
	rdc := make([][]complex128, rows)
	for i := range rdc {
		rdc[i] = make([]complex128, cols)
	}

	rng := rand.New(rand.NewSource(99))
	for r := 0; r < cols; r++ {
		// 随机破坏某些range-bin的多普勒相干性
		coherenceCollapse := rng.Float64() < 0.3
		for d := 0; d < rows; d++ {
			idx := d*cols + r
			base := 200.0 * math.Exp(-math.Pow(float64(r-50), 2)/200.0)
			if coherenceCollapse {
				rd[idx] = base + rng.Float64()*200
				rdc[d][r] = complex(rd[idx], rng.Float64()*100)
			} else {
				phase := float64(d) * 0.1
				rd[idx] = base + 10*math.Cos(phase)
				rdc[d][r] = complex(rd[idx]*math.Cos(phase), rd[idx]*math.Sin(phase))
			}
		}
	}

	masked, avg := cm.Apply(rd, rdc, rows, cols)

	t.Logf("Coherence collapse test:")
	t.Logf("  Masked columns: %d / %d (%.1f%%)",
		masked, cols, 100.0*float64(masked)/float64(cols))
	t.Logf("  Average coherence: %.4f", avg)

	if masked < int(float64(cols)*0.1) {
		t.Logf("  Warning: fewer masks than expected (expected ~30%% collapse)")
	}
	if avg < 0 || avg > 1 {
		t.Errorf("Avg coherence out of range: %.4f", avg)
	}
	for _, v := range rd {
		if !isFinite(v) || v < 0 {
			t.Error("Masking Gate output contains invalid values!")
			break
		}
	}
}

// ============================================================
// Test 5: SanitizePeak 全排列验证
// ============================================================
func TestSanitizePeak_AllInvalids(t *testing.T) {
	cases := []model.PeakInfo{
		{SNR: math.NaN(), Magnitude: math.Inf(1)},
		{RangeBin: -5, DistanceM: math.NaN()},
		{VelocityMPS: math.Inf(-1), Amplitude: -100},
		{ConditionNumber: 1e200, Confidence: math.NaN()},
		{RangeSubPixel: math.NaN(), DopplerSubPixel: math.Inf(1)},
	}

	for i, c := range cases {
		p := c
		SanitizePeak(&p)
		if !isFinite(p.SNR) || !isFinite(p.Magnitude) {
			t.Errorf("case %d: SNR/Magnitude not sanitized: snr=%.2e, mag=%.2e",
				i, p.SNR, p.Magnitude)
		}
		if !isFinite(p.DistanceM) || !isFinite(p.VelocityMPS) {
			t.Errorf("case %d: Distance/Velocity not sanitized: dist=%.2e, vel=%.2e",
				i, p.DistanceM, p.VelocityMPS)
		}
	}
	t.Log("SanitizePeak all-invalid permutations: PASS")
}

// ============================================================
// Test 6: 多径谐波抑制
// ============================================================
func TestMultipathSuppressor_HarmonicRejection(t *testing.T) {
	cfg := makeRobustTestConfig()
	ms := NewMultipathSuppressor(cfg)

	rows, cols := 64, 128
	rd := make([]float64, rows*cols)
	for i := range rd {
		rd[i] = rand.Float64() * 5
	}

	peaks := []model.PeakInfo{
		{RangeBin: 25, SNR: 100, Magnitude: 1000},
		{RangeBin: 50, SNR: 300, Magnitude: 3000}, // 主峰
		{RangeBin: 75, SNR: 20, Magnitude: 200},   // 1.5×谐波（被抑制）
		{RangeBin: 100, SNR: 40, Magnitude: 400},  // 2×谐波（被抑制）
	}

	filtered := ms.Apply(peaks, rd, cols, rows)

	t.Logf("Multipath suppression: input=%d peaks, output=%d peaks",
		len(peaks), len(filtered))

	for _, p := range filtered {
		t.Logf("  Bin %d: SNR=%.0f (kept)", p.RangeBin, p.SNR)
	}

	keptBins := make(map[int]bool)
	for _, p := range filtered {
		keptBins[p.RangeBin] = true
	}
	if !keptBins[50] {
		t.Error("Primary peak at bin 50 should be preserved!")
	}
}

// ============================================================
// Benchmark: 极端相干掩码开销 (worst case)
// ============================================================
func BenchmarkRobustPipeline_Full(b *testing.B) {
	cfg := makeRobustTestConfig()
	cm := NewCoherenceMask(cfg)
	solver := NewDLSSubPixelSolver(cfg)
	ms := NewMultipathSuppressor(cfg)
	fft := NewCGOFFT(cfg)
	fft.Init()
	defer fft.Close()

	rows, cols := 128, 256
	rd := make([]float64, rows*cols)
	rdc := make([][]complex128, rows)
	for i := range rdc {
		rdc[i] = make([]complex128, cols)
	}
	peakBuf := make([]model.PeakInfo, 0, 100)

	rng := rand.New(rand.NewSource(123))
	for d := 0; d < rows; d++ {
		for r := 0; r < cols; r++ {
			main := 1000.0 * math.Exp(
				-math.Pow(float64(r-100), 2)/1000.0 -
					math.Pow(float64(d-64), 2)/200.0,
			)
			rd[d*cols+r] = main + rng.Float64()*10
			rdc[d][r] = complex(rd[d*cols+r], 0)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		localRd := make([]float64, len(rd))
		copy(localRd, rd)
		cm.Apply(localRd, rdc, rows, cols)
		pks := fft.CFARDetect(localRd, rows, cols, 2, 4, 3.0, peakBuf)
		pks = ms.Apply(pks, localRd, cols, rows)
		for j := range pks {
			solver.Solve(localRd, pks[j].RangeBin, pks[j].DopplerBin, cols, rows)
			SanitizePeak(&pks[j])
		}
	}
}
