package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesDatabaseFile(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(context.Background(), dir, "wol.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if err := db.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "wol.db"))
	if err != nil {
		t.Fatalf("数据库文件未生成: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("数据库文件为空")
	}
}

func TestOpenFailsWhenDataDirIsAFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备测试文件失败: %v", err)
	}

	db, err := Open(context.Background(), blocker, "wol.db")
	if err == nil {
		_ = db.Close()
		t.Fatal("Open() 在数据目录不可创建时应当返回错误")
	}
}
