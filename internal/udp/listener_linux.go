//go:build linux

package udp

import (
	"encoding/binary"
	"net"
	"syscall"
	"time"

	"github.com/oil-tank-radar/gateway/internal/config"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

func (l *Listener) createSocket(addr *net.UDPAddr) (*net.UDPConn, error) {
	config := &net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var err error
			c.Control(func(fd uintptr) {
				if l.cfg.RecvBufSize > 0 {
					err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, l.cfg.RecvBufSize)
					if err != nil {
						return
					}
				}

				if l.cfg.EnableGRO {
					err = unix.SetsockoptInt(int(fd), unix.SOL_UDP, unix.UDP_GRO, 1)
					if err != nil {
						l.logger.Debug("UDP_GRO not available", zap.Error(err))
						err = nil
					}
				}

				err = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_PKTINFO, 1)
				if err != nil {
					return
				}
			})
			return err
		},
	}

	conn, err := config.ListenPacket(l.ctx, "udp", addr.String())
	if err != nil {
		return nil, err
	}

	udpConn := conn.(*net.UDPConn)
	if l.cfg.ReadTimeout > 0 {
		udpConn.SetReadDeadline(time.Now().Add(l.cfg.ReadTimeout))
	}

	return udpConn, nil
}

func (l *Listener) createSocketWithReusePort(addr *net.UDPAddr) (*net.UDPConn, error) {
	config := &net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var err error
			c.Control(func(fd uintptr) {
				err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
				if err != nil {
					return
				}

				if l.cfg.RecvBufSize > 0 {
					err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, l.cfg.RecvBufSize)
					if err != nil {
						return
					}
				}

				if l.cfg.EnableGRO {
					err = unix.SetsockoptInt(int(fd), unix.SOL_UDP, unix.UDP_GRO, 1)
					if err != nil {
						l.logger.Debug("UDP_GRO not available", zap.Error(err))
						err = nil
					}
				}
			})
			return err
		},
	}

	conn, err := config.ListenPacket(l.ctx, "udp", addr.String())
	if err != nil {
		return nil, err
	}

	return conn.(*net.UDPConn), nil
}

func (l *Listener) extractTimestamp(oob []byte) time.Time {
	if len(oob) < 16 {
		return time.Time{}
	}

	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return time.Time{}
	}

	for _, msg := range msgs {
		if msg.Header.Level == unix.SOL_SOCKET && msg.Header.Type == unix.SO_TIMESTAMPING {
			if len(msg.Data) >= 16 {
				sec := int64(binary.LittleEndian.Uint64(msg.Data[0:8]))
				nsec := int64(binary.LittleEndian.Uint64(msg.Data[8:16]))
				return time.Unix(sec, nsec)
			}
		}
	}

	return time.Time{}
}
