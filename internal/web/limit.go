package web

import (
	"net/netip"
	"sync"
	"time"
)

// Failed logins allowed per client within failWindow before it is locked out
// for the rest of the window. IPv6 clients are grouped by /64, since a single
// machine usually controls a whole /64.
const (
	maxFails   = 10
	failWindow = 15 * time.Minute
)

type limiter struct {
	mu    sync.Mutex
	fails map[netip.Prefix]*failures
}

type failures struct {
	n     int
	since time.Time
}

func clientKey(ip netip.Addr) netip.Prefix {
	bits := 32
	if ip.Is6() {
		bits = 64
	}
	p, _ := ip.Prefix(bits)
	return p
}

func (l *limiter) blocked(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.fails[clientKey(ip)]
	return f != nil && f.n >= maxFails && time.Since(f.since) < failWindow
}

func (l *limiter) fail(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[netip.Prefix]*failures{}
	}
	if len(l.fails) > 10000 {
		for k, f := range l.fails {
			if time.Since(f.since) >= failWindow {
				delete(l.fails, k)
			}
		}
	}
	k := clientKey(ip)
	f := l.fails[k]
	if f == nil || time.Since(f.since) >= failWindow {
		f = &failures{since: time.Now()}
		l.fails[k] = f
	}
	f.n++
}

func (l *limiter) reset(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, clientKey(ip))
}
