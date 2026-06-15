//go:build cgo && ignore

package fft

/*
#cgo CFLAGS: -I./fftwrap -O3 -ffast-math
#cgo LDFLAGS: -lm
#include <stdlib.h>
#include <string.h>
#include "fftwrap/fft2d.h"
*/
import "C"

import (
"math"
"sync"
"unsafe"

"github.com/oil-tank-radar/gateway/internal/config"
"github.com/oil-tank-radar/gateway/pkg/model"
)

var (
fftInitOnce   sync.Once
fftInitialized bool
fftInitErr    error
)

type CGOFFT struct {
cfg       C.FFTConfig
peaks     []C.PeakInfoC
maxPeaks  int
}

func NewCGOFFT(cfg config.FFTConfig) *CGOFFT {
c := &CGOFFT{
maxPeaks: 100,
peaks:    make([]C.PeakInfoC, 100),
}

c.cfg.range_fft_size = C.int(cfg.RangeFFTSize)
c.cfg.doppler_fft_size = C.int(cfg.DopplerFFTSize)
c.cfg.window_type = C.int(windowTypeToInt(cfg.WindowType))
c.cfg.window_alpha = C.double(cfg.WindowAlpha)
c.cfg.cfar_guard_cells = C.int(cfg.CFARGuardCells)
c.cfg.cfar_train_cells = C.int(cfg.CFARTrainCells)
c.cfg.cfar_threshold = C.double(cfg.CFARThreshold)

return c
}

func windowTypeToInt(windowType string) int {
switch windowType {
case "hann", "Hann":
return 0
case "hamming", "Hamming":
return 1
case "blackman", "Blackman":
return 2
case "kaiser", "Kaiser":
return 3
default:
return 4
}
}

func (f *CGOFFT) Init() error {
fftInitOnce.Do(func() {
ret := C.fft_init(&f.cfg)
if ret != 0 {
fftInitErr = C.int(ret)
} else {
fftInitialized = true
}
})
return fftInitErr
}

func (f *CGOFFT) Cleanup() {
if fftInitialized {
C.fft_cleanup()
fftInitialized = false
}
}

func (f *CGOFFT) FFT1D(data []complex128, inverse bool) {
if len(data) == 0 {
return
}

n := C.int(len(data))
inv := C.int(0)
if inverse {
inv = 1
}

C.fft_1d((*C.complex128_t)(unsafe.Pointer(&data[0])), n, inv)
}

func (f *CGOFFT) FFT2D(data []complex128, rows, cols int, inverse bool) {
if len(data) == 0 || rows*cols > len(data) {
return
}

r := C.int(rows)
c := C.int(cols)
inv := C.int(0)
if inverse {
inv = 1
}

C.fft_2d((*C.complex128_t)(unsafe.Pointer(&data[0])), r, c, inv)
}

func (f *CGOFFT) RangeDopplerFFT(
input *model.FMCWRawFrame,
output *model.FFTResult,
numChirps, numSamples, numChannels int,
) {
if len(input.IFData) == 0 || len(output.RangeDoppler) == 0 {
return
}

var flatInput []C.int16_t
totalSamples := numChirps * numSamples * numChannels
flatInput = make([]C.int16_t, totalSamples)

idx := 0
for ch := 0; ch < numChannels; ch++ {
for chirp := 0; chirp < numChirps; chirp++ {
for sample := 0; sample < numSamples; sample++ {
if ch < len(input.IFData) && idx < len(input.IFData[ch]) {
flatInput[idx] = C.int16_t(input.IFData[ch][chirp*numSamples+sample])
}
idx++
}
}
}

var flatOutput []C.complex128_t
outputSize := f.cfg.doppler_fft_size * f.cfg.range_fft_size * C.int(numChannels)
flatOutput = make([]C.complex128_t, outputSize)

C.range_doppler_fft(
(*C.int16_t)(unsafe.Pointer(&flatInput[0])),
(*C.complex128_t)(unsafe.Pointer(&flatOutput[0])),
C.int(numChirps),
C.int(numSamples),
C.int(numChannels),
&f.cfg,
)

for doppler := 0; doppler < int(f.cfg.doppler_fft_size); doppler++ {
for rng := 0; rng < int(f.cfg.range_fft_size); rng++ {
ch := 0
idx := (doppler*numChannels + ch) * int(f.cfg.range_fft_size) + rng
if idx < len(flatOutput) && doppler < len(output.RangeDoppler) && rng < len(output.RangeDoppler[doppler]) {
output.RangeDoppler[doppler][rng] = complex128(flatOutput[idx])
}
}
}
}

