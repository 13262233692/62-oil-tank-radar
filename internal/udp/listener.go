package udp

import (
	"encoding/binary"
	"net"
	"sync"
	"time"

	"github.com/oil-tank-radar/gateway/pkg/model"
	"go.uber.org/zap"
)

func (l *Listener) Start(outCh chan<- *model.UDPFrame) error {
	workers := l.cfg.Workers
	if workers <= 0 {
		workers = 1
	}

	addr, err := net.ResolveUDPAddr("udp", l.cfg.ListenAddr)
	if err != nil {
		return err
	}

	l.conns = make([]interface{}, 0, workers)

	for i := 0; i < workers; i++ {
		var conn *net.UDPConn
		var createErr error

		if isWindows() {
			conn, createErr = l.createSocketWindows(addr)
		} else {
			conn, createErr = l.createSocket(addr)
		}

		if createErr != nil {
			l.Close()
			return createErr
		}

		l.conns = append(l.conns, conn)

		l.wg.Add(1)
		go l.worker(i, conn, outCh)
	}

	l.logger.Info("UDP listener started",
		zap.String("address", l.cfg.ListenAddr),
		zap.Int("workers", workers),
	)

	return nil
}

func (l *Listener) createSocket(addr *net.UDPAddr) (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	if l.cfg.RecvBufSize > 0 {
		conn.SetReadBuffer(l.cfg.RecvBufSize)
	}
	return conn, nil
}

func (l *Listener) worker(id int, conn *net.UDPConn, outCh chan<- *model.UDPFrame) {
	defer l.wg.Done()

	buf := make([]byte, l.cfg.MaxPacketSize)
	oob := make([]byte, 64)

	for {
		select {
		case <-l.ctx.Done():
			return
		default:
			n, oobn, flags, srcAddr, err := conn.ReadMsgUDP(buf, oob)
			if err != nil {
				l.mu.Lock()
				l.stats.Errors++
				l.mu.Unlock()

				select {
				case <-l.ctx.Done():
					return
				default:
					time.Sleep(10 * time.Millisecond)
					continue
				}
			}

			_ = oobn
			_ = flags

			if n <= 0 {
				continue
			}

			frame := l.pool.GetUDPFrame()
			if len(frame.Data) < n {
				frame.Data = make([]byte, n)
			}
			copy(frame.Data[:n], buf[:n])
			frame.Length = n
			frame.SourceIP = srcAddr.String()

			var ts time.Time
			if isWindows() {
				ts = l.extractTimestampWindows(oob)
			} else {
				ts = l.extractTimestamp(oob)
			}
			if !ts.IsZero() {
				frame.Timestamp = ts
			} else {
				frame.Timestamp = time.Now()
			}

			var frameNumber uint64
			if n >= 8 {
				frameNumber = binary.BigEndian.Uint64(frame.Data[:8])
			}

			l.mu.Lock()
			l.stats.PacketsReceived++
			l.stats.BytesReceived += uint64(n)
			l.mu.Unlock()

			select {
			case outCh <- frame:
			case <-l.ctx.Done():
				l.pool.PutUDPFrame(frame)
				return
			default:
				l.mu.Lock()
				l.stats.PacketsDropped++
				l.mu.Unlock()
				l.pool.PutUDPFrame(frame)
				l.logger.Warn("UDP output channel full, dropping packet",
					zap.Int("worker", id),
					zap.Uint64("frame_number", frameNumber),
				)
			}
		}
	}
}

func (l *Listener) extractTimestamp(oob []byte) time.Time {
	return time.Time{}
}

func (l *Listener) Close() error {
	l.cancel()

	for _, conn := range l.conns {
		if c, ok := conn.(*net.UDPConn); ok {
			c.Close()
		}
	}

	l.wg.Wait()

	stats := l.GetStats()
	l.logger.Info("UDP listener stopped",
		zap.Uint64("packets_received", stats.PacketsReceived),
		zap.Uint64("packets_dropped", stats.PacketsDropped),
		zap.Uint64("bytes_received", stats.BytesReceived),
		zap.Uint64("errors", stats.Errors),
	)

	return nil
}

func isWindows() bool {
	return runtimeIsWindows()
}

var checkOSOnce sync.Once
var isOSWindows bool

func runtimeIsWindows() bool {
	checkOSOnce.Do(func() {
		isOSWindows = false
	})
	return isOSWindows
}
