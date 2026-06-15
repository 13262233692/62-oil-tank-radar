package fft

import (
	"math"

	"github.com/oil-tank-radar/gateway/internal/config"
	"github.com/oil-tank-radar/gateway/pkg/model"
)

const (
	dlsEpsilon          = 1e-12
	finiteCheckTolerance = 1e100
)

// ============================================================
// 第一层：相干性掩码隔离 (Coherence Masking Gate)
// 对低相干回波格点实施掩码，阻止相位奇异性污染后续解算
// ============================================================

type CoherenceMask struct {
	cfg config.FFTConfig
}

func NewCoherenceMask(cfg config.FFTConfig) *CoherenceMask {
	if cfg.PhaseCoherenceWeight <= 0 {
		cfg.PhaseCoherenceWeight = 0.6
	}
	if cfg.AmplitudeStabilityWeight <= 0 {
		cfg.AmplitudeStabilityWeight = 0.4
	}
	if cfg.CoherenceMaskThreshold <= 0 {
		cfg.CoherenceMaskThreshold = 0.35
	}
	return &CoherenceMask{cfg: cfg}
}

// Apply 在CFAR前对RD矩阵进行相干性掩码
// rdMatrix: 多普勒×距离 的幅度矩阵（行优先）
// rangeDoppler: 对应的复数矩阵（用于相位提取）
// 返回掩码后的RD矩阵（原地修改），以及被掩码格点数量
func (cm *CoherenceMask) Apply(
	rdMatrix []float64,
	rangeDoppler [][]complex128,
	dopplerBins, rangeBins int,
) (int, float64) {
	totalCells := dopplerBins * rangeBins
	for i := 0; i < totalCells; i++ {
		v := rdMatrix[i]
		if !isFinite(v) || v < 0 {
			rdMatrix[i] = 0.0
		}
	}

	if !cm.cfg.EnableCoherenceMask {
		return 0, 1.0
	}

	maskedCount := 0
	totalCoherence := 0.0
	validCount := 0

	phaseProfile := make([]float64, dopplerBins)
	ampProfile := make([]float64, dopplerBins)

	for r := 0; r < rangeBins; r++ {
		for d := 0; d < dopplerBins; d++ {
			ampProfile[d] = rdMatrix[d*rangeBins+r]
			if d < len(rangeDoppler) && r < len(rangeDoppler[d]) {
				cv := rangeDoppler[d][r]
				re := real(cv)
				im := imag(cv)
				if !isFinite(re) || !isFinite(im) {
					phaseProfile[d] = 0.0
				} else {
					phaseProfile[d] = math.Atan2(im, re)
				}
			} else {
				phaseProfile[d] = 0.0
			}
		}

		phaseCoherence := cm.calcPhaseCoherence(phaseProfile)
		ampStability := cm.calcAmplitudeStability(ampProfile)

		phaseCoherence = clamp01(phaseCoherence)
		ampStability = clamp01(ampStability)

		totalScore := phaseCoherence*cm.cfg.PhaseCoherenceWeight +
			ampStability*cm.cfg.AmplitudeStabilityWeight
		totalScore = clamp01(totalScore)

		if !isFinite(totalScore) || totalScore < cm.cfg.CoherenceMaskThreshold {
			for d := 0; d < dopplerBins; d++ {
				idx := d*rangeBins + r
				if idx < len(rdMatrix) {
					rdMatrix[idx] *= 0.05
					if rdMatrix[idx] < 0 {
						rdMatrix[idx] = 0
					}
				}
			}
			maskedCount++
		} else {
			totalCoherence += totalScore
			validCount++
		}
	}

	avgCoherence := 0.0
	if validCount > 0 {
		avgCoherence = totalCoherence / float64(validCount)
	}
	avgCoherence = clamp01(avgCoherence)

	return maskedCount, avgCoherence
}

