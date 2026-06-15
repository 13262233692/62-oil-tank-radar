package ranging

import (
"math"
"sync"
"time"

"github.com/oil-tank-radar/gateway/internal/config"
"github.com/oil-tank-radar/gateway/internal/fft"
"github.com/oil-tank-radar/gateway/pkg/model"
"go.uber.org/zap"
)

type Calculator struct {
cfg       config.RangingConfig
fftCfg    config.FFTConfig
framerCfg config.FramerConfig
logger    *zap.Logger
waveBuf   []float64
waveIdx   int
waveMu    sync.Mutex
lastTemp  float64
stats     RangingStats
mu        sync.Mutex
}

type RangingStats struct {
Measurements   uint64
ValidMeasurements uint64
Errors        uint64
AvgSNR        float64
MinDistance    float64
MaxDistance    float64
}

const (
speedOfLight    = 299792458.0
waveBufferSize  = 100
)

func NewCalculator(
cfg config.RangingConfig,
fftCfg config.FFTConfig,
framerCfg config.FramerConfig,
logger *zap.Logger,
) *Calculator {
return &Calculator{
cfg:       cfg,
fftCfg:    fftCfg,
framerCfg: framerCfg,
logger:    logger,
waveBuf:   make([]float64, waveBufferSize),
lastTemp:  20.0,
}
}

func (c *Calculator) Calculate(
	fftResult *model.FFTResult,
	peaks []model.PeakInfo,
) *model.LevelMeasurement {
	c.mu.Lock()
	c.stats.Measurements++
	c.mu.Unlock()

	measurement := &model.LevelMeasurement{
		Timestamp:   fftResult.Timestamp,
		FrameNumber: fftResult.FrameNumber,
		Valid:       false,
		Status:      "processing",
	}
	measurement.Ref()

	if len(peaks) == 0 {
		c.mu.Lock()
		c.stats.Errors++
		c.mu.Unlock()
		measurement.Status = "no_peaks"
		return measurement
	}

	validPeak := c.selectValidPeak(peaks)
	if validPeak == nil {
		c.mu.Lock()
		c.stats.Errors++
		c.mu.Unlock()
		measurement.Status = "no_valid_peaks"
		return measurement
	}

	distance := c.calculateDistanceFromPeak(validPeak)
	if !isFinite64(distance) {
		distance = c.calculateDistance(validPeak.RangeBin)
	}
	distance = c.applyTemperatureCompensation(distance)
	if !isFinite64(distance) {
		measurement.Status = "nan_distance"
		return measurement
	}

	snr := validPeak.SNR
	if !isFinite64(snr) {
		snr = 0
	}
	confidence := c.calculateConfidence(snr, distance)
	if !isFinite64(confidence) {
		confidence = 0
	}

	if validPeak.ConditionNumber > 1e6 && validPeak.ConditionNumber < 1e100 {
		confidence *= math.Sqrt(1e6 / validPeak.ConditionNumber)
		confidence = math.Max(confidence, 0.01)
	}

	if snr < c.cfg.SNRThreshold {
		measurement.Status = "low_snr"
		return measurement
	}

	if distance < c.cfg.MinDistanceM || distance > c.cfg.MaxDistanceM {
		measurement.Status = "out_of_range"
		return measurement
	}

	level := c.cfg.TankHeightM - distance
	if !isFinite64(level) {
		level = 0
	}
	volume := c.calculateVolume(level)
	if !isFinite64(volume) {
		volume = 0
	}
	waveHeight := c.calculateWaveHeight(distance)
	if !isFinite64(waveHeight) {
		waveHeight = 0
	}
	velocity := validPeak.VelocityMPS
	if !isFinite64(velocity) {
		velocity = 0
	}

	measurement.DistanceM = distance
	measurement.LevelM = level
	measurement.VolumeM3 = volume
	measurement.TemperatureC = c.lastTemp
	measurement.SNR = snr
	measurement.VelocityMPS = velocity
	measurement.Confidence = confidence
	measurement.WaveHeightM = waveHeight
	measurement.PeakInfo = *validPeak
	measurement.PeakInfo.DistanceM = distance
	measurement.Valid = true
	measurement.Status = "valid"

c.mu.Lock()
c.stats.ValidMeasurements++
c.stats.AvgSNR = (c.stats.AvgSNR*float64(c.stats.ValidMeasurements-1) + snr) / float64(c.stats.ValidMeasurements)
if c.stats.MinDistance == 0 || distance < c.stats.MinDistance {
c.stats.MinDistance = distance
}
if distance > c.stats.MaxDistance {
c.stats.MaxDistance = distance
}
c.mu.Unlock()

c.addWaveSample(distance)

return measurement
}

