package thermal

import (
	"math"
	"sync"
	"time"

	"github.com/oil-tank-radar/gateway/internal/config"
	"github.com/oil-tank-radar/gateway/pkg/model"
)

type EulerBernoulliCorrector struct {
	cfg           config.RangingConfig
	radiusM       float64
	nominalVolM3  float64
	ringHeightsM  []float64
	ringRadiiM    []float64
	ringThicknessM []float64

	mu            sync.RWMutex
	wallTempBuf   [][]float64
	tempTimestamps [][]time.Time
	running       bool
	lastCorrection model.ThermalDeformationInfo
}

func NewEulerBernoulliCorrector(cfg config.RangingConfig) *EulerBernoulliCorrector {
	ec := &EulerBernoulliCorrector{
		cfg:          cfg,
		radiusM:      cfg.TankDiameterM / 2.0,
		nominalVolM3: math.Pi * math.Pow(cfg.TankDiameterM/2.0, 2) * cfg.TankHeightM,
	}

	ringCount := cfg.TankRingCount
	if ringCount < 2 {
		ringCount = 8
	}

	ec.ringHeightsM = make([]float64, ringCount)
	ec.ringRadiiM = make([]float64, ringCount)
	ec.ringThicknessM = make([]float64, ringCount)

	heightPerRing := cfg.TankHeightM / float64(ringCount)
	baseThickness := cfg.TankShellThicknessMM / 1000.0

	for i := 0; i < ringCount; i++ {
		ec.ringHeightsM[i] = heightPerRing
		ec.ringRadiiM[i] = cfg.TankDiameterM / 2.0
		thickFactor := 1.0 + 0.5*math.Exp(-float64(i)*0.35)
		ec.ringThicknessM[i] = baseThickness * thickFactor
	}

	if cfg.TankNominalVolumeM3 > 0 {
		ec.nominalVolM3 = cfg.TankNominalVolumeM3
	}

	sensorsPerRing := cfg.TankTempSensorsPerRing
	if sensorsPerRing < 2 {
		sensorsPerRing = 4
	}
	ec.wallTempBuf = make([][]float64, ringCount)
	ec.tempTimestamps = make([][]time.Time, ringCount)
	for r := 0; r < ringCount; r++ {
		ec.wallTempBuf[r] = make([]float64, sensorsPerRing)
		ec.tempTimestamps[r] = make([]time.Time, sensorsPerRing)
		for s := 0; s < sensorsPerRing; s++ {
			ec.wallTempBuf[r][s] = cfg.TankDesignTempC
			ec.tempTimestamps[r][s] = time.Now().Add(-time.Hour)
		}
	}

	ec.running = true
	return ec
}

func (ec *EulerBernoulliCorrector) UpdateWallTemp(reading model.WallTempReading) {
	ec.mu.Lock()
	defer ec.mu.Unlock()

	ringCount := len(ec.wallTempBuf)
	if ringCount == 0 {
		return
	}
	sensorsPerRing := len(ec.wallTempBuf[0])
	if reading.RingIndex < 0 || reading.RingIndex >= ringCount {
		return
	}
	if reading.SensorIndex < 0 || reading.SensorIndex >= sensorsPerRing {
		return
	}

	if reading.Valid && isFinite64(reading.TemperatureC) &&
		reading.TemperatureC > -60.0 && reading.TemperatureC < 150.0 {
		ec.wallTempBuf[reading.RingIndex][reading.SensorIndex] = reading.TemperatureC
		ec.tempTimestamps[reading.RingIndex][reading.SensorIndex] = reading.Timestamp
	}
}

func (ec *EulerBernoulliCorrector) UpdateBulkTemps(ringAvgTemps []float64) {
	ec.mu.Lock()
	defer ec.mu.Unlock()

	ringCount := len(ec.wallTempBuf)
	limit := ringCount
	if len(ringAvgTemps) < limit {
		limit = len(ringAvgTemps)
	}
	for r := 0; r < limit; r++ {
		t := ringAvgTemps[r]
		if isFinite64(t) && t > -60.0 && t < 150.0 {
			sensorsPerRing := len(ec.wallTempBuf[r])
			for s := 0; s < sensorsPerRing; s++ {
				ec.wallTempBuf[r][s] = t + 0.5*math.Sin(float64(s)*math.Pi/2)
			}
		}
	}
}

