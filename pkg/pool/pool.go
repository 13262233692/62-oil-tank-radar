package pool

import (
"sync"

"github.com/oil-tank-radar/gateway/pkg/model"
)

type BufferPool struct {
bytePool      sync.Pool
complexPool   sync.Pool
matrixPool    sync.Pool
framePool     sync.Pool
rawFramePool  sync.Pool
ifMatrixPool  sync.Pool
fftResultPool sync.Pool

byteSize    int
complexSize int
rows        int
cols        int
}

func NewBufferPool(byteSize, complexSize, rows, cols int) *BufferPool {
p := &BufferPool{
byteSize:    byteSize,
complexSize: complexSize,
rows:        rows,
cols:        cols,
}

p.bytePool.New = func() interface{} {
b := make([]byte, byteSize)
return &b
}

p.complexPool.New = func() interface{} {
c := make([]complex128, complexSize)
return &c
}

p.matrixPool.New = func() interface{} {
m := make([][]complex128, rows)
for i := range m {
m[i] = make([]complex128, cols)
}
return &m
}

p.framePool.New = func() interface{} {
return &model.UDPFrame{
Data: make([]byte, byteSize),
}
}

p.rawFramePool.New = func() interface{} {
data := make([][]int16, rows)
for i := range data {
data[i] = make([]int16, cols)
}
return &model.FMCWRawFrame{
Header: make([]byte, 64),
IFData: data,
}
}

p.ifMatrixPool.New = func() interface{} {
return &model.IFMatrix{
Data: make([]complex128, rows*cols),
Rows: rows,
Cols: cols,
}
}

p.fftResultPool.New = func() interface{} {
rd := make([][]complex128, rows)
for i := range rd {
rd[i] = make([]complex128, cols)
}
return &model.FFTResult{
RangeDoppler: rd,
RangeProfile: make([]float64, rows*cols),
RDMatrix:     make([]float64, rows*cols),
RangeBins:    cols,
DopplerBins:  rows,
}
}

return p
}

func (p *BufferPool) GetBytes() []byte {
b := *p.bytePool.Get().(*[]byte)
if cap(b) < p.byteSize {
b = make([]byte, p.byteSize)
}
return b[:p.byteSize]
}

func (p *BufferPool) PutBytes(b []byte) {
if cap(b) >= p.byteSize {
p.bytePool.Put(&b)
}
}

func (p *BufferPool) GetComplex() []complex128 {
c := *p.complexPool.Get().(*[]complex128)
if cap(c) < p.complexSize {
c = make([]complex128, p.complexSize)
}
return c[:p.complexSize]
}

func (p *BufferPool) PutComplex(c []complex128) {
if cap(c) >= p.complexSize {
for i := range c {
c[i] = 0
}
p.complexPool.Put(&c)
}
}

func (p *BufferPool) GetMatrix() [][]complex128 {
m := *p.matrixPool.Get().(*[][]complex128)
if len(m) != p.rows || len(m[0]) != p.cols {
m = make([][]complex128, p.rows)
for i := range m {
m[i] = make([]complex128, p.cols)
}
}
return m
}

func (p *BufferPool) PutMatrix(m [][]complex128) {
if len(m) == p.rows && len(m[0]) == p.cols {
for i := range m {
for j := range m[i] {
m[i][j] = 0
}
}
p.matrixPool.Put(&m)
}
}

func (p *BufferPool) GetUDPFrame() *model.UDPFrame {
f := p.framePool.Get().(*model.UDPFrame)
if cap(f.Data) < p.byteSize {
f.Data = make([]byte, p.byteSize)
}
f.Data = f.Data[:p.byteSize]
f.Length = 0
return f
}

func (p *BufferPool) PutUDPFrame(f *model.UDPFrame) {
if cap(f.Data) >= p.byteSize {
f.Data = f.Data[:cap(f.Data)]
for i := range f.Data {
f.Data[i] = 0
}
p.framePool.Put(f)
}
}

func (p *BufferPool) GetFMCWRawFrame() *model.FMCWRawFrame {
f := p.rawFramePool.Get().(*model.FMCWRawFrame)
if len(f.IFData) != p.rows || len(f.IFData[0]) != p.cols {
f.IFData = make([][]int16, p.rows)
for i := range f.IFData {
f.IFData[i] = make([]int16, p.cols)
}
}
return f
}

func (p *BufferPool) PutFMCWRawFrame(f *model.FMCWRawFrame) {
if len(f.IFData) == p.rows && len(f.IFData[0]) == p.cols {
for i := range f.IFData {
for j := range f.IFData[i] {
f.IFData[i][j] = 0
}
}
p.rawFramePool.Put(f)
}
}

func (p *BufferPool) GetIFMatrix() *model.IFMatrix {
m := p.ifMatrixPool.Get().(*model.IFMatrix)
if len(m.Data) != p.rows*p.cols {
m.Data = make([]complex128, p.rows*p.cols)
m.Rows = p.rows
m.Cols = p.cols
}
return m
}

func (p *BufferPool) PutIFMatrix(m *model.IFMatrix) {
if len(m.Data) == p.rows*p.cols {
for i := range m.Data {
m.Data[i] = 0
}
p.ifMatrixPool.Put(m)
}
}

func (p *BufferPool) GetFFTResult() *model.FFTResult {
r := p.fftResultPool.Get().(*model.FFTResult)
requiredSize := p.rows * p.cols
if len(r.RangeDoppler) != p.rows || len(r.RangeDoppler[0]) != p.cols {
r.RangeDoppler = make([][]complex128, p.rows)
for i := range r.RangeDoppler {
r.RangeDoppler[i] = make([]complex128, p.cols)
}
r.RangeBins = p.cols
r.DopplerBins = p.rows
}
if len(r.RangeProfile) < requiredSize {
r.RangeProfile = make([]float64, requiredSize)
}
if len(r.RDMatrix) < requiredSize {
r.RDMatrix = make([]float64, requiredSize)
}
return r
}

func (p *BufferPool) PutFFTResult(r *model.FFTResult) {
if len(r.RangeDoppler) == p.rows && len(r.RangeDoppler[0]) == p.cols {
for i := range r.RangeDoppler {
for j := range r.RangeDoppler[i] {
r.RangeDoppler[i][j] = 0
}
}
for i := range r.RangeProfile {
r.RangeProfile[i] = 0
}
for i := range r.RDMatrix {
r.RDMatrix[i] = 0
}
p.fftResultPool.Put(r)
}
}