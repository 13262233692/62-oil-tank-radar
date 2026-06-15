package fft

import (
"fmt"
"math"

"github.com/oil-tank-radar/gateway/internal/config"
"github.com/oil-tank-radar/gateway/pkg/model"
)

type GoFFT struct {
cfg               config.FFTConfig
rangeFFTSize      int
dopplerFFTSize    int
rangeTwiddle      []complex128
dopplerTwiddle    []complex128
rangeWindow       []float64
dopplerWindow     []float64
rangeBitReverse   []int
dopplerBitReverse []int
}

func NewCGOFFT(cfg config.FFTConfig) *GoFFT {
return &GoFFT{
cfg:            cfg,
rangeFFTSize:   cfg.RangeFFTSize,
dopplerFFTSize: cfg.DopplerFFTSize,
}
}

func (f *GoFFT) Init() error {
if f.rangeFFTSize <= 0 || (f.rangeFFTSize&(f.rangeFFTSize-1)) != 0 {
return fmt.Errorf("range FFT size must be power of 2, got %d", f.rangeFFTSize)
}
if f.dopplerFFTSize <= 0 || (f.dopplerFFTSize&(f.dopplerFFTSize-1)) != 0 {
return fmt.Errorf("doppler FFT size must be power of 2, got %d", f.dopplerFFTSize)
}

f.rangeTwiddle = make([]complex128, f.rangeFFTSize/2)
for i := 0; i < f.rangeFFTSize/2; i++ {
angle := -2.0 * math.Pi * float64(i) / float64(f.rangeFFTSize)
f.rangeTwiddle[i] = complex(math.Cos(angle), math.Sin(angle))
}

f.dopplerTwiddle = make([]complex128, f.dopplerFFTSize/2)
for i := 0; i < f.dopplerFFTSize/2; i++ {
angle := -2.0 * math.Pi * float64(i) / float64(f.dopplerFFTSize)
f.dopplerTwiddle[i] = complex(math.Cos(angle), math.Sin(angle))
}

f.rangeBitReverse = f.bitReverseIndices(f.rangeFFTSize)
f.dopplerBitReverse = f.bitReverseIndices(f.dopplerFFTSize)

f.rangeWindow = f.createWindow(f.rangeFFTSize, f.cfg.WindowType, f.cfg.WindowAlpha)
f.dopplerWindow = f.createWindow(f.dopplerFFTSize, f.cfg.WindowType, f.cfg.WindowAlpha)

return nil
}

func (f *GoFFT) bitReverseIndices(n int) []int {
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

func (f *GoFFT) createWindow(n int, windowType string, alpha float64) []float64 {
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
case "kaiser":
if alpha <= 0 {
alpha = 0.5
}
beta := alpha * math.Pi
denom := f.besselI0(beta)
for i := 0; i < n; i++ {
x := 2.0*float64(i)/float64(n-1) - 1.0
arg := beta * math.Sqrt(1.0-x*x)
window[i] = f.besselI0(arg) / denom
}
default:
for i := 0; i < n; i++ {
window[i] = 1.0
}
}

return window
}

func (f *GoFFT) besselI0(x float64) float64 {
ax := math.Abs(x)
if ax < 3.75 {
y := x / 3.75
y *= y
return 1.0 + y*(3.5156229+y*(3.0899424+y*(1.2067492+y*(0.2659732+y*(0.360768e-1+y*0.45813e-2)))))
}
y := 3.75 / ax
return (math.Exp(ax) / math.Sqrt(ax)) * (0.39894228 + y*(0.1328592e-1+y*(0.225319e-2+y*(-0.157565e-2+y*(0.916281e-2+y*(-0.2057706e-1+y*(0.2635537e-1+y*(-0.1647633e-1+y*0.392377e-2))))))))
}