func (cm *CoherenceMask) calcPhaseCoherence(phases []float64) float64 {
	n := len(phases)
	if n < 3 {
		return 0.0
	}

	var sumSin, sumCos float64
	for i := 0; i < n-1; i++ {
		diff := phases[i+1] - phases[i]
		for diff > math.Pi {
			diff -= 2 * math.Pi
		}
		for diff < -math.Pi {
			diff += 2 * math.Pi
		}
		sumSin += math.Sin(diff)
		sumCos += math.Cos(diff)
	}

	coherentLen := math.Hypot(sumSin, sumCos) / float64(n-1)
	if !isFinite(coherentLen) {
		return 0.0
	}
	return clamp01(coherentLen)
}

func (cm *CoherenceMask) calcAmplitudeStability(amps []float64) float64 {
	n := len(amps)
	if n < 2 {
		return 0.0
	}

	var mean, variance float64
	for _, a := range amps {
		mean += a
	}
	mean /= float64(n)

	if mean < 1e-10 {
		return 0.0
	}

	for _, a := range amps {
		d := a - mean
		variance += d * d
	}
	variance /= float64(n)

	stdDev := math.Sqrt(variance)
	cv := stdDev / (mean + 1e-12)
	stability := 1.0 / (1.0 + cv*cv)

	if !isFinite(stability) {
		return 0.0
	}
	return clamp01(stability)
}

// ============================================================
// 第二层：阻尼最小二乘 (DLS) 亚像素峰值解算
// 镇压Hessian矩阵特征值退化导致的发散
// ============================================================

type SubPixelResult struct {
	RangeOffset   float64
	DopplerOffset float64
	RangeExact    float64
	DopplerExact  float64
	ConditionNum  float64
	Converged     bool
	Iterations    int
	MethodUsed    string
}

type DLSSubPixelSolver struct {
	cfg config.FFTConfig
}

func NewDLSSubPixelSolver(cfg config.FFTConfig) *DLSSubPixelSolver {
	if cfg.DLSDampingFactor <= 0 {
		cfg.DLSDampingFactor = 1e-3
	}
	if cfg.DLSConditionNumLimit <= 0 {
		cfg.DLSConditionNumLimit = 1e10
	}
	if cfg.DLSMaxIterations <= 0 {
		cfg.DLSMaxIterations = 20
	}
	return &DLSSubPixelSolver{cfg: cfg}
}

// Solve 执行亚像素解算，三重降级策略：
//   优先：Levenberg-Marquardt风格DLS（阻尼高斯牛顿）
//   降级1：解析高斯对数插值（无迭代）
//   降级2：简单抛物线插值（最安全）
func (s *DLSSubPixelSolver) Solve(
	rdMatrix []float64,
	peakRangeBin, peakDopplerBin, rangeBins, dopplerBins int,
) SubPixelResult {
	if !s.cfg.EnableSubPixelInterp {
		return SubPixelResult{
			RangeExact:   float64(peakRangeBin),
			DopplerExact: float64(peakDopplerBin),
			MethodUsed:   "integer_bin",
			Converged:    true,
		}
	}

	result := s.tryDampedGaussNewton(rdMatrix, peakRangeBin, peakDopplerBin, rangeBins, dopplerBins)
	if result.Converged && isFinite(result.RangeExact) && isFinite(result.DopplerExact) {
		return result
	}

	gaussResult := s.tryGaussianLog(rdMatrix, peakRangeBin, peakDopplerBin, rangeBins, dopplerBins)
	if isFinite(gaussResult.RangeExact) && isFinite(gaussResult.DopplerExact) {
		return gaussResult
	}

	paraResult := s.parabolicInterp(rdMatrix, peakRangeBin, peakDopplerBin, rangeBins, dopplerBins)
	return paraResult
}

