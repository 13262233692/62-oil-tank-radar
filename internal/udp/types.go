package udp

import (
	"context"
	"sync"

	"github.com/oil-tank-radar/gateway/internal/config"
	"github.com/oil-tank-radar/gateway/pkg/pool"
	"go.uber.org/zap"
)

type Listener struct {
cfg    config.UDPConfig
pool   *pool.BufferPool
logger *zap.Logger
conns  []interface{}
wg     sync.WaitGroup
ctx    context.Context
cancel context.CancelFunc
stats  ListenerStats
mu     sync.Mutex
}

type ListenerStats struct {
PacketsReceived uint64
PacketsDropped  uint64
BytesReceived   uint64
Errors          uint64
}

func NewListener(cfg config.UDPConfig, bufferPool *pool.BufferPool, logger *zap.Logger) *Listener {
ctx, cancel := context.WithCancel(context.Background())

return &Listener{
cfg:    cfg,
pool:   bufferPool,
logger: logger,
ctx:    ctx,
cancel: cancel,
}
}

func (l *Listener) GetStats() ListenerStats {
l.mu.Lock()
defer l.mu.Unlock()
return l.stats
}