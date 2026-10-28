// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"net/netip"
	"sync"
	"time"
)

// rateLimiter is a token bucket per client: each IPv4 address or IPv6 /64
// may send burst beacons at once and perMinute on average after that. It
// stops floods from one client from filling the database or the dashboard
// with fake traffic.
type rateLimiter struct {
	perMinute int // 0 = unlimited
	burst     float64

	mu      sync.Mutex
	buckets map[netip.Prefix]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// maxBuckets bounds memory. When it is reached, idle buckets (which would be
// full anyway) are dropped; if that frees nothing, new clients are refused
// until it does.
const maxBuckets = 100_000

func newRateLimiter(perMinute int) *rateLimiter {
	burst := float64(perMinute)
	if burst < 20 {
		burst = 20
	}
	return &rateLimiter{perMinute: perMinute, burst: burst, buckets: map[netip.Prefix]*bucket{}}
}

func (l *rateLimiter) allow(ip netip.Addr, now time.Time) bool {
	if l == nil || l.perMinute <= 0 {
		return true
	}
	bits := 32
	if ip.Is6() {
		bits = 64
	}
	k, _ := ip.Prefix(bits)
	rate := float64(l.perMinute) / 60 // tokens per second
	refill := time.Duration(l.burst / rate * float64(time.Second))

	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[k]
	if b == nil {
		if len(l.buckets) >= maxBuckets {
			for key, old := range l.buckets {
				if now.Sub(old.last) >= refill {
					delete(l.buckets, key)
				}
			}
			if len(l.buckets) >= maxBuckets {
				return false
			}
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[k] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