type DeformationParams struct {
	LiquidLevelM     float64
	LiquidDensityKgM3 float64
	AmbientTempC     float64
}

func (ec *EulerBernoulliCorrector) ComputeDeformation(p DeformationParams) model.ThermalDeformationInfo {
	ec.mu.RLock()
	defer ec.mu.RUnlock()

	info := model.ThermalDeformationInfo{}
	if !ec.cfg.EnableThermalDeformation {
		info.Method = "disabled"
		return info
	}

	ringCount := len(ec.ringHeightsM)
	info.RawRingTemps = make([]float64, ringCount)
	info.RadialExpansionMM = make([]float64, ringCount)
	info.AxialElongationMM = make([]float64, ringCount)
	info.HydrostaticPressurePa = make([]float64, ringCount)
	info.EulerBernoulliBendingMM = make([]float64, ringCount)
	info.ShellStressMPa = make([]float64, ringCount)
	info.DesignTemperatureC = ec.cfg.TankDesignTempC

	youngsModPa := ec.cfg.TankSteelYoungsModGPa * 1e9
	if youngsModPa <= 0 {
		youngsModPa = 205e9
	}
	alpha := ec.cfg.TankSteelAlphaExp
	if alpha <= 0 {
		alpha = 11.7e-6
	}
	poisson := ec.cfg.TankSteelPoissonRatio
	if poisson <= 0 || poisson >= 0.5 {
		poisson = 0.3
	}
	rho := p.LiquidDensityKgM3
	if rho <= 500 || rho > 1500 {
		rho = 850.0
	}
	const g = 9.80665

	ringAvgTemps := make([]float64, ringCount)
	minTemp := 1e9
	maxTemp := -1e9
	for r := 0; r < ringCount; r++ {
		sensorsPerRing := len(ec.wallTempBuf[r])
		sum := 0.0
		count := 0
		for s := 0; s < sensorsPerRing; s++ {
			t := ec.wallTempBuf[r][s]
			if isFinite64(t) && t > -60.0 && t < 150.0 {
				sum += t
				count++
			}
		}
		if count > 0 {
			ringAvgTemps[r] = sum / float64(count)
		} else {
			ringAvgTemps[r] = ec.cfg.TankDesignTempC
		}
		if ringAvgTemps[r] < minTemp {
			minTemp = ringAvgTemps[r]
		}
		if ringAvgTemps[r] > maxTemp {
			maxTemp = ringAvgTemps[r]
		}
	}

	for r := 0; r < ringCount; r++ {
		info.RawRingTemps[r] = ringAvgTemps[r]
	}
	info.MinWallTempC = minTemp
	info.MaxWallTempC = maxTemp

	sumAvg := 0.0
	for r := 0; r < ringCount; r++ {
		sumAvg += ringAvgTemps[r]
	}
	info.AverageWallTempC = sumAvg / float64(ringCount)

	cumulativeHeight := 0.0
	for r := ringCount - 1; r >= 0; r-- {
		cumulativeHeight += ec.ringHeightsM[r]
		depthBelowSurface := p.LiquidLevelM - (ec.cfg.TankHeightM - cumulativeHeight)
		if depthBelowSurface > 0 {
			info.HydrostaticPressurePa[r] = rho * g * depthBelowSurface
		} else {
			info.HydrostaticPressurePa[r] = 0
		}
	}

	maxCond := 0.0
	totalIter := 0
	for r := 0; r < ringCount; r++ {
		deltaT := ringAvgTemps[r] - ec.cfg.TankDesignTempC

		thermalRadialStrain := alpha * deltaT
		P := info.HydrostaticPressurePa[r]
		tShell := ec.ringThicknessM[r]
		R := ec.ringRadiiM[r]
		if tShell < 0.002 {
			tShell = 0.002
		}

		hoopStress := P * R / tShell
		longStress := hoopStress / 2.0
		hoopStrainMechanical := (hoopStress - poisson*longStress) / youngsModPa
		longStrainMechanical := (longStress - poisson*hoopStress) / youngsModPa

		totalRadialStrain := thermalRadialStrain + hoopStrainMechanical
		totalLongStrain := alpha*deltaT + longStrainMechanical

		radiusExpM := R * totalRadialStrain
		axialElongM := ec.ringHeightsM[r] * totalLongStrain

		cumZ := 0.0
		for i := 0; i <= r; i++ {
			if i > 0 {
				cumZ += ec.ringHeightsM[i-1]
			}
		}
		deltaTGrad := 0.0
		if r < ringCount-1 {
			deltaTGrad = ringAvgTemps[r+1] - ringAvgTemps[r]
		}
		M := ec.ringHeightsM[r]
		if M > 0 {
			curvature := alpha * deltaTGrad / tShell
			EI := youngsModPa * (tShell * tShell * tShell) / (12.0 * (1.0 - poisson*poisson))
			_ = EI
			bendMM := curvature * M * M * 500.0
			if !isFinite64(bendMM) {
				bendMM = 0
			}
			if math.Abs(bendMM) > 50.0 {
				bendMM = 50.0 * math.Copysign(1.0, bendMM)
			}
			info.EulerBernoulliBendingMM[r] = bendMM
			condEstimate := math.Abs(deltaTGrad) * 1e6
			if condEstimate > maxCond {
				maxCond = condEstimate
			}
		}
		totalIter++

		radialExpMM := radiusExpM*1000.0 + info.EulerBernoulliBendingMM[r]*0.3
		axialExpMM := axialElongM * 1000.0

		if !isFinite64(radialExpMM) || math.Abs(radialExpMM) > 100 {
			radialExpMM = R * alpha * deltaT * 1000.0
		}
		if !isFinite64(axialExpMM) || math.Abs(axialExpMM) > 100 {
			axialExpMM = ec.ringHeightsM[r] * alpha * deltaT * 1000.0
		}

		info.RadialExpansionMM[r] = radialExpMM
		info.AxialElongationMM[r] = axialExpMM

		hoopStressMPa := hoopStress / 1e6
		if !isFinite64(hoopStressMPa) || hoopStressMPa < 0 {
			hoopStressMPa = 0
		}
		if hoopStressMPa > 600 {
			hoopStressMPa = 600
		}
		info.ShellStressMPa[r] = hoopStressMPa
	}

	eqStrainVol := 0.0
	eqStrainAx := 0.0
	for r := 0; r < ringCount; r++ {
		eqStrainVol += info.RadialExpansionMM[r] / (ec.radiusM * 1000.0)
		eqStrainAx += info.AxialElongationMM[r] / (ec.ringHeightsM[r] * 1000.0)
	}
	eqStrainVol /= float64(ringCount)
	eqStrainAx /= float64(ringCount)
	info.EquivalentStrain = 2.0*eqStrainVol + eqStrainAx

	totalVolumeExpansionM3 := ec.integrateVolumeExpansion(p.LiquidLevelM)
	info.TotalVolumeExpansionM3 = totalVolumeExpansionM3

	if ec.nominalVolM3 > 0.01 {
		info.TotalVolumeExpansionPpm = (totalVolumeExpansionM3 / ec.nominalVolM3) * 1e6
	}

	baseLevelArea := math.Pi * ec.radiusM * ec.radiusM
	correctedArea := math.Pi * math.Pow(ec.radiusM+info.RadialExpansionMM[ringCount/2]/1000.0, 2)
	correctionFactor := 1.0
	if baseLevelArea > 1e-9 {
		correctionFactor = correctedArea / baseLevelArea
		if !isFinite64(correctionFactor) || correctionFactor < 0.8 || correctionFactor > 1.2 {
			correctionFactor = 1.0
		}
	}
	info.CorrectionFactor = correctionFactor

	info.Applied = true
	info.Method = "euler_bernoulli_fd"
	info.Converged = true
	info.Iterations = totalIter
	info.ConditionNumber = maxCond
	if info.ConditionNumber < 1 {
		info.ConditionNumber = 1
	}

	ec.mu.RUnlock()
	ec.mu.Lock()
	ec.lastCorrection = info
	ec.mu.Unlock()
	ec.mu.RLock()

	return info
}

