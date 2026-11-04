package cli

import (
	"net"
	"testing"
	"time"
)

func TestLimitListener(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newLimitListener(inner, 2)
	defer ln.Close()

	accepted := make(chan net.Conn, 3)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	var clients []net.Conn
	for range 3 {
		c, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		clients = append(clients, c)
	}
	first := <-accepted
	<-accepted
	select {
	case <-accepted:
		t.Fatal("third connection accepted while two are open")
	case <-time.After(200 * time.Millisecond):
	}
	first.Close()
	first.Close() // a second Close must not free another slot
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("third connection not accepted after one closed")
	}
}
