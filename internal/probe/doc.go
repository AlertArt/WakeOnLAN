// Package probe 负责唤醒结果校验（F3）：Ping 或 TCP 端口探测。
//
// 文件分工：
//   - probe.go  探测器本身，纯函数式，不依赖数据库与 HTTP，可单独调用；
//   - runner.go 校验任务的生命周期（进程内单任务、120 秒上限、5 秒间隔）。
package probe