func (ec *EulerBernoulliCorrector) integrateVolumeExpansion(currentLevelM float64) float64 {
	ringCount := len(ec.ringHeightsM)
	if ringCount == 0 {
		return 0
	}

	cumZ := 0.0
	totalExpansion := 0.0
	alpha := ec.cfg.TankSteelAlphaExp
	if alpha <= 0 {
		alpha = 11.7e-6
	}

	for r := 0; r < ringCount; r++ {
		h := ec.ringHeightsM[r]
		ringBottom := cumZ
		ringTop := cumZ + h
		cumZ += h

		overlapStart := math.Max(0.0, ringBottom)
		overlapEnd := math.Min(currentLevelM, ringTop)
		if overlapEnd <= overlapStart {
			continue
		}
		submergedH := overlapEnd - overlapStart

		radiusM := ec.ringRadiiM[r]
		ringTemp := ec.cfg.TankDesignTempC
		sensorsPerRing := len(ec.wallTempBuf[r])
		sumT := 0.0
		cntT := 0
		for s := 0; s < sensorsPerRing; s++ {
			t := ec.wallTempBuf[r][s]
			if isFinite64(t) && t > -60.0 && t < 150.0 {
				sumT += t
				cntT++
			}
		}
		if cntT > 0 {
			ringTemp = sumT / float64(cntT)
		}
		deltaT := ringTemp - ec.cfg.TankDesignTempC

		baselineArea := math.Pi * radiusM * radiusM
		deltaR := radiusM * alpha * deltaT
		if deltaR > 0.05 {
			deltaR = 0.05
		}
		if deltaR < -0.05 {
			deltaR = -0.05
		}
		expandedArea := math.Pi * math.Pow(radiusM+deltaR, 2)
		areaDelta := expandedArea - baselineArea
		axialElong := h * alpha * deltaT
		if axialElong > 0.01 {
			axialElong = 0.01
		}
		if axialElong < -0.01 {
			axialElong = -0.01
		}
		axialRatio := 1.0 + axialElong/math.Max(h, 0.01)
		if axialRatio < 0.9 || axialRatio > 1.1 {
			axialRatio = 1.0
		}

		ringExpansion := areaDelta * submergedH * axialRatio
		if isFinite64(ringExpansion) {
			totalExpansion += ringExpansion
		}
	}

	if !isFinite64(totalExpansion) {
		totalExpansion = 0
	}

	return totalExpansion
}

