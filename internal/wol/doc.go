// Package wol 负责 WOL 魔术包的构造与发送（F2）。
// E0 阶段只放置包结构说明与常量，实现由 E2 完成；
// 探测逻辑（F3）在 internal/probe 中，同属后续环节。
package wol

// PacketLen 是 WOL 魔术包的固定长度：6 字节 0xFF 同步头 + 目标 MAC 重复 16 次。
const PacketLen = 102

// MagicHeader 是魔术包的同步头。
const MagicHeader = 0xFF

// DefaultBroadcastPort 是 WOL 的常用端口。
const DefaultBroadcastPort = 9
