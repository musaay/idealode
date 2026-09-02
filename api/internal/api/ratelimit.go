package api

import (
	"sync"
	"time"
)

// chatRateLimit/blendRateLimit: sözleşme kotaları (oturum başına, api
// süreci içinde bellekte tutulur — süreç yeniden başlarsa sıfırlanır,
// yatay ölçekte paylaşılmaz; MVP için yeterli, bkz. spec).
const (
	chatRateLimit  = 30 // mesaj / saat
	blendRateLimit = 5  // blend / gün
)

// rateLimiter, oturum (sid) başına sabit pencereli (fixed-window) basit bir
// kota sayaçıdır. Süreç belleğinde yaşar; kalıcı değildir.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	counts map[string]*rlEntry
}

type rlEntry struct {
	count   int
	resetAt time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, counts: map[string]*rlEntry{}}
}

// Allow, sid için kotadan bir birim düşer ve izin verilip verilmediğini
// döner. Pencere dolmuşsa sayaç sıfırlanır. Kota aşıldıysa sayaç
// ARTIRILMAZ (aşım tekrar tekrar denense de sayaç şişmez).
func (rl *rateLimiter) Allow(sid string) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	e, ok := rl.counts[sid]
	if !ok || now.After(e.resetAt) {
		e = &rlEntry{resetAt: now.Add(rl.window)}
		rl.counts[sid] = e
	}
	if e.count >= rl.limit {
		return false
	}
	e.count++
	return true
}
