package model

import (
"math/cmplx"
"sync/atomic"
"time"
)

type UDPFrame struct {
Data      []byte
Length    int
Timestamp time.Time
SourceIP  string
refCount  int32
}

func (f *UDPFrame) Ref() {
atomic.AddInt32(&f.refCount, 1)
}

func (f *UDPFrame) Unref() {
if atomic.AddInt32(&f.refCount, -1) == 0 {
f.Data = f.Data[:0]
}
}

type FMCWRawFrame struct {
Header      []byte
IFData      [][]int16
Timestamp   time.Time
FrameNumber uint64
refCount    int32
}

func (f *FMCWRawFrame) Ref() {
atomic.AddInt32(&f.refCount, 1)
}

func (f *FMCWRawFrame) Unref() {
if atomic.AddInt32(&f.refCount, -1) == 0 {
f.Header = f.Header[:0]
for i := range f.IFData {
f.IFData[i] = f.IFData[i][:0]
}
f.IFData = nil
}
}

type IFMatrix struct {
Data     []complex128
Rows     int
Cols     int
refCount int32
}

func (m *IFMatrix) Ref() {
atomic.AddInt32(&m.refCount, 1)
}

func (m *IFMatrix) Unref() {
if atomic.AddInt32(&m.refCount, -1) == 0 {
m.Data = m.Data[:0]
}
}

func (m *IFMatrix) At(row, col int) complex128 {
return m.Data[row*m.Cols+col]
}

func (m *IFMatrix) Set(row, col int, val complex128) {
m.Data[row*m.Cols+col] = val
}

type FFTResult struct {
	RangeDoppler   [][]complex128
	RangeProfile   []float64
	RDMatrix       []float64
	RangeBins      int
	DopplerBins    int
	Timestamp      time.Time
	FrameNumber    uint64
	PeakRangeIdx   int
	PeakDopplerIdx int
	refCount       int32
}

func (r *FFTResult) Ref() {
atomic.AddInt32(&r.refCount, 1)
}

func (r *FFTResult) Unref() {
if atomic.AddInt32(&r.refCount, -1) == 0 {
for i := range r.RangeDoppler {
r.RangeDoppler[i] = r.RangeDoppler[i][:0]
}
r.RangeDoppler = nil
r.RangeProfile = nil
r.RDMatrix = nil
}
}

func (r *FFTResult) Magnitude(rangeBin, dopplerBin int) float64 {
return cmplx.Abs(r.RangeDoppler[dopplerBin][rangeBin])
}

type PeakInfo struct {
	RangeBin         int
	DopplerBin       int
	RangeSubPixel    float64
	DopplerSubPixel  float64
	SubPixelValid    bool
	SubPixelMethod   string
	ConditionNumber  float64
	Magnitude        float64
	Amplitude        float64
	Phase            float64
	SNR              float64
	DistanceM        float64
	VelocityMPS      float64
	Confidence       float64
	CoherenceScore   float64
}

type LevelMeasurement struct {
	Timestamp     time.Time
	FrameNumber   uint64
	DistanceM     float64
	LevelM        float64
	VolumeM3      float64
	VelocityMPS   float64
	TemperatureC  float64
	SNR           float64
	SNRdB         float64
	Confidence    float64
	WaveHeightM   float64
	PeakInfo      PeakInfo
	Valid         bool
	Status        string
	RawData       []byte
	refCount      int32
}

func (m *LevelMeasurement) Ref() {
	atomic.AddInt32(&m.refCount, 1)
}

func (m *LevelMeasurement) Unref() {
	if atomic.AddInt32(&m.refCount, -1) == 0 {
		m.RawData = nil
	}
}

type WaveMeasurement struct {
Timestamp    time.Time
HeightM      float64
PeriodS      float64
DirectionDeg float64
Valid        bool
}