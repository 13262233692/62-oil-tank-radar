package framer

import (
"context"
"encoding/binary"
"errors"
"runtime"
"sync"
"time"

"github.com/oil-tank-radar/gateway/internal/config"
"github.com/oil-tank-radar/gateway/pkg/model"
"github.com/oil-tank-radar/gateway/pkg/pool"
"go.uber.org/zap"
)

type Framer struct {
cfg       config.FramerConfig
pool      *pool.BufferPool
logger    *zap.Logger
inCh      <-chan *model.UDPFrame
outCh     chan<- *model.FMCWRawFrame
wg        sync.WaitGroup
ctx       context.Context
cancel    context.CancelFunc
frameBuf  []byte
frameIdx  int
frameNum  uint64
stats     FramerStats
mu        sync.Mutex
bitMask   uint16
bitShift  uint8
}

type FramerStats struct {
FramesParsed  uint64
BytesProcessed uint64
Errors        uint64
InvalidFrames uint64
}

var (
ErrInvalidFrameSize = errors.New("invalid frame size")
ErrInvalidHeader    = errors.New("invalid frame header")
ErrFrameIncomplete  = errors.New("frame incomplete")
)

func NewFramer(cfg config.FramerConfig, bufferPool *pool.BufferPool, logger *zap.Logger) *Framer {
ctx, cancel := context.WithCancel(context.Background())

var bitMask uint16
var bitShift uint8
switch cfg.ADCBits {
case 12:
bitMask = 0x0FFF
bitShift = 0
case 14:
bitMask = 0x3FFF
bitShift = 0
case 16:
bitMask = 0xFFFF
bitShift = 0
default:
bitMask = 0xFFFF
bitShift = 0
}

return &Framer{
cfg:      cfg,
pool:     bufferPool,
logger:   logger,
ctx:      ctx,
cancel:   cancel,
frameBuf: make([]byte, cfg.FrameSize),
bitMask:  bitMask,
bitShift: bitShift,
}
}

func (f *Framer) Start(inCh <-chan *model.UDPFrame, outCh chan<- *model.FMCWRawFrame) error {
f.inCh = inCh
f.outCh = outCh

workers := runtime.NumCPU()
if workers > 4 {
workers = 4
}

for i := 0; i < workers; i++ {
f.wg.Add(1)
go f.worker(i)
}

f.logger.Info("Framer started",
zap.Int("adc_bits", f.cfg.ADCBits),
zap.Int("samples_per_chirp", f.cfg.SamplesPerChirp),
zap.Int("chirps_per_frame", f.cfg.ChirpsPerFrame),
zap.Int("rx_channels", f.cfg.RxChannels),
zap.Int("frame_size", f.cfg.FrameSize),
zap.Int("workers", workers),
)

return nil
}

func (f *Framer) worker(id int) {
defer f.wg.Done()

for {
select {
case <-f.ctx.Done():
return
case udpFrame, ok := <-f.inCh:
if !ok {
return
}

rawFrame, err := f.parseFrame(udpFrame)
udpFrame.Unref()

if err != nil {
f.mu.Lock()
f.stats.Errors++
if errors.Is(err, ErrInvalidFrameSize) || errors.Is(err, ErrInvalidHeader) {
f.stats.InvalidFrames++
}
f.mu.Unlock()

f.logger.Debug("Frame parse error",
zap.Int("worker", id),
zap.Error(err),
)
continue
}

f.mu.Lock()
f.stats.FramesParsed++
f.stats.BytesProcessed += uint64(udpFrame.Length)
f.mu.Unlock()

select {
case f.outCh <- rawFrame:
case <-f.ctx.Done():
f.pool.PutFMCWRawFrame(rawFrame)
return
default:
f.pool.PutFMCWRawFrame(rawFrame)
f.logger.Warn("Framer output channel full, dropping frame")
}
}
}
}

