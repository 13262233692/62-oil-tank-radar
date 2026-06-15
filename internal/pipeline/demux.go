package pipeline

import (
"context"
"sync"
"time"

"github.com/oil-tank-radar/gateway/internal/config"
"github.com/oil-tank-radar/gateway/internal/fft"
"github.com/oil-tank-radar/gateway/internal/ranging"
"github.com/oil-tank-radar/gateway/internal/tsdb"
"github.com/oil-tank-radar/gateway/pkg/model"
"go.uber.org/zap"
)

type Demux struct {
cfg         config.Config
logger      *zap.Logger
fftProc     *fft.Processor
rangingCalc *ranging.Calculator
storage     *tsdb.Storage
inCh        <-chan *model.FFTResult
measurementCh chan<- *model.LevelMeasurement
wg          sync.WaitGroup
ctx         context.Context
cancel      context.CancelFunc
stats       DemuxStats
mu          sync.Mutex
backpressure *BackpressureController
}

type DemuxStats struct {
FramesProcessed uint64
Measurements    uint64
ValidMeasurements uint64
DroppedFrames   uint64
Errors          uint64
ProcessingLatencyNs uint64
}

type BackpressureController struct {
highWaterMark int
lowWaterMark  int
currentLoad   int64
paused        bool
mu            sync.Mutex
cond          *sync.Cond
}

func NewBackpressureController(highWaterMark, lowWaterMark int) *BackpressureController {
bc := &BackpressureController{
highWaterMark: highWaterMark,
lowWaterMark:  lowWaterMark,
}
bc.cond = sync.NewCond(&bc.mu)
return bc
}

func (bc *BackpressureController) Increment() {
bc.mu.Lock()
defer bc.mu.Unlock()

bc.currentLoad++

if bc.currentLoad >= int64(bc.highWaterMark) && !bc.paused {
bc.paused = true
}
}

func (bc *BackpressureController) Decrement() {
bc.mu.Lock()
defer bc.mu.Unlock()

bc.currentLoad--

if bc.paused && bc.currentLoad <= int64(bc.lowWaterMark) {
bc.paused = false
bc.cond.Broadcast()
}
}

func (bc *BackpressureController) WaitIfPaused() {
bc.mu.Lock()
defer bc.mu.Unlock()

for bc.paused {
bc.cond.Wait()
}
}

func (bc *BackpressureController) GetLoad() int64 {
bc.mu.Lock()
defer bc.mu.Unlock()
return bc.currentLoad
}

func (bc *BackpressureController) IsPaused() bool {
bc.mu.Lock()
defer bc.mu.Unlock()
return bc.paused
}

func NewDemux(
cfg config.Config,
fftProc *fft.Processor,
rangingCalc *ranging.Calculator,
storage *tsdb.Storage,
logger *zap.Logger,
) *Demux {
ctx, cancel := context.WithCancel(context.Background())

return &Demux{
cfg:         cfg,
logger:      logger,
fftProc:     fftProc,
rangingCalc: rangingCalc,
storage:     storage,
ctx:         ctx,
cancel:      cancel,
backpressure: NewBackpressureController(1000, 500),
}
}

func (d *Demux) Start(
inCh <-chan *model.FFTResult,
measurementCh chan<- *model.LevelMeasurement,
) error {
d.inCh = inCh
d.measurementCh = measurementCh

workers := d.cfg.FFT.Workers
if workers <= 0 {
workers = 4
}

for i := 0; i < workers; i++ {
d.wg.Add(1)
go d.worker(i)
}

d.wg.Add(1)
go d.monitorWorker()

d.logger.Info("Pipeline demux started",
zap.Int("workers", workers),
zap.Int("high_water_mark", d.backpressure.highWaterMark),
zap.Int("low_water_mark", d.backpressure.lowWaterMark),
)

return nil
}

func (d *Demux) worker(id int) {
defer d.wg.Done()

for {
select {
case <-d.ctx.Done():
return
case fftResult, ok := <-d.inCh:
if !ok {
return
}

d.backpressure.WaitIfPaused()

start := time.Now()

d.backpressure.Increment()

measurement, err := d.processFrame(fftResult)

fftResult.Unref()

d.backpressure.Decrement()

latency := time.Since(start)

d.mu.Lock()
d.stats.FramesProcessed++
d.stats.ProcessingLatencyNs += uint64(latency.Nanoseconds())
d.mu.Unlock()

if err != nil {
d.mu.Lock()
d.stats.Errors++
d.mu.Unlock()

d.logger.Error("Frame processing error",
zap.Int("worker", id),
zap.Error(err),
)
continue
}

d.mu.Lock()
d.stats.Measurements++
if measurement.Valid {
d.stats.ValidMeasurements++
}
d.mu.Unlock()

if d.storage != nil {
if err := d.storage.WriteMeasurement(measurement); err != nil {
	d.logger.Error("Failed to store measurement", zap.Error(err))
}
}

if d.measurementCh != nil {
select {
case d.measurementCh <- measurement:
case <-d.ctx.Done():
return
default:
d.logger.Debug("Measurement channel full, skipping send")
}
}
}
}
}