func (f *GoFFT) RangeDopplerFFT(
rawFrame *model.FMCWRawFrame,
result *model.FFTResult,
numChirps, numSamples, numChannels int,
) {
rangeSize := f.rangeFFTSize
dopplerSize := f.dopplerFFTSize

workBuf := make([]complex128, maxInt(rangeSize, dopplerSize))

for chirp := 0; chirp < numChirps; chirp++ {
for ch := 0; ch < numChannels; ch++ {
idx := (chirp*numChannels + ch) * rangeSize

if ch*numChirps+chirp >= len(rawFrame.IFData) {
for i := 0; i < rangeSize; i++ {
workBuf[f.rangeBitReverse[i]] = 0
}
} else {
chData := rawFrame.IFData[ch*numChirps+chirp]
for i := 0; i < numSamples && i < rangeSize && i < len(chData); i++ {
sample := float64(chData[i])
workBuf[f.rangeBitReverse[i]] = complex(sample*f.rangeWindow[i], 0.0)
}
for i := numSamples; i < rangeSize; i++ {
workBuf[f.rangeBitReverse[i]] = 0
}
}

f.fftInPlace(workBuf, rangeSize, f.rangeTwiddle)

for i := 0; i < rangeSize; i++ {
result.RangeProfile[idx+i] = cmplxAbs(workBuf[i])
}
}
}

for rangeBin := 0; rangeBin < rangeSize; rangeBin++ {
for ch := 0; ch < numChannels; ch++ {
for chirp := 0; chirp < numChirps && chirp < dopplerSize; chirp++ {
idx := chirp*numChannels*rangeSize + ch*rangeSize + rangeBin
workBuf[f.dopplerBitReverse[chirp]] = complex(result.RangeProfile[idx]*f.dopplerWindow[chirp], 0.0)
}
for chirp := numChirps; chirp < dopplerSize; chirp++ {
workBuf[f.dopplerBitReverse[chirp]] = 0
}

f.fftInPlace(workBuf, dopplerSize, f.dopplerTwiddle)

for chirp := 0; chirp < dopplerSize; chirp++ {
rdIdx := (chirp*numChannels+ch)*rangeSize + rangeBin
result.RDMatrix[rdIdx] = cmplxAbs(workBuf[chirp])
}
}
}
}

func (f *GoFFT) fftInPlace(data []complex128, n int, twiddle []complex128) {
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

func (f *GoFFT) FFTShift(data []float64, rows, cols int) {
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

func (f *GoFFT) CFARDetect(
data []float64,
rows, cols int,
guardCells, trainCells int,
threshold float64,
peaks []model.PeakInfo,
) []model.PeakInfo {
peaks = peaks[:0]

for row := guardCells; row < rows-guardCells; row++ {
for col := guardCells; col < cols-guardCells; col++ {
idx := row*cols + col
current := data[idx]

var sum float64
var count int

for dr := -guardCells - trainCells; dr <= guardCells+trainCells; dr++ {
for dc := -guardCells - trainCells; dc <= guardCells+trainCells; dc++ {
if dr >= -guardCells && dr <= guardCells && dc >= -guardCells && dc <= guardCells {
continue
}
nr := row + dr
nc := col + dc
if nr >= 0 && nr < rows && nc >= 0 && nc < cols {
sum += data[nr*cols+nc]
count++
}
}
}

if count > 0 {
avg := sum / float64(count)
if current > avg*threshold {
peak := model.PeakInfo{
RangeBin:   col,
DopplerBin: row,
Amplitude:  current,
Magnitude:  current,
SNR:        current / maxFloat(avg, 1e-10),
}
peaks = append(peaks, peak)
}
}
}
}

return peaks
}

func (f *GoFFT) PhaseUnwrap(phases []float64, threshold float64) {
if len(phases) < 2 {
return
}

for i := 1; i < len(phases); i++ {
diff := phases[i] - phases[i-1]
for diff > threshold {
phases[i] -= 2 * math.Pi
diff = phases[i] - phases[i-1]
}
for diff < -threshold {
phases[i] += 2 * math.Pi
diff = phases[i] - phases[i-1]
}
}
}

func (f *GoFFT) Close() error {
f.rangeTwiddle = nil
f.dopplerTwiddle = nil
f.rangeWindow = nil
f.dopplerWindow = nil
f.rangeBitReverse = nil
f.dopplerBitReverse = nil
return nil
}

func maxInt(a, b int) int {
if a > b {
return a
}
return b
}

func maxFloat(a, b float64) float64 {
if a > b {
return a
}
return b
}

func cmplxAbs(c complex128) float64 {
return math.Hypot(real(c), imag(c))
}