// tryDampedGaussNewton Levenberg-Marquardt风格DLS解算
func (s *DLSSubPixelSolver) tryDampedGaussNewton(
	rdMatrix []float64,
	peakR, peakD, rangeBins, dopplerBins int,
) SubPixelResult {
	result := SubPixelResult{
		RangeExact:   float64(peakR),
		DopplerExact: float64(peakD),
		MethodUsed:   "dls_gauss_newton",
	}

	if peakR < 2 || peakR > rangeBins-3 || peakD < 2 || peakD > dopplerBins-3 {
		return result
	}

	lambda := s.cfg.DLSDampingFactor
	curR := float64(peakR)
	curD := float64(peakD)

	for iter := 0; iter < s.cfg.DLSMaxIterations; iter++ {
		ir := int(math.Round(curR))
		id := int(math.Round(curD))

		grad, hessian := s.localGradientHessian(rdMatrix, ir, id, rangeBins, dopplerBins)

		condNum := matrixCondition2x2(hessian)
		result.ConditionNum = condNum

		if !isFiniteVector(grad[:]) || !isFiniteMatrix(hessian) {
			result.Converged = false
			return result
		}

		if condNum > s.cfg.DLSConditionNumLimit {
			lambda *= 10.0
		} else if condNum < s.cfg.DLSConditionNumLimit*0.01 {
			lambda *= 0.5
			lambda = math.Max(lambda, 1e-12)
		}

		dampedHessian := [2][2]float64{
			{hessian[0][0] + lambda, hessian[0][1]},
			{hessian[1][0], hessian[1][1] + lambda},
		}

		delta, ok := solve2x2(dampedHessian, [2]float64{-grad[0], -grad[1]})
		if !ok {
			result.Converged = false
			return result
		}

		nextR := curR + delta[0]
		nextD := curD + delta[1]

		stepLen := math.Hypot(delta[0], delta[1])
		result.Iterations = iter + 1

		if stepLen < 1e-4 {
			result.RangeExact = nextR
			result.DopplerExact = nextD
			result.RangeOffset = nextR - float64(peakR)
			result.DopplerOffset = nextD - float64(peakD)
			result.Converged = true
			return result
		}

		searchWindow := 2.0
		nextR = clamp(nextR, float64(peakR)-searchWindow, float64(peakR)+searchWindow)
		nextD = clamp(nextD, float64(peakD)-searchWindow, float64(peakD)+searchWindow)

		curR = nextR
		curD = nextD
	}

	result.RangeExact = curR
	result.DopplerExact = curD
	result.RangeOffset = curR - float64(peakR)
	result.DopplerOffset = curD - float64(peakD)
	result.Converged = true
	return result
}

func (s *DLSSubPixelSolver) localGradientHessian(
	rd []float64, r, d, rangeBins, dopplerBins int,
) ([2]float64, [2][2]float64) {
	var grad [2]float64
	var hess [2][2]float64

	idx := func(dr, dc int) float64 {
		nr := r + dr
		nd := d + dc
		if nr < 0 || nr >= rangeBins || nd < 0 || nd >= dopplerBins {
			return 0.0
		}
		return rd[nd*rangeBins+nr]
	}

	c := idx(0, 0)
	l := idx(-1, 0)
	rt := idx(1, 0)
	u := idx(0, -1)
	dn := idx(0, 1)
	l1 := idx(-1, -1)
	r1 := idx(1, -1)
	l2 := idx(-1, 1)
	r2 := idx(1, 1)

	grad[0] = 0.5 * (rt - l)
	grad[1] = 0.5 * (dn - u)

	hess[0][0] = rt - 2*c + l
	hess[1][1] = dn - 2*c + u
	hess[0][1] = 0.25 * (r2 - r1 - l2 + l1)
	hess[1][0] = hess[0][1]

	return grad, hess
}

