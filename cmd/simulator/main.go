package main

import (
"encoding/binary"
"flag"
"fmt"
"math"
"math/rand"
"net"
"os"
"os/signal"
"syscall"
"time"

"go.uber.org/zap"
"go.uber.org/zap/zapcore"
)

var (
targetHost  = flag.String("host", "127.0.0.1", "Target host")
targetPort  = flag.Int("port", 9000, "Target port")
sampleRate  = flag.Int("sample-rate", 25, "Frame rate per second")
duration    = flag.Int("duration", 0, "Duration in seconds (0 = infinite)")
logLevel    = flag.String("log-level", "info", "Log level")
adcBits     = flag.Int("adc-bits", 16, "ADC bit depth")
samples     = flag.Int("samples", 256, "Samples per chirp")
chirps      = flag.Int("chirps", 128, "Chirps per frame")
channels    = flag.Int("channels", 1, "RX channels")
targetLevel = flag.Float64("target-level", 10.0, "Target liquid level in meters")
noiseLevel  = flag.Float64("noise", 0.01, "Noise level")
waveHeight  = flag.Float64("wave-height", 0.0, "Wave height in meters")
)

const (
magicNumber = 0x52414452
)

type RadarFrame struct {
Magic         uint32
Version       uint16
Timestamp     uint64
FrameNumber   uint32
ADCBits       uint8
SamplesPerChirp uint16
ChirpsPerFrame uint16
RxChannels    uint8
Reserved      [20]byte
Payload       []int16
}

type Simulator struct {
conn     net.Conn
logger   *zap.Logger
rand     *rand.Rand
frameNum uint32
}

func main() {
flag.Parse()

logger := initLogger(*logLevel)
defer logger.Sync()

logger.Info("Starting radar data simulator...",
zap.String("target_host", *targetHost),
zap.Int("target_port", *targetPort),
zap.Int("sample_rate", *sampleRate),
zap.Int("adc_bits", *adcBits),
zap.Int("samples", *samples),
zap.Int("chirps", *chirps),
zap.Int("channels", *channels),
zap.Float64("target_level", *targetLevel),
zap.Float64("noise", *noiseLevel),
zap.Float64("wave_height", *waveHeight),
)

conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", *targetHost, *targetPort))
if err != nil {
logger.Fatal("Failed to connect to target", zap.Error(err))
}
defer conn.Close()

sim := &Simulator{
conn:   conn,
logger: logger,
rand:   rand.New(rand.NewSource(time.Now().UnixNano())),
}

sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

interval := time.Duration(1000000000 / *sampleRate)
ticker := time.NewTicker(interval)
defer ticker.Stop()

var endTime time.Time
if *duration > 0 {
endTime = time.Now().Add(time.Duration(*duration) * time.Second)
}

frameCount := 0
startTime := time.Now()

for {
select {
case <-sigCh:
logger.Info("Received signal, stopping simulator")
printStats(logger, frameCount, startTime)
return
case <-ticker.C:
if *duration > 0 && time.Now().After(endTime) {
logger.Info("Duration reached, stopping simulator")
printStats(logger, frameCount, startTime)
return
}

if err := sim.sendFrame(); err != nil {
logger.Error("Failed to send frame", zap.Error(err))
}

frameCount++
if frameCount%(*sampleRate*10) == 0 {
logger.Info("Simulator running",
zap.Int("frames_sent", frameCount),
zap.Duration("elapsed", time.Since(startTime)),
zap.Float64("fps", float64(frameCount)/time.Since(startTime).Seconds()),
)
}
}
}
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
Development:      true,
Sampling:         nil,
Encoding:         "console",
EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
OutputPaths:      []string{"stdout"},
ErrorOutputPaths: []string{"stderr"},
}

logger, err := cfg.Build()
if err != nil {
panic(fmt.Sprintf("Failed to create logger: %v", err))
}

return logger
}

func (s *Simulator) sendFrame() error {
frame := s.generateFrame()
data := frameToBytes(frame)

_, err := s.conn.Write(data)
if err != nil {
return fmt.Errorf("failed to write frame: %w", err)
}

return nil
}

func (s *Simulator) generateFrame() *RadarFrame {
adcMax := (1 << *adcBits) - 1
adcMid := 1 << (*adcBits - 1)

totalSamples := *samples * *chirps * *channels

frame := &RadarFrame{
Magic:           magicNumber,
Version:         1,
Timestamp:       uint64(time.Now().UnixNano()),
FrameNumber:     s.frameNum,
ADCBits:         uint8(*adcBits),
SamplesPerChirp: uint16(*samples),
ChirpsPerFrame:  uint16(*chirps),
RxChannels:      uint8(*channels),
Payload:         make([]int16, totalSamples),
}

s.frameNum++

waveOffset := 0.0
if *waveHeight > 0 {
waveOffset = *waveHeight * math.Sin(float64(time.Now().UnixNano())/1e9*0.5)
}

targetDistance := 20.0 - *targetLevel + waveOffset
targetBin := int(targetDistance / (299792458.0 / (2 * 250e9)))
if targetBin < 0 {
targetBin = 0
}
if targetBin >= *samples {
targetBin = *samples - 1
}

for chirp := 0; chirp < *chirps; chirp++ {
for ch := 0; ch < *channels; ch++ {
baseIdx := (chirp**channels + ch) * *samples

for i := 0; i < *samples; i++ {
idx := baseIdx + i

signal := 0.0

if i == targetBin || i == targetBin-1 || i == targetBin+1 {
dist := float64(i - targetBin)
amplitude := float64(adcMax) * 0.6 * math.Exp(-dist*dist*0.5)
signal += amplitude
}

noise := s.rand.NormFloat64() * float64(adcMax) * *noiseLevel
signal += noise

sample := int16(adcMid + int(signal))

if sample < 0 {
sample = 0
}
if sample > int16(adcMax) {
sample = int16(adcMax)
}

frame.Payload[idx] = sample
}
}
}

return frame
}

func frameToBytes(frame *RadarFrame) []byte {
headerSize := 4 + 2 + 8 + 4 + 1 + 2 + 2 + 1 + 20
payloadSize := len(frame.Payload) * 2
buf := make([]byte, headerSize+payloadSize)

offset := 0

binary.LittleEndian.PutUint32(buf[offset:], frame.Magic)
offset += 4

binary.LittleEndian.PutUint16(buf[offset:], frame.Version)
offset += 2

binary.LittleEndian.PutUint64(buf[offset:], frame.Timestamp)
offset += 8

binary.LittleEndian.PutUint32(buf[offset:], frame.FrameNumber)
offset += 4

buf[offset] = frame.ADCBits
offset++

binary.LittleEndian.PutUint16(buf[offset:], frame.SamplesPerChirp)
offset += 2

binary.LittleEndian.PutUint16(buf[offset:], frame.ChirpsPerFrame)
offset += 2

buf[offset] = frame.RxChannels
offset++

for i := range frame.Payload {
binary.LittleEndian.PutUint16(buf[offset:], uint16(frame.Payload[i]))
offset += 2
}

return buf
}

func printStats(logger *zap.Logger, frameCount int, startTime time.Time) {
elapsed := time.Since(startTime)
fps := float64(frameCount) / elapsed.Seconds()

logger.Info("Simulator stopped",
zap.Int("total_frames", frameCount),
zap.Duration("elapsed", elapsed),
zap.Float64("avg_fps", fps),
)
}