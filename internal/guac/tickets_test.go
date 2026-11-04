package guac

import (
	"testing"
	"time"
)

// 票据只能用一次：第二次取走必须失败。
func TestTicketSingleUse(t *testing.T) {
	store := NewTicketStore(time.Minute)

	id, expires, err := store.Issue("payload", "conn")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if id == "" || len(id) != 32 {
		t.Fatalf("票据 ID 应为 32 位十六进制，实际 %q", id)
	}
	if time.Until(expires) <= 0 {
		t.Fatalf("过期时间应在将来：%v", expires)
	}

	got, ok := store.Consume(id)
	if !ok || got.Data != "payload" {
		t.Fatalf("首次取走失败：ok=%v data=%q", ok, got.Data)
	}
	if _, ok := store.Consume(id); ok {
		t.Fatal("同一张票据被第二次取走")
	}
	if store.Len() != 0 {
		t.Fatalf("取走后应清空，剩 %d 条", store.Len())
	}
}

// 过期后取走失败，且不留下垃圾。
func TestTicketExpires(t *testing.T) {
	store := NewTicketStore(50 * time.Millisecond)
	now := time.Now()
	store.now = func() time.Time { return now }

	id, _, err := store.Issue("payload", "conn")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	store.now = func() time.Time { return now.Add(60 * time.Millisecond) }
	if _, ok := store.Consume(id); ok {
		t.Fatal("过期票据仍然可用")
	}
}

// 不存在的票据按失败处理，不能 panic 也不能返回零值成功。
func TestTicketUnknownID(t *testing.T) {
	store := NewTicketStore(time.Minute)
	if _, ok := store.Consume("deadbeef"); ok {
		t.Fatal("不存在的票据被接受")
	}
	if _, ok := store.Consume(""); ok {
		t.Fatal("空票据被接受")
	}
}

// 过期票据在下次签发时被顺带清理，表不会无限增长。
func TestTicketSweepOnIssue(t *testing.T) {
	store := NewTicketStore(20 * time.Millisecond)
	now := time.Now()
	store.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if _, _, err := store.Issue("payload", "conn"); err != nil {
			t.Fatalf("Issue: %v", err)
		}
	}
	if store.Len() != 3 {
		t.Fatalf("应已签发 3 张，实际 %d", store.Len())
	}

	store.now = func() time.Time { return now.Add(30 * time.Millisecond) }
	if _, _, err := store.Issue("payload", "conn"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("过期票据未被清理，剩 %d 条", store.Len())
	}
}