func (f *CGOFFT) CFAR(
rdMatrix []complex128,
rangeBins, dopplerBins int,
) ([]model.PeakInfo, error) {
if len(rdMatrix) == 0 {
return nil, nil
}

result := C.PeakDetectionResult{
peaks:     (*C.PeakInfoC)(unsafe.Pointer(&f.peaks[0])),
num_peaks: 0,
max_peaks: C.int(f.maxPeaks),
}

C.cfar_detector(
(*C.complex128_t)(unsafe.Pointer(&rdMatrix[0])),
C.int(rangeBins),
C.int(dopplerBins),
&result,
&f.cfg,
)

peaks := make([]model.PeakInfo, result.num_peaks)
for i := 0; i < int(result.num_peaks); i++ {
p := f.peaks[i]
peaks[i] = model.PeakInfo{
RangeBin:    int(p.range_bin),
DopplerBin:  int(p.doppler_bin),
Magnitude:   float64(p.magnitude),
Phase:       float64(p.phase),
SNR:         float64(p.snr),
DistanceM:   float64(p.distance_m),
VelocityMPS: float64(p.velocity_mps),
Confidence:  float64(p.confidence),
}
}

return peaks, nil
}

func (f *CGOFFT) PhaseUnwrap(inputPhases []float64) []float64 {
if len(inputPhases) == 0 {
return nil
}

n := C.int(len(inputPhases))
output := make([]float64, len(inputPhases))

C.phase_unwrap(
(*C.double)(unsafe.Pointer(&inputPhases[0])),
(*C.double)(unsafe.Pointer(&output[0])),
n,
)

return output
}

func (f *CGOFFT) MagnitudeSpectrum(input []complex128) []float64 {
if len(input) == 0 {
return nil
}

n := C.int(len(input))
output := make([]float64, len(input))

C.magnitude_spectrum(
(*C.complex128_t)(unsafe.Pointer(&input[0])),
(*C.double)(unsafe.Pointer(&output[0])),
n,
)

return output
}

func (f *CGOFFT) LogMagnitude(input []float64, ref float64) []float64 {
if len(input) == 0 {
return nil
}

n := C.int(len(input))
output := make([]float64, len(input))

C.log_magnitude(
(*C.double)(unsafe.Pointer(&input[0])),
(*C.double)(unsafe.Pointer(&output[0])),
n,
C.double(ref),
)

return output
}

func (f *CGOFFT) NextPowerOfTwo(n int) int {
return int(C.next_power_of_two(C.int(n)))
}

func (f *CGOFFT) FFTShift(data []complex128, rows, cols int) {
if len(data) == 0 || rows*cols > len(data) {
return
}

C.fft_shift(
(*C.complex128_t)(unsafe.Pointer(&data[0])),
C.int(rows),
C.int(cols),
)
}

func (f *CGOFFT) ZeroPad(input []complex128, inputRows, inputCols int, outputRows, outputCols int) []complex128 {
output := make([]complex128, outputRows*outputCols)

C.zero_pad(
(*C.complex128_t)(unsafe.Pointer(&input[0])),
(*C.complex128_t)(unsafe.Pointer(&output[0])),
C.int(inputRows),
C.int(inputCols),
C.int(outputRows),
C.int(outputCols),
)

return output
}

func (f *CGOFFT) ApplyWindow(data []complex128, windowType string, alpha float64) {
if len(data) == 0 {
return
}

wt := C.int(windowTypeToInt(windowType))
n := C.int(len(data))

C.window_apply(
(*C.complex128_t)(unsafe.Pointer(&data[0])),
n,
wt,
C.double(alpha),
)
}

func RangeBinToDistance(rangeBin int, rangeBins int, sampleRateMHz, bandwidthGHz float64) float64 {
c := 299792458.0
bandwidthHz := bandwidthGHz * 1e9
sampleRateHz := sampleRateMHz * 1e6

binResolution := c / (2 * bandwidthHz)
nyquistRange := binResolution * float64(rangeBins) / 2

return float64(rangeBin) * nyquistRange / float64(rangeBins/2)
}

func DopplerBinToVelocity(dopplerBin int, dopplerBins int, startFreqGHz, chirpDurationSec float64) float64 {
c := 299792458.0
startFreqHz := startFreqGHz * 1e9
wavelength := c / startFreqHz
prf := 1.0 / chirpDurationSec

velocityResolution := wavelength * prf / (2 * float64(dopplerBins))
nyquistVelocity := velocityResolution * float64(dopplerBins) / 2

return (float64(dopplerBin) - float64(dopplerBins)/2) * nyquistVelocity / float64(dopplerBins/2)
}

func CalculateSNR(signalMagnitude, noiseFloor float64) float64 {
if noiseFloor <= 0 {
noiseFloor = 1e-10
}
return 20 * math.Log10(signalMagnitude/noiseFloor)
}