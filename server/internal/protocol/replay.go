package protocol

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrEnvelopeReplay       = errors.New("envelope replay detected")
	ErrEnvelopeSeqRegressed = errors.New("envelope sequence regressed")
)

type ReplayGuard struct {
	mu      sync.Mutex
	ttl     time.Duration
	seen    map[string]time.Time
	lastSeq uint64
}

func NewReplayGuard(ttl time.Duration) *ReplayGuard {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &ReplayGuard{ttl: ttl, seen: map[string]time.Time{}}
}

func (g *ReplayGuard) Accept(env Envelope, now time.Time) error {
	if g == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cleanup(now)
	if env.Seq > 0 {
		if env.Seq < g.lastSeq {
			return ErrEnvelopeSeqRegressed
		}
		if env.Seq > g.lastSeq {
			g.lastSeq = env.Seq
		}
	}
	for _, key := range []string{env.MessageID, env.Nonce} {
		if key == "" {
			continue
		}
		if expiresAt, ok := g.seen[key]; ok && expiresAt.After(now) {
			return ErrEnvelopeReplay
		}
		g.seen[key] = now.Add(g.ttl)
	}
	return nil
}

func (g *ReplayGuard) cleanup(now time.Time) {
	for key, expiresAt := range g.seen {
		if !expiresAt.After(now) {
			delete(g.seen, key)
		}
	}
}
