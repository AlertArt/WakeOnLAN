// 本文件实现在内存里发一次性票据。
//
// 为什么票据不落库：票据的寿命只有几十秒，重启丢失的后果是"点一次重来"，
// 而落库会引入一个需要清理、需要担心时钟漂移的临时表。
// 一次性的含义也在这里实现：Consume 取走即删除，第二个请求拿到 not found。
package guac

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Ticket 是一张已签发的票据。
type Ticket struct {
	// Data 是 guacamole-auth-json 需要的 base64 载荷（已含过期时间）。
	Data string
	// ConnName 是载荷里唯一的连接名，兑换时用于拼直连路由。
	ConnName  string
	ExpiresAt time.Time
}

// TicketStore 是票据的内存表，并发安全。
// 零值不可用，必须经 NewTicketStore 构造。
type TicketStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]Ticket
	now   func() time.Time // 便于测试固定时间
}

// NewTicketStore 创建一个票据表。ttl<=0 时用 DefaultTTL。
func NewTicketStore(ttl time.Duration) *TicketStore {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &TicketStore{
		ttl:   ttl,
		items: make(map[string]Ticket),
		now:   time.Now,
	}
}

// Issue 签发一张票据，返回票据 ID 与过期时间。connName 随票据一起保存，
// 兑换时拼直连路由用——不放进 data（那是给 Guacamole 的签名载荷，不能多字段）。
func (s *TicketStore) Issue(data, connName string) (string, time.Time, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	id := hex.EncodeToString(buf)
	expires := s.now().Add(s.ttl)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.items[id] = Ticket{Data: data, ConnName: connName, ExpiresAt: expires}
	return id, expires, nil
}

// Consume 取走票据：存在且未过期才返回 ok，无论结果如何都会删除——
// 过期票据没有留着的价值，重复消费也不能第二次成功。
func (s *TicketStore) Consume(id string) (Ticket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sweepLocked()
	t, ok := s.items[id]
	if !ok {
		return Ticket{}, false
	}
	delete(s.items, id)
	if !s.now().Before(t.ExpiresAt) {
		return Ticket{}, false
	}
	return t, true
}

// Len 返回当前未清理的票据数，仅供测试断言。
func (s *TicketStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// sweepLocked 清掉已过期的票据。调用方必须持锁。
// 票据数量只有"最近几十秒点过的次数"，线性清理的代价可以忽略。
func (s *TicketStore) sweepLocked() {
	now := s.now()
	for id, t := range s.items {
		if !now.Before(t.ExpiresAt) {
			delete(s.items, id)
		}
	}
}