func (f *Framer) parseFrame(udpFrame *model.UDPFrame) (*model.FMCWRawFrame, error) {
if udpFrame.Length < f.cfg.FrameHeaderSize {
return nil, ErrInvalidFrameSize
}

data := udpFrame.Data[:udpFrame.Length]

if !f.validateHeader(data[:f.cfg.FrameHeaderSize]) {
return nil, ErrInvalidHeader
}

expectedSize := f.cfg.FrameHeaderSize +
f.cfg.RxChannels*f.cfg.ChirpsPerFrame*f.cfg.SamplesPerChirp*f.cfg.ADCBits/8

if udpFrame.Length < expectedSize {
return nil, ErrFrameIncomplete
}

rawFrame := f.pool.GetFMCWRawFrame()
rawFrame.Timestamp = udpFrame.Timestamp

f.mu.Lock()
f.frameNum++
rawFrame.FrameNumber = f.frameNum
f.mu.Unlock()

copy(rawFrame.Header, data[:f.cfg.FrameHeaderSize])

payloadOffset := f.cfg.FrameHeaderSize
samplesPerChannel := f.cfg.SamplesPerChirp * f.cfg.ChirpsPerFrame

for ch := 0; ch < f.cfg.RxChannels; ch++ {
chOffset := payloadOffset + ch*samplesPerChannel*f.cfg.ADCBits/8

switch f.cfg.ADCBits {
case 12:
f.extract12BitSamples(data[chOffset:], rawFrame.IFData[ch], samplesPerChannel)
case 14:
f.extract14BitSamples(data[chOffset:], rawFrame.IFData[ch], samplesPerChannel)
case 16:
f.extract16BitSamples(data[chOffset:], rawFrame.IFData[ch], samplesPerChannel)
default:
f.extract16BitSamples(data[chOffset:], rawFrame.IFData[ch], samplesPerChannel)
}
}

return rawFrame, nil
}

func (f *Framer) validateHeader(header []byte) bool {
if len(header) < 8 {
return false
}

magic := binary.LittleEndian.Uint32(header[0:4])
return magic == 0x52414452
}

func (f *Framer) extract12BitSamples(data []byte, samples []int16, count int) {
bitIdx := 0
for i := 0; i < count && bitIdx+12 <= len(data)*8; i++ {
byteIdx := bitIdx / 8
bitOffset := bitIdx % 8

var sample uint16
if bitOffset <= 4 {
sample = uint16(data[byteIdx])>>bitOffset |
uint16(data[byteIdx+1])<<(8-bitOffset)
} else {
sample = uint16(data[byteIdx])>>bitOffset |
uint16(data[byteIdx+1])<<(8-bitOffset) |
uint16(data[byteIdx+2])<<(16-bitOffset)
}

sample &= 0x0FFF
if sample&0x0800 != 0 {
sample |= 0xF000
}

samples[i] = int16(sample)
bitIdx += 12
}
}

func (f *Framer) extract14BitSamples(data []byte, samples []int16, count int) {
bitIdx := 0
for i := 0; i < count && bitIdx+14 <= len(data)*8; i++ {
byteIdx := bitIdx / 8
bitOffset := bitIdx % 8

var sample uint16
switch {
case bitOffset <= 2:
sample = uint16(data[byteIdx])>>bitOffset |
uint16(data[byteIdx+1])<<(8-bitOffset)
case bitOffset <= 10:
sample = uint16(data[byteIdx])>>bitOffset |
uint16(data[byteIdx+1])<<(8-bitOffset) |
uint16(data[byteIdx+2])<<(16-bitOffset)
default:
sample = uint16(data[byteIdx])>>bitOffset |
uint16(data[byteIdx+1])<<(8-bitOffset) |
uint16(data[byteIdx+2])<<(16-bitOffset)
}

sample &= 0x3FFF
if sample&0x2000 != 0 {
sample |= 0xC000
}

samples[i] = int16(sample)
bitIdx += 14
}
}

func (f *Framer) extract16BitSamples(data []byte, samples []int16, count int) {
for i := 0; i < count && (i+1)*2 <= len(data); i++ {
sample := binary.LittleEndian.Uint16(data[i*2 : (i+1)*2])
samples[i] = int16(sample)
}
}

func (f *Framer) ParseFrameHeader(data []byte) (uint64, time.Duration, error) {
if len(data) < f.cfg.FrameHeaderSize {
return 0, 0, ErrInvalidHeader
}

if !f.validateHeader(data) {
return 0, 0, ErrInvalidHeader
}

frameNum := binary.LittleEndian.Uint64(data[8:16])
timestamp := binary.LittleEndian.Uint64(data[16:24])

return frameNum, time.Duration(timestamp), nil
}

func (f *Framer) GetStats() FramerStats {
f.mu.Lock()
defer f.mu.Unlock()
return f.stats
}

func (f *Framer) Close() error {
f.cancel()
f.wg.Wait()

f.logger.Info("Framer stopped",
zap.Uint64("frames_parsed", f.stats.FramesParsed),
zap.Uint64("bytes_processed", f.stats.BytesProcessed),
zap.Uint64("errors", f.stats.Errors),
zap.Uint64("invalid_frames", f.stats.InvalidFrames),
)

return nil
}