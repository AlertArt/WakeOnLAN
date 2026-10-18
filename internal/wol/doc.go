// Package wol 负责 WOL 魔术包的构造与发送（F2）。
//
// 设计取向：本包是纯粹的「字节 → 网络」转换层。
// 不依赖数据库、不依赖 HTTP、不持有配置，可脱离本服务单独调用（E2 验收 2-5）。
// 一次调用只发一个包，不重试、不并发（E2 验收 2-6）。
// 探测与状态判定属 F3，在 internal/probe 中。
package wol

// PacketLen 是 WOL 魔术包的固定长度：6 字节 0xFF 同步头 + 目标 MAC 重复 16 次。
const PacketLen = 102

// MagicHeader 是魔术包的同步头。
const MagicHeader = 0xFF

// DefaultBroadcastPort 是 WOL 的常用端口。
const DefaultBroadcastPort = 9
