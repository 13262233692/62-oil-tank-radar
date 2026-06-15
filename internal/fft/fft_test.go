package fft

import (
	"math"
	"testing"

	"github.com/oil-tank-radar/gateway/internal/config"
	"github.com/oil-tank-radar/gateway/pkg/model"
)

func newTestConfig(rows, cols int) config.FFTConfig {
	return config.FFTConfig{
		RangeFFTSize:   cols,
		DopplerFFTSize: rows,
		WindowType:     "hann",
		WindowAlpha:    0.5,
		CFARGuardCells: 2,
		CFARTrainCells: 8,
		CFARThreshold:  3.0,
		EnableFFTShift: true,
	}
}

func newTestFFT(rows, cols int) *GoFFT {
	cfg := newTestConfig(rows, cols)
	fft := NewCGOFFT(cfg)
	fft.Init()
	return fft
}

func complexAbsDiff(a, b complex128) float64 {
	d := a - b
	return math.Hypot(real(d), imag(d))
}

func TestFFT1D_PeakDetect(t *testing.T) {
	size := 64
	fft := newTestFFT(size, size)
	defer fft.Close()

	data := make([]complex128, size)
	freq := 5.0
	for i := 0; i < size; i++ {
		phase := 2.0 * math.Pi * freq * float64(i) / float64(size)
		data[i] = complex(math.Cos(phase), math.Sin(phase))
	}

	reversed := make([]complex128, size)
	for i, r := range fft.rangeBitReverse {
		reversed[r] = data[i]
	}

	fftInPlace(reversed, size, fft.rangeTwiddle)

	magMax := 0.0
	peakIdx := 0
	for i := 1; i < size/2; i++ {
		mag := cmplxAbs(reversed[i])
		if mag > magMax {
			magMax = mag
			peakIdx = i
		}
	}

	t.Logf("FFT peak at bin %d (expected ~%d), magnitude %.2f", peakIdx, int(freq), magMax)
	if peakIdx < 4 || peakIdx > 6 {
		t.Errorf("Expected peak around bin 5, got %d", peakIdx)
	}
	if magMax < 10 {
		t.Errorf("Peak magnitude too small: %.2f", magMax)
	}
}

func TestGoFFT_RangeDopplerFFT(t *testing.T) {
	rows := 64
	cols := 128
	fft := newTestFFT(rows, cols)
	defer fft.Close()

	rawFrame := &model.FMCWRawFrame{}
	rawFrame.IFData = make([][]int16, rows)
	for i := range rawFrame.IFData {
		rawFrame.IFData[i] = make([]int16, cols)
		for j := range rawFrame.IFData[i] {
			rawFrame.IFData[i][j] = int16(200 * math.Sin(2*math.Pi*float64(j)*10/float64(cols)))
		}
	}

	fftResult := &model.FFTResult{
		RangeDoppler: make([][]complex128, rows),
		RangeProfile: make([]float64, rows*cols),
		RDMatrix:     make([]float64, rows*cols),
	}
	for i := range fftResult.RangeDoppler {
		fftResult.RangeDoppler[i] = make([]complex128, cols)
	}

	fft.RangeDopplerFFT(rawFrame, fftResult, rows, cols, 1)

	peakIdx := 0
	peakVal := 0.0
	for i, v := range fftResult.RangeProfile[:cols] {
		if v > peakVal {
			peakVal = v
			peakIdx = i
		}
	}

	t.Logf("Range-Doppler FFT: peak at bin %d (expected ~%d), value %.2f",
		peakIdx, 10, peakVal)

	if len(fftResult.RangeProfile) == 0 {
		t.Error("RangeProfile should not be empty")
	}
	if peakVal < 1000 {
		t.Errorf("Peak value too small: %.2f", peakVal)
	}
}

func TestWindowFunctions(t *testing.T) {
	tests := []struct {
		name   string
		wtype  string
	}{
		{"hann", "hann"},
		{"hamming", "hamming"},
		{"blackman", "blackman"},
		{"none", "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := 256
			w := createWindow(n, tt.wtype, 0.5)
			if len(w) != n {
				t.Errorf("Window length mismatch: expected %d, got %d", n, len(w))
			}
			mid := w[n/2]
			edge := w[0]
			if mid < 0.95 {
				t.Errorf("Window %s: center should be near 1.0, got %.4f", tt.wtype, mid)
			}
			if tt.wtype != "none" && edge > mid*0.5 {
				t.Errorf("Window %s: edge (%.4f) should be smaller than center*0.5 (%.4f)",
					tt.wtype, edge, mid*0.5)
			}
			t.Logf("Window %s: center=%.4f, edge=%.4f", tt.wtype, mid, edge)
		})
	}
}

