//go:build windows

package udp

import (
"net"
"syscall"
"time"

"go.uber.org/zap"
)

func (l *Listener) createSocketWindows(addr *net.UDPAddr) (*net.UDPConn, error) {
config := &net.ListenConfig{
Control: func(network, address string, c syscall.RawConn) error {
var err error
c.Control(func(fd uintptr) {
if l.cfg.RecvBufSize > 0 {
err = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, l.cfg.RecvBufSize)
if err != nil {
l.logger.Debug("Failed to set SO_RCVBUF", zap.Error(err))
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

udpConn := conn.(*net.UDPConn)
if l.cfg.ReadTimeout > 0 {
udpConn.SetReadDeadline(time.Now().Add(l.cfg.ReadTimeout))
}

return udpConn, nil
}

func (l *Listener) extractTimestampWindows(oob []byte) time.Time {
return time.Time{}
}