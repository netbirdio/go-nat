package pcp

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestSendOnceStopsOnCancel(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	received := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1)
		_, _, err := server.ReadFromUDP(buffer)
		received <- err
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (&Client{timeout: 10 * time.Second}).sendOnce(ctx, server.LocalAddr().(*net.UDPAddr), []byte{0})
		done <- err
	}()

	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("send stopped before cancellation: %v", err)
	case <-time.After(time.Second):
		t.Fatal("server did not receive PCP packet")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("send error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("send did not stop after cancellation")
	}
}
