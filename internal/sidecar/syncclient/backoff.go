package syncclient

import (
	"math/rand"
	"time"
)

// backoff 是有界指数退避 + 全抖动状态机。须经 newBackoff 构造。
type backoff struct {
	initial time.Duration
	max     time.Duration
	attempt int
	rng     func(n int64) int64 // 返回 [0,n)，注入便于测试
}

func newBackoff(initial, max time.Duration) *backoff {
	if initial <= 0 {
		initial = defaultBackoffInitial
	}
	if max <= 0 {
		max = defaultBackoffMax
	}
	if initial > max {
		initial = max
	}
	return &backoff{initial: initial, max: max, rng: rand.Int63n}
}

// capFor 返回第 attempt 次重试的退避上界 min(max, initial*2^attempt)，防溢出。
func (b *backoff) capFor(attempt int) time.Duration {
	capped := b.initial
	for i := 0; i < attempt; i++ {
		if capped >= b.max/2 {
			return b.max
		}
		capped *= 2
	}
	if capped > b.max {
		return b.max
	}
	return capped
}

// next 返回下一次退避时长（全抖动：[0, cap)）并推进 attempt。
func (b *backoff) next() time.Duration {
	c := b.capFor(b.attempt)
	b.attempt++
	if c <= 0 {
		return 0
	}
	return time.Duration(b.rng(int64(c)))
}

// reset 清零退避（连接健康后调用）。
func (b *backoff) reset() { b.attempt = 0 }