func (s *DLSSubPixelSolver) tryGaussianLog(
	rd []float64, peakR, peakD, rangeBins, dopplerBins int,
) SubPixelResult {
	result := SubPixelResult{
		RangeExact:   float64(peakR),
		DopplerExact: float64(peakD),
		MethodUsed:   "gaussian_log",
	}

	if peakR < 1 || peakR > rangeBins-2 || peakD < 1 || peakD > dopplerBins-2 {
		result.Converged = true
		return result
	}

	getLog := func(dr, dc int) float64 {
		nr := peakR + dr
		nd := peakD + dc
		if nr < 0 || nr >= rangeBins || nd < 0 || nd >= dopplerBins {
			return -1e10
		}
		v := rd[nd*rangeBins+nr]
		if v <= 1e-20 {
			return -1e10
		}
		return math.Log(v)
	}

	ym1 := getLog(-1, 0)
	y0 := getLog(0, 0)
	yp1 := getLog(1, 0)

	rDenom := 2.0*(ym1 - 2*y0 + yp1)
	rOff := 0.0
	if math.Abs(rDenom) > 1e-12 {
		rOff = (ym1 - yp1) / rDenom
		if !isFinite(rOff) || math.Abs(rOff) > 1.0 {
			rOff = 0.0
		}
	}

	xm1 := getLog(0, -1)
	xp1 := getLog(0, 1)
	dDenom := 2.0*(xm1 - 2*y0 + xp1)
	dOff := 0.0
	if math.Abs(dDenom) > 1e-12 {
		dOff = (xm1 - xp1) / dDenom
		if !isFinite(dOff) || math.Abs(dOff) > 1.0 {
			dOff = 0.0
		}
	}

	result.RangeOffset = rOff
	result.DopplerOffset = dOff
	result.RangeExact = float64(peakR) + rOff
	result.DopplerExact = float64(peakD) + dOff
	result.Converged = true
	return result
}

func (s *DLSSubPixelSolver) parabolicInterp(
	rd []float64, peakR, peakD, rangeBins, dopplerBins int,
) SubPixelResult {
	result := SubPixelResult{
		RangeExact:   float64(peakR),
		DopplerExact: float64(peakD),
		MethodUsed:   "parabolic",
		Converged:    true,
	}

	if peakR < 1 || peakR > rangeBins-2 || peakD < 1 || peakD > dopplerBins-2 {
		return result
	}

	idx := func(dr, dc int) float64 {
		nr := peakR + dr
		nd := peakD + dc
		if nr < 0 || nr >= rangeBins || nd < 0 || nd >= dopplerBins {
			return 0.0
		}
		return rd[nd*rangeBins+nr]
	}

	ym1 := idx(-1, 0)
	y0 := idx(0, 0)
	yp1 := idx(1, 0)

	rDenom := ym1 - 2*y0 + yp1
	rOff := 0.0
	if math.Abs(rDenom) > 1e-12 {
		rOff = 0.5 * (ym1 - yp1) / rDenom
		if !isFinite(rOff) {
			rOff = 0.0
		}
		rOff = clamp(rOff, -0.5, 0.5)
	}

	xm1 := idx(0, -1)
	xp1 := idx(0, 1)
	dDenom := xm1 - 2*y0 + xp1
	dOff := 0.0
	if math.Abs(dDenom) > 1e-12 {
		dOff = 0.5 * (xm1 - xp1) / dDenom
		if !isFinite(dOff) {
			dOff = 0.0
		}
		dOff = clamp(dOff, -0.5, 0.5)
	}

	result.RangeOffset = rOff
	result.DopplerOffset = dOff
	result.RangeExact = float64(peakR) + rOff
	result.DopplerExact = float64(peakD) + dOff
	return result
}

// ============================================================
// 第三层：多径伪影抑制
// 在距离维抑制谐波/镜像伪影（全反射导致的间距规律假峰）
// ============================================================

type MultipathSuppressor struct {
	cfg config.FFTConfig
}

func NewMultipathSuppressor(cfg config.FFTConfig) *MultipathSuppressor {
	if cfg.MultipathNullDepth <= 0 {
		cfg.MultipathNullDepth = 0.2
	}
	if cfg.MultipathHarmonicOrder <= 0 {
		cfg.MultipathHarmonicOrder = 3
	}
	return &MultipathSuppressor{cfg: cfg}
}

