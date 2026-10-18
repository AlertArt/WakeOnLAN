// Package config 集中声明运行期配置。E0 只落地骨架必需的 4 个环境变量，
// 其余配置项由对应环节按需追加：令牌有效期属 E4，禁止在骨架阶段预先占位。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// EnvPrefix 统一前缀，避免与其他容器的环境变量混淆。
const EnvPrefix = "WOL_"

// DefaultTokenTTL 是令牌默认有效期（7 天），见 docs/E4-contract.md。
const DefaultTokenTTL = 168 * time.Hour

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
	// TokenTTL 是登录令牌的有效期，默认 7 天。
	TokenTTL time.Duration
}

// Load 读取环境变量并做最小校验。
func Load() (Config, error) {
	c := Config{
		Port:          8080,
		DataDir:       "/data",
		LogLevel:      "info",
		AdminPassword: "",
		TokenTTL:      DefaultTokenTTL,
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
	if v := os.Getenv(EnvPrefix + "TOKEN_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("%sTOKEN_TTL 非法: %q（需为大于 0 的时长，如 168h）", EnvPrefix, v)
		}
		c.TokenTTL = d
	}
	c.AdminPassword = os.Getenv(EnvPrefix + "ADMIN_PASSWORD")

	// 口令没有默认值：给一个默认口令等于把"没配置"变成"配了个人人都知道的"，比不设防更危险。
	if c.AdminPassword == "" {
		return Config{}, fmt.Errorf("%sADMIN_PASSWORD 未设置：请通过环境变量注入登录口令，服务不会以空口令启动", EnvPrefix)
	}

	return c, nil
}
