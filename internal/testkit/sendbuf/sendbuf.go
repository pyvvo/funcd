// Package sendbuf fixes the kernel send buffer of a test server's connections. Linux grows a loopback connection's
// send buffer to several MiB and wakes a writer blocked on it only once about a third has drained, so without a fixed
// size how much of a response the kernel takes, and how soon a reading client lets a blocked write go on, depend on
// the host.
package sendbuf

import "net"

// Size is the send buffer Listener sets: far below any test response, above one 32 KiB write.
const Size = 64 << 10

type listener struct{ net.Listener }

// Listener returns l with the send buffer of each TCP connection it accepts set to Size.
func Listener(l net.Listener) net.Listener { return listener{l} }

func (l listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if err := c.(*net.TCPConn).SetWriteBuffer(Size); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}