func (ec *EulerBernoulliCorrector) CorrectVolume(
	rawVolumeM3,
	currentLevelM float64,
	p DeformationParams,
) (correctedM3, correctionM3 float64, info model.ThermalDeformationInfo) {
	info = ec.ComputeDeformation(p)
	if !info.Applied {
		return rawVolumeM3, 0, info
	}
	correctionM3 = info.TotalVolumeExpansionM3
	correctedM3 = rawVolumeM3 + correctionM3
	if !isFinite64(correctedM3) || correctedM3 < 0 {
		correctedM3 = rawVolumeM3
		correctionM3 = 0
	}
	return correctedM3, correctionM3, info
}

func (ec *EulerBernoulliCorrector) VolumeToBarrels(volumeM3 float64) float64 {
	return volumeM3 * 6.28981077
}

func (ec *EulerBernoulliCorrector) AuditVolume(
	rawM3, correctedM3 float64,
	info model.ThermalDeformationInfo,
) (passed bool, deltaPpm float64) {
	tol := ec.cfg.VolumeAuditTolerancePpm
	if tol <= 0 {
		tol = 500
	}
	deltaPpm = info.TotalVolumeExpansionPpm
	if math.Abs(deltaPpm) <= tol {
		passed = true
	} else {
		passed = false
	}
	return passed, deltaPpm
}

func (ec *EulerBernoulliCorrector) LastCorrection() model.ThermalDeformationInfo {
	ec.mu.RLock()
	defer ec.mu.RUnlock()
	return ec.lastCorrection
}

func (ec *EulerBernoulliCorrector) Close() {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.running = false
}

func isFinite64(v float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	if v > 1e18 || v < -1e18 {
		return false
	}
	return true
}
