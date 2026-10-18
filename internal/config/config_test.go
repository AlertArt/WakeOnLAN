package config

import (
	"testing"
	"time"
)

// setBase 设置一组最小合法环境，避免各用例互相继承环境变量。
func setBase(t *testing.T) {
	t.Helper()
	t.Setenv(EnvPrefix+"PORT", "")
	t.Setenv(EnvPrefix+"DATA_DIR", "")
	t.Setenv(EnvPrefix+"LOG_LEVEL", "")
	t.Setenv(EnvPrefix+"TOKEN_TTL", "")
	t.Setenv(EnvPrefix+"ADMIN_PASSWORD", "test-password-1234")
}

func TestLoadDefaults(t *testing.T) {
	setBase(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.Port != 8080 {
		t.Errorf("Port = %d, want 8080", c.Port)
	}
	if c.DataDir != "/data" {
		t.Errorf("DataDir = %q, want /data", c.DataDir)
	}
	if c.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", c.LogLevel)
	}
	if c.TokenTTL != DefaultTokenTTL {
		t.Errorf("TokenTTL = %s, want %s", c.TokenTTL, DefaultTokenTTL)
	}
}

func TestLoadOverrides(t *testing.T) {
	setBase(t)
	t.Setenv(EnvPrefix+"PORT", "9090")
	t.Setenv(EnvPrefix+"DATA_DIR", "C:/wol-data")
	t.Setenv(EnvPrefix+"LOG_LEVEL", "debug")
	t.Setenv(EnvPrefix+"TOKEN_TTL", "30m")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.Port != 9090 || c.DataDir != "C:/wol-data" || c.LogLevel != "debug" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if c.TokenTTL != 30*time.Minute {
		t.Fatalf("TokenTTL = %s, want 30m", c.TokenTTL)
	}
}

func TestLoadRejectsEmptyPassword(t *testing.T) {
	setBase(t)
	t.Setenv(EnvPrefix+"ADMIN_PASSWORD", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() 应当拒绝空口令：不允许服务以无口令状态启动")
	}
}

func TestLoadRejectsBadTokenTTL(t *testing.T) {
	for _, v := range []string{"7d", "0", "-1h", "abc"} {
		setBase(t)
		t.Setenv(EnvPrefix+"TOKEN_TTL", v)
		if _, err := Load(); err == nil {
			t.Fatalf("Load() 应当拒绝 TOKEN_TTL=%q", v)
		}
	}
}

func TestLoadRejectsBadPort(t *testing.T) {
	setBase(t)
	t.Setenv(EnvPrefix+"PORT", "70000")
	if _, err := Load(); err == nil {
		t.Fatal("Load() 应当拒绝越界端口")
	}
}

func TestLoadRejectsBadLogLevel(t *testing.T) {
	setBase(t)
	t.Setenv(EnvPrefix+"LOG_LEVEL", "verbose")
	if _, err := Load(); err == nil {
		t.Fatal("Load() 应当拒绝非法日志级别")
	}
}