func (ms *MultipathSuppressor) Apply(
	peaks []model.PeakInfo,
	rdMatrix []float64,
	rangeBins, dopplerBins int,
) []model.PeakInfo {
	if !ms.cfg.EnableMultipathSuppression || len(peaks) < 2 {
		return peaks
	}

	valid := make([]bool, len(peaks))
	for i := range valid {
		valid[i] = true
	}

	for order := 2; order <= ms.cfg.MultipathHarmonicOrder; order++ {
		for i := 0; i < len(peaks); i++ {
			if !valid[i] {
				continue
			}
			primaryR := peaks[i].RangeBin
			snr := peaks[i].SNR

			for j := 0; j < len(peaks); j++ {
				if i == j || !valid[j] {
					continue
				}
				candidateR := peaks[j].RangeBin

				ratio := float64(candidateR) / float64(maxInt(primaryR, 1))
				expectedRatio := float64(order)
				if math.Abs(ratio-expectedRatio) < 0.15 {
					if peaks[j].SNR < snr*ms.cfg.MultipathNullDepth {
						valid[j] = false
					}
				}
			}
		}
	}

	filtered := peaks[:0]
	for i, p := range peaks {
		if valid[i] {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

// ============================================================
// 数学工具：线性代数 + 有限值检查
// ============================================================

func solve2x2(A [2][2]float64, b [2]float64) ([2]float64, bool) {
	det := A[0][0]*A[1][1] - A[0][1]*A[1][0]
	if math.Abs(det) < dlsEpsilon {
		return [2]float64{}, false
	}
	invDet := 1.0 / det
	x := [2]float64{
		invDet * (A[1][1]*b[0] - A[0][1]*b[1]),
		invDet * (-A[1][0]*b[0] + A[0][0]*b[1]),
	}
	return x, true
}

func matrixCondition2x2(A [2][2]float64) float64 {
	a := A[0][0]
	b := A[0][1]
	c := A[1][0]
	d := A[1][1]

	tr := a + d
	det := a*d - b*c

	if math.Abs(det) < dlsEpsilon {
		return finiteCheckTolerance
	}

	tr2 := tr * tr
	discriminant := tr2 - 4*det
	if discriminant < 0 {
		discriminant = 0
	}
	sqrtDisc := math.Sqrt(discriminant)

	eig1 := (tr + sqrtDisc) / 2.0
	eig2 := (tr - sqrtDisc) / 2.0

	eig1Abs := math.Abs(eig1)
	eig2Abs := math.Abs(eig2)

	maxEig := math.Max(eig1Abs, eig2Abs)
	minEig := math.Min(eig1Abs, eig2Abs)

	if minEig < dlsEpsilon {
		return finiteCheckTolerance
	}
	cond := maxEig / minEig
	if !isFinite(cond) {
		return finiteCheckTolerance
	}
	return cond
}

func isFinite(v float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	if math.Abs(v) > finiteCheckTolerance {
		return false
	}
	return true
}

func isFiniteVector(v []float64) bool {
	for _, x := range v {
		if !isFinite(x) {
			return false
		}
	}
	return true
}

func isFiniteMatrix(m [2][2]float64) bool {
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			if !isFinite(m[i][j]) {
				return false
			}
		}
	}
	return true
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func SanitizePeak(p *model.PeakInfo) {
	if p.RangeBin < 0 {
		p.RangeBin = 0
	}
	if p.DopplerBin < 0 {
		p.DopplerBin = 0
	}
	if !isFinite(p.Magnitude) || p.Magnitude < 0 {
		p.Magnitude = 0
	}
	if !isFinite(p.Amplitude) || p.Amplitude < 0 {
		p.Amplitude = p.Magnitude
	}
	if !isFinite(p.SNR) || p.SNR < 0 {
		p.SNR = 0
	}
	if !isFinite(p.DistanceM) {
		p.DistanceM = 0
	}
	if !isFinite(p.VelocityMPS) {
		p.VelocityMPS = 0
	}
}