func (c *Calculator) selectValidPeak(peaks []model.PeakInfo) *model.PeakInfo {
	if len(peaks) == 0 {
		return nil
	}

	var bestPeak *model.PeakInfo
	bestScore := -1.0

	for i := range peaks {
		peak := &peaks[i]
		if !isFinite64(peak.SNR) || !isFinite64(peak.Magnitude) {
			continue
		}

		distance := c.calculateDistanceFromPeak(peak)
		if !isFinite64(distance) {
			continue
		}

		if distance < c.cfg.MinDistanceM || distance > c.cfg.MaxDistanceM {
			continue
		}

		snr := peak.SNR
		rangeBins := c.fftCfg.RangeFFTSize
		centerBin := rangeBins / 4

		exactBin := float64(peak.RangeBin)
		if peak.SubPixelValid && isFinite64(peak.RangeSubPixel) {
			exactBin = peak.RangeSubPixel
		}
		rangeWeight := 1.0 - math.Abs(exactBin-float64(centerBin))/float64(rangeBins/2)
		rangeWeight = math.Max(rangeWeight, 0.1)

		coherenceBonus := 1.0
		if isFinite64(peak.CoherenceScore) && peak.CoherenceScore > 0 {
			coherenceBonus = 0.5 + 0.5*peak.CoherenceScore
		}

		condPenalty := 1.0
		if isFinite64(peak.ConditionNumber) && peak.ConditionNumber > c.fftCfg.DLSConditionNumLimit*0.1 {
			condPenalty = c.fftCfg.DLSConditionNumLimit * 0.1 / (peak.ConditionNumber + 1e-10)
			condPenalty = math.Max(condPenalty, 0.01)
		}

		score := snr * rangeWeight * coherenceBonus * condPenalty

		if score > bestScore {
			bestScore = score
			bestPeak = peak
		}
	}

	return bestPeak
}

func (c *Calculator) calculateDistanceFromPeak(peak *model.PeakInfo) float64 {
	rangeForCalc := float64(peak.RangeBin)
	if peak.SubPixelValid && isFinite64(peak.RangeSubPixel) {
		rangeForCalc = peak.RangeSubPixel
	}
	return fft.RangeBinToDistance(
		int(math.Round(rangeForCalc)),
		c.fftCfg.RangeFFTSize,
		c.framerCfg.SamplesPerChirp,
		c.framerCfg.ChirpsPerFrame,
		c.fftCfg.RangeFFTSize,
		c.cfg.StartFreqGHz,
		c.cfg.BandwidthGHz,
		c.cfg.SampleRateMHz,
	)
}

func isFinite64(v float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	return math.Abs(v) < 1e100
}

func (c *Calculator) calculateDistance(rangeBin int) float64 {
	return fft.RangeBinToDistance(
		rangeBin,
		c.fftCfg.RangeFFTSize,
		c.framerCfg.SamplesPerChirp,
		c.framerCfg.ChirpsPerFrame,
		c.fftCfg.RangeFFTSize,
		c.cfg.StartFreqGHz,
		c.cfg.BandwidthGHz,
		c.cfg.SampleRateMHz,
	)
}

