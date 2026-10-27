// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"net/netip"
	"sync"
	"time"
)

// Failed logins allowed per client within failWindow before it is locked out
// for the rest of the window. IPv6 clients are grouped by /64, since a single
// machine usually controls a whole /64. maxGlobalFails caps failures from all
// clients together, so guessing can't be spread over many addresses.
const (
	maxFails       = 10
	maxGlobalFails = 100
	failWindow     = 15 * time.Minute
)

type limiter struct {
	mu     sync.Mutex
	fails  map[netip.Prefix]*failures
	global failures
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

// count adds one attempt to f, starting a new window if the old one is over,
// and reports whether f was still under max before it.
func (f *failures) count(max int, now time.Time) bool {
	if now.Sub(f.since) >= failWindow {
		*f = failures{since: now}
	}
	if f.n >= max {
		return false
	}
	f.n++
	return true
}

// attempt reserves a login attempt for ip and reports whether it may go
// ahead. It counts as a failure until success is called, so concurrent
// requests can't all pass the check before any failure is recorded.
func (l *limiter) attempt(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.fails == nil {
		l.fails = map[netip.Prefix]*failures{}
	}
	if len(l.fails) > 10000 {
		for k, f := range l.fails {
			if now.Sub(f.since) >= failWindow {
				delete(l.fails, k)
			}
		}
	}
	k := clientKey(ip)
	f := l.fails[k]
	if f == nil {
		f = &failures{since: now}
		l.fails[k] = f
	}
	if g := l.global; now.Sub(g.since) < failWindow && g.n >= maxGlobalFails {
		return false
	}
	if !f.count(maxFails, now) {
		return false
	}
	l.global.count(maxGlobalFails, now)
	return true
}

// success clears ip's failures and takes back the attempt it reserved.
func (l *limiter) success(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, clientKey(ip))
	if l.global.n > 0 {
		l.global.n--
	}
}
