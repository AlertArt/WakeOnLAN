// Package auth 提供单口令认证：口令校验、令牌签发/校验/吊销、按来源 IP 的失败限流。
//
// 设计约束（见 docs/E4-contract.md）：
//   - 只有一个口令，不存在用户表、账号、角色；
//   - 令牌用 crypto/rand 生成，服务端只保存 SHA-256 摘要；
//   - 限流按来源 IP 计，只存在内存中，进程重启即清空；
//   - 不 import net/http，也不 import internal/store，便于脱离服务单独测试。
package auth
