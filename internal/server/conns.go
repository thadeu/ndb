package server

import (
	"net"
	"sync"
)

// LimitListener accepts at most n connections at the same time (n <= 0 =
// no limit). Over it, Accept waits until a connection closes, and the new
// clients wait in the kernel backlog. It is golang.org/x/net/netutil
// LimitListener, without the dependency.
func LimitListener(l net.Listener, n int) net.Listener {
	if n <= 0 {
		return l
	}

	return &limitListener{Listener: l, sem: make(chan struct{}, n), done: make(chan struct{})}
}

type limitListener struct {
	net.Listener

	sem  chan struct{}
	done chan struct{}
	once sync.Once
}

func (l *limitListener) Accept() (net.Conn, error) {
	select {
	case l.sem <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}

	c, err := l.Listener.Accept()

	if err != nil {
		<-l.sem

		return nil, err
	}

	return &limitConn{Conn: c, release: func() { <-l.sem }}, nil
}

func (l *limitListener) Close() error {
	l.once.Do(func() { close(l.done) })

	return l.Listener.Close()
}

// limitConn gives its slot back once, on the first Close.
type limitConn struct {
	net.Conn

	release func()
	once    sync.Once
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()

	c.once.Do(c.release)

	return err
}
