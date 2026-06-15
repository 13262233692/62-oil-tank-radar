package fft

import (
"context"
"math"
"runtime"
"sync"
"time"

"github.com/oil-tank-radar/gateway/internal/config"
"github.com/oil-tank-radar/gateway/pkg/model"
"github.com/oil-tank-radar/gateway/pkg/pool"
"go.uber.org/zap"
)

type FFTOperator interface {
Init() error
RangeDopplerFFT(rawFrame *model.FMCWRawFrame, result *model.FFTResult, numChirps, numSamples, numChannels int)
FFTShift(data []float64, rows, cols int)
CFARDetect(data []float64, rows, cols int, guardCells, trainCells int, threshold float64, peaks []model.PeakInfo) []model.PeakInfo
PhaseUnwrap(phases []float64, threshold float64)
Close() error
}

type Processor struct {
cfg       config.FFTConfig
framerCfg config.FramerConfig
pool      *pool.BufferPool
logger    *zap.Logger
inCh      <-chan *model.FMCWRawFrame
outCh     chan<- *model.FFTResult
fft       FFTOperator
wg        sync.WaitGroup
ctx       context.Context
cancel    context.CancelFunc
stats     ProcessorStats
mu        sync.Mutex
peakBuf   []model.PeakInfo
}

type ProcessorStats struct {
FramesProcessed uint64
FFTTimeNs       uint64
Errors          uint64
DroppedFrames   uint64
}

func NewProcessor(
cfg config.FFTConfig,
framerCfg config.FramerConfig,
bufferPool *pool.BufferPool,
logger *zap.Logger,
) *Processor {
ctx, cancel := context.WithCancel(context.Background())

return &Processor{
cfg:       cfg,
framerCfg: framerCfg,
pool:      bufferPool,
logger:    logger,
ctx:       ctx,
cancel:    cancel,
fft:       NewCGOFFT(cfg),
peakBuf:   make([]model.PeakInfo, 0, 100),
}
}

func (p *Processor) Start(inCh <-chan *model.FMCWRawFrame, outCh chan<- *model.FFTResult) error {
p.inCh = inCh
p.outCh = outCh

if err := p.fft.Init(); err != nil {
return err
}

workers := p.cfg.Workers
if workers <= 0 {
workers = runtime.NumCPU()
}

for i := 0; i < workers; i++ {
p.wg.Add(1)
go p.worker(i)
}

p.logger.Info("FFT processor started",
zap.Int("range_fft_size", p.cfg.RangeFFTSize),
zap.Int("doppler_fft_size", p.cfg.DopplerFFTSize),
zap.String("window_type", p.cfg.WindowType),
zap.Int("workers", workers),
)

return nil
}

func (p *Processor) worker(id int) {
defer p.wg.Done()

for {
select {
case <-p.ctx.Done():
return
case rawFrame, ok := <-p.inCh:
if !ok {
return
}

start := time.Now()
fftResult, err := p.processFrame(rawFrame)
duration := time.Since(start)

rawFrame.Unref()

if err != nil {
p.mu.Lock()
p.stats.Errors++
p.mu.Unlock()

p.logger.Error("FFT processing error",
zap.Int("worker", id),
zap.Error(err),
)
continue
}

p.mu.Lock()
p.stats.FramesProcessed++
p.stats.FFTTimeNs += uint64(duration.Nanoseconds())
p.mu.Unlock()

select {
case p.outCh <- fftResult:
case <-p.ctx.Done():
p.pool.PutFFTResult(fftResult)
return
default:
p.mu.Lock()
p.stats.DroppedFrames++
p.mu.Unlock()
p.pool.PutFFTResult(fftResult)
p.logger.Warn("FFT output channel full, dropping frame",
zap.Int("worker", id),
)
}
}
}
}