func (c *Calculator) applyTemperatureCompensation(distance float64) float64 {
if !c.cfg.TempCompEnabled {
return distance
}

temp := c.lastTemp
refTemp := 20.0
tempFactor := 1.0 + 0.000001 * (temp - refTemp)

return distance * tempFactor
}

func (c *Calculator) calculateConfidence(snr, distance float64) float64 {
snrConfidence := 1.0 - math.Exp(-snr / 10.0)

rangeCenter := (c.cfg.MinDistanceM + c.cfg.MaxDistanceM) / 2.0
rangeHalf := (c.cfg.MaxDistanceM - c.cfg.MinDistanceM) / 2.0
rangeConfidence := 1.0 - math.Abs(distance-rangeCenter)/rangeHalf
rangeConfidence = math.Max(rangeConfidence, 0.1)

confidence := math.Sqrt(snrConfidence * rangeConfidence)

return math.Max(0.0, math.Min(1.0, confidence))
}

func (c *Calculator) calculateVolume(level float64) float64 {
if level <= 0 {
return 0
}

tankDiameter := 10.0
tankRadius := tankDiameter / 2.0

return math.Pi * tankRadius * tankRadius * level
}

func (c *Calculator) calculateWaveHeight(currentDistance float64) float64 {
c.waveMu.Lock()
defer c.waveMu.Unlock()

avgDistance := 0.0
count := 0
for _, d := range c.waveBuf {
if d > 0 {
avgDistance += d
count++
}
}

if count == 0 {
return 0
}

avgDistance /= float64(count)

variance := 0.0
for _, d := range c.waveBuf {
if d > 0 {
diff := d - avgDistance
variance += diff * diff
}
}
variance /= float64(count)

return math.Sqrt(variance) * 4.0
}

func (c *Calculator) addWaveSample(distance float64) {
c.waveMu.Lock()
defer c.waveMu.Unlock()

c.waveBuf[c.waveIdx] = distance
c.waveIdx = (c.waveIdx + 1) % len(c.waveBuf)
}

func (c *Calculator) UpdateTemperature(temp float64) {
c.lastTemp = temp
}

func (c *Calculator) CalculateVelocity(dopplerBin int) float64 {
	chirpDuration := c.framerCfg.ChirpDuration.Seconds()
	sampleRateHz := 1.0 / chirpDuration
	return fft.DopplerBinToVelocity(
		dopplerBin,
		c.fftCfg.DopplerFFTSize,
		c.cfg.StartFreqGHz,
		sampleRateHz,
		c.framerCfg.SamplesPerChirp,
	)
}

func (c *Calculator) GetWaveMeasurement() *model.WaveMeasurement {
c.waveMu.Lock()
defer c.waveMu.Unlock()

avgDistance := 0.0
count := 0
for _, d := range c.waveBuf {
if d > 0 {
avgDistance += d
count++
}
}

if count == 0 {
return &model.WaveMeasurement{
Timestamp: time.Now(),
Valid:     false,
}
}

avgDistance /= float64(count)

variance := 0.0
for _, d := range c.waveBuf {
if d > 0 {
diff := d - avgDistance
variance += diff * diff
}
}
variance /= float64(count)

waveHeight := math.Sqrt(variance) * 4.0

return &model.WaveMeasurement{
Timestamp: time.Now(),
HeightM:   waveHeight,
PeriodS:    0.5,
Valid:       true,
}
}

func (c *Calculator) GetStats() RangingStats {
c.mu.Lock()
defer c.mu.Unlock()
return c.stats
}

func (c *Calculator) Close() error {
stats := c.GetStats()
c.logger.Info("Ranging calculator stopped",
zap.Uint64("measurements", stats.Measurements),
zap.Uint64("valid_measurements", stats.ValidMeasurements),
zap.Uint64("errors", stats.Errors),
zap.Float64("avg_snr", stats.AvgSNR),
zap.Float64("min_distance", stats.MinDistance),
zap.Float64("max_distance", stats.MaxDistance),
)
return nil
}