func TestBitReverse(t *testing.T) {
	indices := bitReverseIndices(8)
	expected := []int{0, 4, 2, 6, 1, 5, 3, 7}
	for i, v := range indices {
		if v != expected[i] {
			t.Errorf("bitReverse[8][%d] = %d, expected %d", i, v, expected[i])
		}
	}
	t.Log("Bit reverse indices test passed")
}

func TestRangeBinToDistance(t *testing.T) {
	dist := RangeBinToDistance(
		100,
		256,
		256,
		128,
		256,
		24.0,
		1.5,
		5.0,
	)

	t.Logf("Range bin 100 -> %.4f m", dist)
	if dist < 1 || dist > 50 {
		t.Errorf("Distance out of reasonable range: %.4f m", dist)
	}

	zero := RangeBinToDistance(0, 256, 256, 128, 256, 24.0, 1.5, 5.0)
	if zero > 0.01 {
		t.Errorf("Bin 0 should map to near 0, got %.4f", zero)
	}

	nyquist := RangeBinToDistance(128, 256, 256, 128, 256, 24.0, 1.5, 5.0)
	t.Logf("Range bin 128 (Nyquist) -> %.4f m", nyquist)
	if nyquist < 5 {
		t.Errorf("Nyquist bin should give meaningful distance, got %.4f", nyquist)
	}
}

func TestDopplerBinToVelocity(t *testing.T) {
	zeroVel := DopplerBinToVelocity(64, 128, 24.0, 5000.0, 256)
	t.Logf("Doppler bin 64 (center) -> %.6f m/s", zeroVel)
	if math.Abs(zeroVel) > 0.001 {
		t.Errorf("Center bin should be near 0 velocity, got %.6f", zeroVel)
	}

	posVel := DopplerBinToVelocity(96, 128, 24.0, 5000.0, 256)
	t.Logf("Doppler bin 96 -> %.6f m/s (should be positive)", posVel)
	if posVel <= 0 {
		t.Errorf("Bin 96 should have positive velocity, got %.6f", posVel)
	}
}

func TestCFARDetect(t *testing.T) {
	rows := 32
	cols := 64
	fft := newTestFFT(rows, cols)
	defer fft.Close()

	data := make([]float64, rows*cols)
	peakRow, peakCol := 16, 32
	data[peakRow*cols+peakCol] = 100.0
	data[peakRow*cols+peakCol+1] = 50.0

	peaks := make([]model.PeakInfo, 0, 10)
	result := fft.CFARDetect(data, rows, cols, 1, 4, 2.0, peaks)

	t.Logf("CFAR detected %d peaks", len(result))
	if len(result) == 0 {
		t.Error("Should detect at least one peak")
	}

	for _, p := range result {
		t.Logf("  Peak at (range=%d, doppler=%d) mag=%.2f snr=%.2fdB",
			p.RangeBin, p.DopplerBin, p.Magnitude, p.SNR)
	}
}

func TestFFTShift(t *testing.T) {
	rows := 4
	cols := 4
	data := make([]float64, rows*cols)
	for i := range data {
		data[i] = float64(i)
	}

	orig := make([]float64, len(data))
	copy(orig, data)

	fftShift(data, rows, cols)
	fftShift(data, rows, cols)

	for i := range data {
		if data[i] != orig[i] {
			t.Errorf("Double FFT shift mismatch at %d: expected %v, got %v",
				i, orig[i], data[i])
		}
	}
	t.Log("FFT shift double-inversion test passed")
}

func BenchmarkFFT1D_256(b *testing.B) {
	size := 256
	fft := newTestFFT(size, size)
	defer fft.Close()
	data := make([]complex128, size)
	for i := range data {
		data[i] = complex(float64(i)/float64(size), 0)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := make([]complex128, size)
		copy(buf, data)
		reversed := make([]complex128, size)
		for j, r := range fft.rangeBitReverse {
			reversed[r] = buf[j]
		}
		fftInPlace(reversed, size, fft.rangeTwiddle)
	}
}

func BenchmarkRangeDopplerFFT_64x128(b *testing.B) {
	rows := 64
	cols := 128
	fft := newTestFFT(rows, cols)
	defer fft.Close()

	rawFrame := &model.FMCWRawFrame{}
	rawFrame.IFData = make([][]int16, rows)
	for i := range rawFrame.IFData {
		rawFrame.IFData[i] = make([]int16, cols)
		for j := range rawFrame.IFData[i] {
			rawFrame.IFData[i][j] = int16(j)
		}
	}

	fftResult := &model.FFTResult{
		RangeDoppler: make([][]complex128, rows),
		RangeProfile: make([]float64, rows*cols),
		RDMatrix:     make([]float64, rows*cols),
	}
	for i := range fftResult.RangeDoppler {
		fftResult.RangeDoppler[i] = make([]complex128, cols)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fft.RangeDopplerFFT(rawFrame, fftResult, rows, cols, 1)
	}
}
