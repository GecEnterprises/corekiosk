package ckdiscovery

import (
	"encoding/json"
	"testing"
)

func TestProbeOfferRoundTrip(t *testing.T) {
	probe := []byte(`{"ck":1,"t":"probe","n":"abc123","p":1}`)
	decoded, err := DecodeProbe(probe)
	if err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	if decoded.Nonce != "abc123" {
		t.Fatalf("nonce = %q", decoded.Nonce)
	}

	offer := BuildOffer(Info{
		ServerID:        "srv-1",
		AppName:         "joyland",
		Name:            "Joyland Arcade",
		HTTPPort:        8080,
		ProtocolVersion: 1,
		Version:         "0.3.0",
		Methods:         []string{MethodUDP, MethodMDNS},
	}, decoded.Nonce)
	data, err := EncodeOffer(offer)
	if err != nil {
		t.Fatalf("encode offer: %v", err)
	}
	got, err := DecodeOffer(data)
	if err != nil {
		t.Fatalf("decode offer: %v", err)
	}
	if got.ServerID != "srv-1" || got.Nonce != "abc123" || got.Port != 8080 {
		t.Fatalf("unexpected offer: %+v", got)
	}
	if len(got.Methods) != 2 || got.Methods[0] != MethodUDP {
		t.Fatalf("methods = %v", got.Methods)
	}
}

func TestDecodeRejectsForeignPackets(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(`{"ck":2,"t":"probe","n":"x"}`),
		[]byte(`{"ck":1,"t":"offer","id":"","port":8080}`),
		[]byte(`{"ck":1,"t":"offer","id":"srv","port":0}`),
		[]byte(`not json`),
	}
	for _, raw := range cases {
		if _, err := DecodeProbe(raw); err == nil {
			// An offer can still be a valid probe-shaped packet only if it says probe.
			if _, err2 := DecodeOffer(raw); err2 == nil {
				t.Fatalf("accepted invalid packet %s", raw)
			}
		}
	}
}

func TestOfferSizeCap(t *testing.T) {
	offer := BuildOffer(Info{
		ServerID: "srv",
		Name:     string(make([]byte, maxPacketSize)),
		HTTPPort: 8080,
		Methods:  []string{MethodUDP},
	}, "n")
	if _, err := json.Marshal(offer); err == nil {
		if _, err := EncodeOffer(offer); err == nil {
			t.Fatal("expected oversized offer to be rejected")
		}
	}
}

func TestSanitizeName(t *testing.T) {
	got := SanitizeName("  Joyland\nArcade..  ", "fallback")
	if got != "Joyland Arcade" {
		t.Fatalf("got %q", got)
	}
	if SanitizeName("", "") != "CoreKiosk" {
		t.Fatal("empty name should fall back")
	}
}

func TestSourceLimiter(t *testing.T) {
	lim := newSourceLimiter(2, 1e9) // 2 tokens/second
	if !lim.allow("a") || !lim.allow("a") {
		t.Fatal("first two probes should pass")
	}
	if lim.allow("a") {
		t.Fatal("third immediate probe should be limited")
	}
	if !lim.allow("b") {
		t.Fatal("separate source should pass")
	}
}