func (p *Processor) processFrame(rawFrame *model.FMCWRawFrame) (*model.FFTResult, error) {
fftResult := p.pool.GetFFTResult()
fftResult.Timestamp = rawFrame.Timestamp
fftResult.FrameNumber = rawFrame.FrameNumber
fftResult.RangeBins = p.cfg.RangeFFTSize
fftResult.DopplerBins = p.cfg.DopplerFFTSize

numChirps := p.framerCfg.ChirpsPerFrame
numSamples := p.framerCfg.SamplesPerChirp
numChannels := p.framerCfg.RxChannels

if numChirps <= 0 {
numChirps = len(rawFrame.IFData) / numChannels
if numChirps <= 0 {
numChirps = 128
}
}
if numSamples <= 0 {
numSamples = len(rawFrame.IFData[0])
if numSamples <= 0 {
numSamples = 256
}
}
if numChannels <= 0 {
numChannels = 1
}

rangeBins := p.cfg.RangeFFTSize
dopplerBins := p.cfg.DopplerFFTSize

rangeSize := rangeBins
dopplerSize := dopplerBins

rangeWindow := createWindow(rangeSize, p.cfg.WindowType, p.cfg.WindowAlpha)
dopplerWindow := createWindow(dopplerSize, p.cfg.WindowType, p.cfg.WindowAlpha)

workBuf := make([]complex128, maxInt(rangeSize, dopplerSize))
rangeBitReverse := bitReverseIndices(rangeSize)
dopplerBitReverse := bitReverseIndices(dopplerSize)
rangeTwiddle := make([]complex128, rangeSize/2)
for i := 0; i < rangeSize/2; i++ {
angle := -2.0 * math.Pi * float64(i) / float64(rangeSize)
rangeTwiddle[i] = complex(math.Cos(angle), math.Sin(angle))
}
dopplerTwiddle := make([]complex128, dopplerSize/2)
for i := 0; i < dopplerSize/2; i++ {
angle := -2.0 * math.Pi * float64(i) / float64(dopplerSize)
dopplerTwiddle[i] = complex(math.Cos(angle), math.Sin(angle))
}

for chirp := 0; chirp < numChirps; chirp++ {
for ch := 0; ch < numChannels; ch++ {
idx := (chirp*numChannels + ch) * rangeSize

if ch*numChirps+chirp >= len(rawFrame.IFData) {
for i := 0; i < rangeSize; i++ {
workBuf[rangeBitReverse[i]] = 0
}
} else {
chData := rawFrame.IFData[ch*numChirps+chirp]
for i := 0; i < numSamples && i < rangeSize && i < len(chData); i++ {
sample := float64(chData[i])
workBuf[rangeBitReverse[i]] = complex(sample*rangeWindow[i], 0.0)
}
for i := numSamples; i < rangeSize; i++ {
workBuf[rangeBitReverse[i]] = 0
}
}

fftInPlace(workBuf, rangeSize, rangeTwiddle)

for i := 0; i < rangeSize; i++ {
fftResult.RangeProfile[idx+i] = cmplxAbs(workBuf[i])
}
}
}

for rangeBin := 0; rangeBin < rangeSize; rangeBin++ {
for ch := 0; ch < numChannels; ch++ {
for chirp := 0; chirp < numChirps && chirp < dopplerSize; chirp++ {
idx := chirp*numChannels*rangeSize + ch*rangeSize + rangeBin
workBuf[dopplerBitReverse[chirp]] = complex(fftResult.RangeProfile[idx]*dopplerWindow[chirp], 0.0)
}
for chirp := numChirps; chirp < dopplerSize; chirp++ {
workBuf[dopplerBitReverse[chirp]] = 0
}

fftInPlace(workBuf, dopplerSize, dopplerTwiddle)

for chirp := 0; chirp < dopplerSize; chirp++ {
rdIdx := (chirp*numChannels+ch)*rangeSize + rangeBin
fftResult.RDMatrix[rdIdx] = cmplxAbs(workBuf[chirp])
}
}
}

if p.cfg.EnableFFTShift && rangeBins > 1 && dopplerBins > 1 {
fftShift(fftResult.RDMatrix, dopplerBins, rangeBins)
}

for d := 0; d < dopplerBins; d++ {
for r := 0; r < rangeBins; r++ {
fftResult.RangeDoppler[d][r] = complex(fftResult.RDMatrix[d*rangeBins+r], 0.0)
}
}

return fftResult, nil
}

func (p *Processor) DetectPeaks(fftResult *model.FFTResult) ([]model.PeakInfo, error) {
rangeBins := fftResult.RangeBins
dopplerBins := fftResult.DopplerBins

peaks := p.fft.CFARDetect(
fftResult.RDMatrix,
dopplerBins, rangeBins,
p.cfg.CFARGuardCells,
p.cfg.CFARTrainCells,
p.cfg.CFARThreshold,
p.peakBuf,
)

for i := range peaks {
peak := &peaks[i]
peak.DistanceM = rangeBinToDistance(peak.RangeBin, rangeBins,
p.framerCfg.SamplesPerChirp,
p.framerCfg.ChirpsPerFrame,
p.cfg.RangeFFTSize,
24.0, 250.0, 12.5)
peak.VelocityMPS = dopplerBinToVelocity(peak.DopplerBin, dopplerBins,
24.0, 12.5e6, 256)
if peak.Amplitude == 0 {
peak.Amplitude = peak.Magnitude
}
}

return peaks, nil
}

