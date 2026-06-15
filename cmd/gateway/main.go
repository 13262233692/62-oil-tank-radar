package main

import (
"context"
"flag"
"fmt"
"net/http"
_ "net/http/pprof"
"os"
"os/signal"
"syscall"

"github.com/prometheus/client_golang/prometheus/promhttp"
"go.uber.org/zap"
"go.uber.org/zap/zapcore"
"gopkg.in/yaml.v3"

"github.com/oil-tank-radar/gateway/internal/config"
"github.com/oil-tank-radar/gateway/internal/fft"
"github.com/oil-tank-radar/gateway/internal/framer"
"github.com/oil-tank-radar/gateway/internal/pipeline"
"github.com/oil-tank-radar/gateway/internal/ranging"
"github.com/oil-tank-radar/gateway/internal/tsdb"
"github.com/oil-tank-radar/gateway/internal/udp"
"github.com/oil-tank-radar/gateway/pkg/model"
"github.com/oil-tank-radar/gateway/pkg/pool"
)

var (
configPath = flag.String("config", "configs/gateway.yaml", "Path to configuration file")
logLevel   = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
)

func main() {
flag.Parse()

logger := initLogger(*logLevel)
defer logger.Sync()

logger.Info("Starting oil tank radar gateway...")

cfg, err := loadConfig(*configPath)
if err != nil {
logger.Fatal("Failed to load configuration", zap.Error(err))
}

printConfig(logger, cfg)

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

bufferPool := initBufferPool(cfg)

udpListener := udp.NewListener(cfg.UDP, bufferPool, logger)
framer := framer.NewFramer(cfg.Framer, bufferPool, logger)
fftProcessor := fft.NewProcessor(cfg.FFT, cfg.Framer, bufferPool, logger)
rangingCalc := ranging.NewCalculator(cfg.Ranging, cfg.FFT, cfg.Framer, logger)
storage := tsdb.NewStorage(cfg.TimeScaleDB, logger)

udpCh := make(chan *model.UDPFrame, 1000)
framerCh := make(chan *model.FMCWRawFrame, 1000)
fftCh := make(chan *model.FFTResult, 1000)
measurementCh := make(chan *model.LevelMeasurement, 100)

demux := pipeline.NewDemux(*cfg, fftProcessor, rangingCalc, storage, logger)
measurementSink := pipeline.NewMeasurementSink(storage, logger)

if cfg.Runtime.MetricsAddr != "" {
go startMetricsServer(cfg.Runtime.MetricsAddr, logger)
}

if cfg.Runtime.EnableProfiling && cfg.Runtime.PprofAddr != "" {
go startPprofServer(cfg.Runtime.PprofAddr, logger)
}

if err := storage.Start(); err != nil {
logger.Error("Failed to start storage, continuing without persistence", zap.Error(err))
storage = nil
}

if err := udpListener.Start(udpCh); err != nil {
logger.Fatal("Failed to start UDP listener", zap.Error(err))
}

if err := framer.Start(udpCh, framerCh); err != nil {
logger.Fatal("Failed to start framer", zap.Error(err))
}

if err := fftProcessor.Start(framerCh, fftCh); err != nil {
logger.Fatal("Failed to start FFT processor", zap.Error(err))
}

if err := demux.Start(fftCh, measurementCh); err != nil {
logger.Fatal("Failed to start pipeline demux", zap.Error(err))
}

if err := measurementSink.Start(measurementCh); err != nil {
logger.Fatal("Failed to start measurement sink", zap.Error(err))
}

logger.Info("All components started successfully")

sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

select {
case sig := <-sigCh:
logger.Info("Received signal, initiating shutdown", zap.String("signal", sig.String()))
case <-ctx.Done():
logger.Info("Context cancelled, initiating shutdown")
}

shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Runtime.ShutdownTimeout)
defer shutdownCancel()

go func() {
<-shutdownCtx.Done()
if shutdownCtx.Err() == context.DeadlineExceeded {
logger.Fatal("Shutdown timed out, forcing exit")
}
}()

udpListener.Close()
close(udpCh)

framer.Close()
close(framerCh)

fftProcessor.Close()
close(fftCh)

demux.Close()
close(measurementCh)

measurementSink.Close()

if storage != nil {
storage.Close()
}

rangingCalc.Close()

logger.Info("Gateway shutdown complete")
}

func initLogger(level string) *zap.Logger {
var zapLevel zapcore.Level
switch level {
case "debug":
zapLevel = zapcore.DebugLevel
case "info":
zapLevel = zapcore.InfoLevel
case "warn":
zapLevel = zapcore.WarnLevel
case "error":
zapLevel = zapcore.ErrorLevel
default:
zapLevel = zapcore.InfoLevel
}

cfg := zap.Config{
Level:            zap.NewAtomicLevelAt(zapLevel),
Development:      false,
Sampling:         &zap.SamplingConfig{Initial: 100, Thereafter: 100},
Encoding:         "json",
EncoderConfig:    zap.NewProductionEncoderConfig(),
OutputPaths:      []string{"stdout"},
ErrorOutputPaths: []string{"stderr"},
}

logger, err := cfg.Build()
if err != nil {
panic(fmt.Sprintf("Failed to create logger: %v", err))
}

return logger
}

func loadConfig(path string) (*config.Config, error) {
data, err := os.ReadFile(path)
if err != nil {
return nil, fmt.Errorf("failed to read config file: %w", err)
}

var cfg config.Config
if err := yaml.Unmarshal(data, &cfg); err != nil {
return nil, fmt.Errorf("failed to parse config file: %w", err)
}

setDefaults(&cfg)

return &cfg, nil
}