func (d *Demux) processFrame(fftResult *model.FFTResult) (*model.LevelMeasurement, error) {
peaks, err := d.fftProc.DetectPeaks(fftResult)
if err != nil {
return nil, err
}

measurement := d.rangingCalc.Calculate(fftResult, peaks)

return measurement, nil
}

func (d *Demux) monitorWorker() {
defer d.wg.Done()

ticker := time.NewTicker(5 * time.Second)
defer ticker.Stop()

for {
select {
case <-d.ctx.Done():
return
case <-ticker.C:
load := d.backpressure.GetLoad()
paused := d.backpressure.IsPaused()

stats := d.GetStats()

d.logger.Info("Pipeline status",
zap.Int64("current_load", load),
zap.Bool("paused", paused),
zap.Uint64("frames_processed", stats.FramesProcessed),
zap.Uint64("measurements", stats.Measurements),
zap.Uint64("valid_measurements", stats.ValidMeasurements),
zap.Uint64("dropped_frames", stats.DroppedFrames),
zap.Uint64("errors", stats.Errors),
zap.Duration("avg_latency", time.Duration(stats.ProcessingLatencyNs)),
)
}
}
}

func (d *Demux) GetStats() DemuxStats {
d.mu.Lock()
defer d.mu.Unlock()

stats := d.stats
if stats.FramesProcessed > 0 {
stats.ProcessingLatencyNs /= stats.FramesProcessed
}

return stats
}

type MeasurementSink struct {
logger      *zap.Logger
storage     *tsdb.Storage
inCh        <-chan *model.LevelMeasurement
wg          sync.WaitGroup
ctx         context.Context
cancel      context.CancelFunc
stats       SinkStats
mu          sync.Mutex
}

type SinkStats struct {
MeasurementsReceived uint64
MeasurementsStored   uint64
Errors               uint64
}

func NewMeasurementSink(
storage *tsdb.Storage,
logger *zap.Logger,
) *MeasurementSink {
ctx, cancel := context.WithCancel(context.Background())

return &MeasurementSink{
logger:  logger,
storage: storage,
ctx:     ctx,
cancel:  cancel,
}
}

func (s *MeasurementSink) Start(inCh <-chan *model.LevelMeasurement) error {
s.inCh = inCh

s.wg.Add(1)
go s.worker()

s.logger.Info("Measurement sink started")

return nil
}

func (s *MeasurementSink) worker() {
defer s.wg.Done()

for {
select {
case <-s.ctx.Done():
return
case measurement, ok := <-s.inCh:
if !ok {
return
}

s.mu.Lock()
s.stats.MeasurementsReceived++
s.mu.Unlock()

if measurement.Valid && s.storage != nil {
if err := s.storage.WriteMeasurement(measurement); err != nil {
s.logger.Error("Failed to store measurement", zap.Error(err))
}

s.mu.Lock()
s.stats.MeasurementsStored++
s.mu.Unlock()
}

s.logger.Debug("Measurement received",
zap.Time("timestamp", measurement.Timestamp),
zap.Float64("distance_m", measurement.DistanceM),
zap.Float64("level_m", measurement.LevelM),
zap.Float64("snr", measurement.SNR),
zap.Bool("valid", measurement.Valid),
zap.String("status", measurement.Status),
)
}
}
}

func (s *MeasurementSink) GetStats() SinkStats {
s.mu.Lock()
defer s.mu.Unlock()
return s.stats
}

func (s *MeasurementSink) Close() error {
s.cancel()
s.wg.Wait()

stats := s.GetStats()
s.logger.Info("Measurement sink stopped",
zap.Uint64("measurements_received", stats.MeasurementsReceived),
zap.Uint64("measurements_stored", stats.MeasurementsStored),
zap.Uint64("errors", stats.Errors),
)

return nil
}

func (d *Demux) Close() error {
d.cancel()
d.wg.Wait()

stats := d.GetStats()
d.logger.Info("Pipeline demux stopped",
zap.Uint64("frames_processed", stats.FramesProcessed),
zap.Uint64("measurements", stats.Measurements),
zap.Uint64("valid_measurements", stats.ValidMeasurements),
zap.Uint64("dropped_frames", stats.DroppedFrames),
zap.Uint64("errors", stats.Errors),
zap.Duration("avg_latency", time.Duration(stats.ProcessingLatencyNs)),
)

return nil
}