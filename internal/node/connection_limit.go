package node

import (
	"net"
	"sync"
)

const defaultMaxRPCConnections = 8192

type connectionLimiter struct {
	mu          sync.Mutex
	counts      map[string]int
	total       int
	globalLimit int
}

func newConnectionLimiter(globalLimit int) *connectionLimiter {
	if globalLimit == 0 {
		globalLimit = defaultMaxRPCConnections
	}
	return &connectionLimiter{
		counts:      make(map[string]int),
		globalLimit: globalLimit,
	}
}

func (l *connectionLimiter) tryAcquire(portName string, portLimit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.globalLimit > 0 && l.total >= l.globalLimit {
		return false
	}
	if portLimit > 0 && l.counts[portName] >= portLimit {
		return false
	}
	l.counts[portName]++
	l.total++
	return true
}

func (l *connectionLimiter) release(portName string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[portName] == 0 {
		return
	}
	l.counts[portName]--
	l.total--
}

type limitedListener struct {
	net.Listener
	limiter   *connectionLimiter
	portName  string
	portLimit int
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.limiter.tryAcquire(l.portName, l.portLimit) {
			release := &releaseOnClose{release: func() {
				l.limiter.release(l.portName)
			}}
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				return &limitedTCPConn{TCPConn: tcpConn, releaseOnClose: release}, nil
			}
			return &limitedConn{Conn: conn, releaseOnClose: release}, nil
		}
		_ = conn.Close()
	}
}

type limitedConn struct {
	net.Conn
	*releaseOnClose
}

func (c *limitedConn) Close() error {
	return c.close(c.Conn)
}

type limitedTCPConn struct {
	*net.TCPConn
	*releaseOnClose
}

func (c *limitedTCPConn) Close() error {
	return c.close(c.TCPConn)
}

type releaseOnClose struct {
	once    sync.Once
	release func()
}

func (c *releaseOnClose) close(conn net.Conn) error {
	err := conn.Close()
	c.once.Do(c.release)
	return err
}