func setDefaults(cfg *config.Config) {
if cfg.UDP.MaxPacketSize == 0 {
cfg.UDP.MaxPacketSize = 9000
}
if cfg.UDP.RecvBufSize == 0 {
cfg.UDP.RecvBufSize = 32 * 1024 * 1024
}
if cfg.UDP.Workers == 0 {
cfg.UDP.Workers = 4
}

if cfg.Framer.ADCBits == 0 {
cfg.Framer.ADCBits = 16
}
if cfg.Framer.FrameHeaderSize == 0 {
cfg.Framer.FrameHeaderSize = 64
}

if cfg.FFT.RangeFFTSize == 0 {
cfg.FFT.RangeFFTSize = 256
}
if cfg.FFT.DopplerFFTSize == 0 {
cfg.FFT.DopplerFFTSize = 64
}
if cfg.FFT.WindowType == "" {
cfg.FFT.WindowType = "hann"
}
if cfg.FFT.CFARGuardCells == 0 {
cfg.FFT.CFARGuardCells = 2
}
if cfg.FFT.CFARTrainCells == 0 {
cfg.FFT.CFARTrainCells = 8
}
if cfg.FFT.CFARThreshold == 0 {
cfg.FFT.CFARThreshold = 5.0
}
if cfg.FFT.Workers == 0 {
cfg.FFT.Workers = 4
}

if cfg.Ranging.StartFreqGHz == 0 {
cfg.Ranging.StartFreqGHz = 24.0
}
if cfg.Ranging.BandwidthGHz == 0 {
cfg.Ranging.BandwidthGHz = 250.0
}
if cfg.Ranging.SampleRateMHz == 0 {
cfg.Ranging.SampleRateMHz = 12.5
}
if cfg.Ranging.TankHeightM == 0 {
cfg.Ranging.TankHeightM = 20.0
}
if cfg.Ranging.MinDistanceM == 0 {
cfg.Ranging.MinDistanceM = 0.5
}
if cfg.Ranging.MaxDistanceM == 0 {
cfg.Ranging.MaxDistanceM = 25.0
}
if cfg.Ranging.SNRThreshold == 0 {
cfg.Ranging.SNRThreshold = 10.0
}

if cfg.TimeScaleDB.Port == 0 {
cfg.TimeScaleDB.Port = 5432
}
if cfg.TimeScaleDB.User == "" {
cfg.TimeScaleDB.User = "postgres"
}
if cfg.TimeScaleDB.Database == "" {
cfg.TimeScaleDB.Database = "radar"
}
if cfg.TimeScaleDB.SSLMode == "" {
cfg.TimeScaleDB.SSLMode = "disable"
}
if cfg.TimeScaleDB.MaxConns == 0 {
cfg.TimeScaleDB.MaxConns = 10
}
if cfg.TimeScaleDB.MinConns == 0 {
cfg.TimeScaleDB.MinConns = 2
}
if cfg.TimeScaleDB.BatchSize == 0 {
cfg.TimeScaleDB.BatchSize = 1000
}
if cfg.TimeScaleDB.FlushInterval == 0 {
cfg.TimeScaleDB.FlushInterval = 1000000000
}
if cfg.TimeScaleDB.HyperTable == "" {
cfg.TimeScaleDB.HyperTable = "level_measurements"
}

if cfg.Runtime.ShutdownTimeout == 0 {
cfg.Runtime.ShutdownTimeout = 30000000000
}
if cfg.Runtime.BufferPoolSize == 0 {
cfg.Runtime.BufferPoolSize = 1000
}
}

func initBufferPool(cfg *config.Config) *pool.BufferPool {
byteSize := cfg.UDP.MaxPacketSize
complexSize := cfg.FFT.RangeFFTSize
rows := cfg.Framer.ChirpsPerFrame
cols := cfg.Framer.SamplesPerChirp

if rows == 0 {
rows = 128
}
if cols == 0 {
cols = 256
}

return pool.NewBufferPool(byteSize, complexSize, rows, cols)
}

func printConfig(logger *zap.Logger, cfg *config.Config) {
logger.Info("Configuration loaded",
zap.String("udp_listen_addr", cfg.UDP.ListenAddr),
zap.Int("udp_port", cfg.UDP.Port),
zap.Int("udp_workers", cfg.UDP.Workers),
zap.Int("adc_bits", cfg.Framer.ADCBits),
zap.Int("samples_per_chirp", cfg.Framer.SamplesPerChirp),
zap.Int("chirps_per_frame", cfg.Framer.ChirpsPerFrame),
zap.Int("rx_channels", cfg.Framer.RxChannels),
zap.Int("range_fft_size", cfg.FFT.RangeFFTSize),
zap.Int("doppler_fft_size", cfg.FFT.DopplerFFTSize),
zap.String("window_type", cfg.FFT.WindowType),
zap.Float64("start_freq_ghz", cfg.Ranging.StartFreqGHz),
zap.Float64("bandwidth_ghz", cfg.Ranging.BandwidthGHz),
zap.Float64("tank_height_m", cfg.Ranging.TankHeightM),
zap.String("timescaledb_host", cfg.TimeScaleDB.Host),
zap.String("timescaledb_database", cfg.TimeScaleDB.Database),
)
}

func startMetricsServer(addr string, logger *zap.Logger) {
http.Handle("/metrics", promhttp.Handler())

logger.Info("Starting metrics server", zap.String("addr", addr))
if err := http.ListenAndServe(addr, nil); err != nil {
logger.Error("Metrics server failed", zap.Error(err))
}
}

func startPprofServer(addr string, logger *zap.Logger) {
logger.Info("Starting pprof server", zap.String("addr", addr))
if err := http.ListenAndServe(addr, nil); err != nil {
logger.Error("Pprof server failed", zap.Error(err))
}
}