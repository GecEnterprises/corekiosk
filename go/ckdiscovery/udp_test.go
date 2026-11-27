package ckdiscovery

import (
	"context"
	"net"
	"testing"
	"time"
)

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	return port
}

func TestUDPResponder(t *testing.T) {
	port := freeUDPPort(t)
	info := Info{
		ServerID:        "server-123",
		AppName:         "joyland",
		Name:            "Joyland Arcade",
		HTTPPort:        8080,
		ProtocolVersion: 1,
		Version:         "0.3.0",
		Methods:         []string{MethodUDP},
	}
	responder := newUDPResponder(port, func() Info { return info })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := responder.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer responder.Close()

	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		t.Fatalf("client socket: %v", err)
	}
	defer conn.Close()
	probe := []byte(`{"ck":1,"t":"probe","n":"nonce-1","p":1}`)
	if _, err := conn.WriteToUDP(probe, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read offer: %v", err)
	}
	offer, err := DecodeOffer(buf[:n])
	if err != nil {
		t.Fatalf("decode offer: %v", err)
	}
	if offer.ServerID != info.ServerID || offer.Nonce != "nonce-1" || offer.Port != 8080 {
		t.Fatalf("unexpected offer: %+v", offer)
	}
}

func TestUDPResponderRejectsNonProbe(t *testing.T) {
	port := freeUDPPort(t)
	responder := newUDPResponder(port, func() Info { return Info{ServerID: "s", HTTPPort: 8080} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := responder.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer responder.Close()

	conn, _ := net.ListenUDP("udp4", nil)
	defer conn.Close()
	_, _ = conn.WriteToUDP([]byte(`{"ck":1,"t":"offer","id":"s","port":8080}`), &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 256)
	if _, _, err := conn.ReadFromUDP(buf); err == nil {
		t.Fatal("server replied to a non-probe packet")
	}
}