func (p *Processor) GetStats() ProcessorStats {
p.mu.Lock()
defer p.mu.Unlock()

stats := p.stats
if stats.FramesProcessed > 0 {
stats.FFTTimeNs /= stats.FramesProcessed
}

return stats
}

func (p *Processor) Close() error {
p.cancel()
p.wg.Wait()
p.fft.Close()

stats := p.GetStats()
p.logger.Info("FFT processor stopped",
zap.Uint64("frames_processed", stats.FramesProcessed),
zap.Uint64("avg_fft_time_ns", stats.FFTTimeNs),
zap.Uint64("errors", stats.Errors),
zap.Uint64("dropped_frames", stats.DroppedFrames),
)

return nil
}

func createWindow(n int, windowType string, alpha float64) []float64 {
window := make([]float64, n)

switch windowType {
case "hann", "hanning":
for i := 0; i < n; i++ {
window[i] = 0.5 * (1.0 - math.Cos(2.0*math.Pi*float64(i)/float64(n-1)))
}
case "hamming":
for i := 0; i < n; i++ {
window[i] = 0.54 - 0.46*math.Cos(2.0*math.Pi*float64(i)/float64(n-1))
}
case "blackman":
for i := 0; i < n; i++ {
t := 2.0 * math.Pi * float64(i) / float64(n-1)
window[i] = 0.42 - 0.5*math.Cos(t) + 0.08*math.Cos(2.0*t)
}
default:
for i := 0; i < n; i++ {
window[i] = 1.0
}
}

return window
}

func bitReverseIndices(n int) []int {
indices := make([]int, n)
bits := 0
for k := n; k > 1; k >>= 1 {
bits++
}
for i := 0; i < n; i++ {
rev := 0
j := i
for b := 0; b < bits; b++ {
rev = (rev << 1) | (j & 1)
j >>= 1
}
indices[i] = rev
}
return indices
}

func fftInPlace(data []complex128, n int, twiddle []complex128) {
for size := 2; size <= n; size <<= 1 {
halfSize := size >> 1
twiddleStep := n / size

for i := 0; i < n; i += size {
k := 0
for j := 0; j < halfSize; j++ {
even := data[i+j]
odd := data[i+j+halfSize] * twiddle[k]
data[i+j] = even + odd
data[i+j+halfSize] = even - odd
k += twiddleStep
}
}
}
}

func fftShift(data []float64, rows, cols int) {
midRow := rows / 2
midCol := cols / 2

tmp := make([]float64, cols)

for i := 0; i < midRow; i++ {
copy(tmp, data[i*cols:(i+1)*cols])
copy(data[i*cols:(i+1)*cols], data[(i+midRow)*cols:(i+midRow+1)*cols])
copy(data[(i+midRow)*cols:(i+midRow+1)*cols], tmp)
}

for row := 0; row < rows; row++ {
rowStart := row * cols
for i := 0; i < midCol; i++ {
tmpVal := data[rowStart+i]
data[rowStart+i] = data[rowStart+i+midCol]
data[rowStart+i+midCol] = tmpVal
}
}
}

func rangeBinToDistance(rangeBin, rangeBins, samplesPerChirp, chirpsPerFrame, fftSize int, startFreqGHz, bandwidthGHz, sampleRateMHz float64) float64 {
c := 299792458.0
bandwidthHz := bandwidthGHz * 1e9

binResolution := c / (2 * bandwidthHz)
nyquistRange := binResolution * float64(fftSize) / 2

return float64(rangeBin) * nyquistRange / float64(fftSize/2)
}

func dopplerBinToVelocity(dopplerBin, dopplerBins int, startFreqGHz float64, sampleRateHz float64, samplesPerChirp int) float64 {
c := 299792458.0
startFreqHz := startFreqGHz * 1e9
wavelength := c / startFreqHz
prf := sampleRateHz / float64(samplesPerChirp)

velocityResolution := wavelength * prf / (2 * float64(dopplerBins))
nyquistVelocity := velocityResolution * float64(dopplerBins) / 2

return (float64(dopplerBin) - float64(dopplerBins)/2) * nyquistVelocity / float64(dopplerBins/2)
}