package ckdiscovery

import (
	"context"
	"net"
	"sync"
	"time"
)

type udpResponder struct {
	port    int
	getInfo func() Info

	mu    sync.Mutex
	conn  *net.UDPConn
	done  chan struct{}
	close sync.Once
	limit *sourceLimiter
}

func newUDPResponder(port int, getInfo func() Info) *udpResponder {
	return &udpResponder{
		port:    port,
		getInfo: getInfo,
		done:    make(chan struct{}),
		limit:   newSourceLimiter(5, time.Second),
	}
}

func (u *udpResponder) Start(ctx context.Context) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: u.port})
	if err != nil {
		return err
	}
	u.mu.Lock()
	u.conn = conn
	u.mu.Unlock()

	go func() {
		defer close(u.done)
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					return
				}
			}
			if addr == nil || !IsPrivateSource(addr.IP) || !u.limit.allow(addr.IP.String()) {
				continue
			}
			probe, err := DecodeProbe(buf[:n])
			if err != nil {
				continue
			}
			info := u.getInfo()
			offer := BuildOffer(info, probe.Nonce)
			payload, err := EncodeOffer(offer)
			if err != nil {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
			_, _ = conn.WriteToUDP(payload, addr)
		}
	}()
	return nil
}

func (u *udpResponder) Update(Info) {}

func (u *udpResponder) Close() error {
	u.close.Do(func() {
		u.mu.Lock()
		conn := u.conn
		u.conn = nil
		u.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		select {
		case <-u.done:
		case <-time.After(500 * time.Millisecond):
		}
	})
	return nil
}

// sourceLimiter is a tiny per-IP token bucket. It prevents a LAN host from using
// the discovery responder as a reflection/amplification source.
type sourceLimiter struct {
	mu      sync.Mutex
	rate    float64
	per     time.Duration
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newSourceLimiter(rate float64, per time.Duration) *sourceLimiter {
	return &sourceLimiter{rate: rate, per: per, buckets: map[string]*tokenBucket{}}
}

func (l *sourceLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) > 1024 {
			for k, v := range l.buckets {
				if now.Sub(v.last) > 2*time.Minute {
					delete(l.buckets, k)
				}
			}
		}
		l.buckets[key] = &tokenBucket{tokens: l.rate - 1, last: now}
		return true
	}
	elapsed := now.Sub(b.last)
	b.tokens += elapsed.Seconds() / l.per.Seconds() * l.rate
	if b.tokens > l.rate {
		b.tokens = l.rate
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
