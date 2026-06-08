package common

import "sync"

// InMemoryConcurrencyLimiter 单节点内存并发计数器（统计 in-flight 请求数）。
// 多节点需改 Redis（INCR/DECR + 过期兜底防泄漏），当前单 new-api 节点用内存即可。
type InMemoryConcurrencyLimiter struct {
	mu      sync.Mutex
	current map[string]int
}

func (l *InMemoryConcurrencyLimiter) Init() {
	if l.current == nil {
		l.mu.Lock()
		if l.current == nil {
			l.current = make(map[string]int)
		}
		l.mu.Unlock()
	}
}

// TryAcquire 未达上限则占用一个槽并返回 true；max<=0 表示不限制。
func (l *InMemoryConcurrencyLimiter) TryAcquire(key string, max int) bool {
	if max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current == nil {
		l.current = make(map[string]int)
	}
	if l.current[key] >= max {
		return false
	}
	l.current[key]++
	return true
}

// Release 释放一个槽（归零即删，避免 map 无限增长）。
func (l *InMemoryConcurrencyLimiter) Release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current == nil {
		return
	}
	if l.current[key] > 0 {
		l.current[key]--
		if l.current[key] == 0 {
			delete(l.current, key)
		}
	}
}
