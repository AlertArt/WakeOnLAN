// Package config 集中声明运行期配置。E0 阶段只落地已经冻结的 4 个环境变量，
// 其余配置项（令牌有效期、探测节奏等）由对应环节（E4/E3）按需追加，
// 禁止在骨架阶段预先占位。
package config

import (
	"fmt"
	"os"
	"strconv"
)

// EnvPrefix 统一前缀，避免与其他容器的环境变量混淆。
const EnvPrefix = "WOL_"

// Config 是进程级配置，全部来自环境变量（F8：单容器 + 环境变量配置）。
type Config struct {
	// Port 是 HTTP 监听端口，默认 8080。
	Port int
	// DataDir 是持久化目录（SQLite 文件所在位置），默认 /data。
	DataDir string
	// LogLevel 取 debug / info / warn / error，默认 info。
	LogLevel string
	// AdminPassword 是登录口令，由环境变量注入，不得写入代码或镜像。
	AdminPassword string
}

// Load 读取环境变量并做最小校验。
func Load() (Config, error) {
	c := Config{
		Port:          8080,
		DataDir:       "/data",
		LogLevel:      "info",
		AdminPassword: "",
	}

	if v := os.Getenv(EnvPrefix + "PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return Config{}, fmt.Errorf("%sPORT 非法: %q", EnvPrefix, v)
		}
		c.Port = p
	}
	if v := os.Getenv(EnvPrefix + "DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv(EnvPrefix + "LOG_LEVEL"); v != "" {
		switch v {
		case "debug", "info", "warn", "error":
			c.LogLevel = v
		default:
			return Config{}, fmt.Errorf("%sLOG_LEVEL 非法: %q", EnvPrefix, v)
		}
	}
	c.AdminPassword = os.Getenv(EnvPrefix + "ADMIN_PASSWORD")

	return c, nil
}
