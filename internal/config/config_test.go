package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv(EnvPrefix+"PORT", "")
	t.Setenv(EnvPrefix+"DATA_DIR", "")
	t.Setenv(EnvPrefix+"LOG_LEVEL", "")

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
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv(EnvPrefix+"PORT", "9090")
	t.Setenv(EnvPrefix+"DATA_DIR", "C:/wol-data")
	t.Setenv(EnvPrefix+"LOG_LEVEL", "debug")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.Port != 9090 || c.DataDir != "C:/wol-data" || c.LogLevel != "debug" {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestLoadRejectsBadPort(t *testing.T) {
	t.Setenv(EnvPrefix+"PORT", "70000")
	if _, err := Load(); err == nil {
		t.Fatal("Load() 应当拒绝越界端口")
	}
}

func TestLoadRejectsBadLogLevel(t *testing.T) {
	t.Setenv(EnvPrefix+"LOG_LEVEL", "verbose")
	if _, err := Load(); err == nil {
		t.Fatal("Load() 应当拒绝非法日志级别")
	}
}
