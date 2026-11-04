using System;
using System.Diagnostics;
using System.Text;

namespace WolHeartbeat;

/// <summary>
/// 执行服务端下发的指令。
/// 只做固定几种动作，不做任何字符串拼接执行，杜绝命令注入：
/// 参数全部来自本文件写死的常量，服务端只能选"执行哪一种"，不能改参数。
/// shutdown/restart 普通用户即可执行；enable_rdp/disable_rdp 需要管理员权限。
/// </summary>
internal static class CommandRunner
{
    /// <summary>演练模式：只记录不执行，用于验收与演示。</summary>
    public static bool DryRun { get; set; }

    public static readonly string[] SupportedTypes =
        ["shutdown", "restart", "enable_rdp", "disable_rdp"];

    public static bool IsSupported(string type) =>
        Array.IndexOf(SupportedTypes, type) >= 0;

    /// <summary>指令执行结果。RdpEnabled 只对 RDP 类指令有意义，null 表示"没结论"。</summary>
    public sealed record CommandResult(bool Ok, string Message, bool? RdpEnabled = null);

    /// <summary>执行一条指令，返回结果说明（会回执给服务端）。</summary>
    public static CommandResult Execute(string type, int delaySeconds, bool force)
    {
        if (!IsSupported(type))
        {
            return new CommandResult(false, $"不支持的指令类型：{type}");
        }

        return type switch
        {
            "enable_rdp" => SetRdp(true),
            "disable_rdp" => SetRdp(false),
            _ => ScheduleShutdown(type, delaySeconds, force),
        };
    }

    /// <summary>关机 / 重启：交给 shutdown.exe，带倒计时。</summary>
    private static CommandResult ScheduleShutdown(string type, int delaySeconds, bool force)
    {
        // 和服务端契约保持一致：延时下限 10 秒，给用户留撤销窗口。
        var delay = Math.Clamp(delaySeconds, 10, 600);
        var flag = type == "restart" ? "/r" : "/s";
        var args = $"{flag} /t {delay}" + (force ? " /f" : "") + " /c \"远程指令：WakeOnLAN 管理端\"";

        if (DryRun)
        {
            AppLog.Info($"[演练] 本应执行：shutdown.exe {args}");
            return new CommandResult(true, $"演练模式，未真正执行：shutdown.exe {args}");
        }

        var (ok, message) = RunTool("shutdown.exe", args, $"{type} 指令（{delay} 秒后执行）");
        return new CommandResult(ok, message);
    }

    /// <summary>撤销已经排期的关机（shutdown /a）。</summary>
    public static (bool Ok, string Message) Abort()
    {
        if (DryRun)
        {
            AppLog.Info("[演练] 本应执行：shutdown.exe /a");
            return (true, "演练模式，未真正执行：shutdown.exe /a");
        }

        return RunTool("shutdown.exe", "/a", "撤销关机");
    }

    /// <summary>
    /// 启用/关闭远程桌面。
    ///
    /// 四步都要做，缺一步就会出现"注册表开了但连不上"的经典现象：
    ///   1) fDenyTSConnections：总开关，0 = 允许远程连接；
    ///   2) 防火墙：按**规则名**启用，不能用组名——中文系统里组名是「远程桌面」，
    ///      英文系统是「Remote Desktop」，写死哪个都会在另一种语言上失效，
    ///      而规则名（RemoteDesktop-UserMode-In-TCP/UDP）与语言无关；
    ///   3) NLA：Win10 默认要求网络级身份验证，关掉反而会被策略拒绝；
    ///   4) TermService：服务没起来时注册表再对也不会监听 3389。
    ///
    /// 做完回读注册表确认，只有真的改成了才回执 rdp_enabled=true。
    /// </summary>
    private static CommandResult SetRdp(bool enable)
    {
        var value = enable ? "0" : "1";

        if (DryRun)
        {
            var plan = enable
                ? "fDenyTSConnections=0、启用防火墙规则 RemoteDesktop-UserMode-In-TCP/UDP、确保 TermService 运行"
                : "fDenyTSConnections=1、关闭防火墙规则 RemoteDesktop-UserMode-In-TCP/UDP";
            AppLog.Info($"[演练] 本应执行：{plan}");
            return new CommandResult(true, $"演练模式，未真正执行：{plan}");
        }

        var steps = new StringBuilder();

        // 1) 总开关
        var (ok, msg) = RunTool("reg.exe",
            $"add \"HKLM\\SYSTEM\\CurrentControlSet\\Control\\Terminal Server\" /v fDenyTSConnections /t REG_DWORD /d {value} /f",
            $"{Action(enable)}：写 fDenyTSConnections");
        steps.Append(Shorten(msg));
        if (!ok)
        {
            // 最常见原因是没提权：把结论说清楚，别让用户去猜"为什么网页上还显示未启用"。
            var hint = msg.Contains("拒绝访问", StringComparison.OrdinalIgnoreCase) ||
                       msg.Contains("Access is denied", StringComparison.OrdinalIgnoreCase)
                ? "需要以管理员身份运行客户端（或用 SYSTEM 计划任务启动）"
                : msg;
            return new CommandResult(false, $"{Action(enable)}失败：{hint}");
        }

        // 2) 防火墙规则（按名字，跨语言有效）
        var firewallCmd = enable
            ? "Set-NetFirewallRule -Name RemoteDesktop-UserMode-In-TCP -Enabled True -ErrorAction Stop; " +
              "Set-NetFirewallRule -Name RemoteDesktop-UserMode-In-UDP -Enabled True -ErrorAction SilentlyContinue"
            : "Set-NetFirewallRule -Name RemoteDesktop-UserMode-In-TCP -Enabled False -ErrorAction Stop; " +
              "Set-NetFirewallRule -Name RemoteDesktop-UserMode-In-UDP -Enabled False -ErrorAction SilentlyContinue";
        (ok, msg) = RunTool("powershell.exe",
            $"-NoProfile -NonInteractive -ExecutionPolicy Bypass -Command \"{firewallCmd}\"",
            $"{Action(enable)}：防火墙规则", timeoutMs: 30000);
        // 防火墙失败不直接判死：注册表已改，3389 可能照样能连（企业域里常有组策略统一放行）。
        // 但要把失败写进回执，后面再用端口监听做最终结论。
        steps.Append(ok ? "；防火墙已放行" : $"；防火墙操作未成功（{Shorten(msg)}）");

        // 3) NLA：启用时关闭（UserAuthentication=0）。浏览器网关不带 Windows 凭据，
        //    NLA 要求在会话建立前完成认证，guacd 的凭据提示框 15 秒就超时，
        //    人工输入口令根本来不及；关掉 NLA 后凭据在会话内的 Windows 登录界面
        //    输入（TLS 加密传输，安全性仍由登录口令把守）。关闭时保留原值不动。
        if (enable)
        {
            (ok, msg) = RunTool("reg.exe",
                "add \"HKLM\\SYSTEM\\CurrentControlSet\\Control\\Terminal Server\\WinStations\\RDP-Tcp\" " +
                "/v UserAuthentication /t REG_DWORD /d 0 /f",
                "关闭 NLA（凭据改在登录界面输入）");
            steps.Append(ok ? "；NLA 已关闭" : $"；NLA 设置失败（{Shorten(msg)}）");
        }

        // 4) 确保 TermService 在跑
        if (enable)
        {
            (ok, msg) = RunTool("sc.exe", "query TermService", "查询 TermService");
            if (!ok || !msg.Contains("RUNNING", StringComparison.OrdinalIgnoreCase))
            {
                (ok, msg) = RunTool("sc.exe", "start TermService", "启动 TermService", timeoutMs: 20000);
                steps.Append(ok ? "；TermService 已启动" : $"；TermService 启动失败（{Shorten(msg)}）");
            }
        }

        // 5) 回读注册表给结论：只认实际值，不认"命令返回 0"。
        var actual = ReadDenyValue();
        if (actual is null)
        {
            return new CommandResult(false, $"{Action(enable)}后无法回读注册表确认结果");
        }

        var enabled = actual == 0;
        var listening = enabled && IsRdpListening();
        var summary = new StringBuilder();
        summary.Append(enabled ? "RDP 已启用" : "RDP 已关闭");
        if (enabled && !listening)
        {
            // 注册表对了但没监听：多半是服务被策略禁用，如实回报比假装成功有用。
            summary.Append("（3389 尚未监听，可能被组策略限制）");
        }
        summary.Append(" · ").Append(steps);

        var finalOk = enabled == enable;
        AppLog.Info($"RDP 开关结果：{summary}");
        return new CommandResult(finalOk, summary.ToString(), enabled);
    }

    private static string Action(bool enable) => enable ? "启用远程桌面" : "关闭远程桌面";

    /// <summary>回读 fDenyTSConnections，读不到返回 null。</summary>
    private static int? ReadDenyValue()
    {
        var (ok, output) = RunTool("reg.exe",
            "query \"HKLM\\SYSTEM\\CurrentControlSet\\Control\\Terminal Server\" /v fDenyTSConnections",
            "回读 RDP 开关");
        if (!ok)
        {
            return null;
        }

        // 输出形如：    fDenyTSConnections    REG_DWORD    0x0
        var idx = output.IndexOf("REG_DWORD", StringComparison.OrdinalIgnoreCase);
        if (idx < 0)
        {
            return null;
        }

        var tail = output[(idx + "REG_DWORD".Length)..].Trim();
        var token = tail.Split(new[] { ' ', '\t', '\r', '\n' }, StringSplitOptions.RemoveEmptyEntries);
        if (token.Length == 0)
        {
            return null;
        }

        var raw = token[0];
        if (raw.StartsWith("0x", StringComparison.OrdinalIgnoreCase) &&
            int.TryParse(raw[2..], System.Globalization.NumberStyles.HexNumber, null, out var hex))
        {
            return hex;
        }
        return int.TryParse(raw, out var dec) ? dec : null;
    }

    /// <summary>3389 是否处于 LISTENING。仅用于回执说明，不作为控制流分支。</summary>
    private static bool IsRdpListening()
    {
        var (ok, output) = RunTool("netstat.exe", "-an -p tcp", "检查 3389 监听", timeoutMs: 15000);
        if (!ok)
        {
            return false;
        }

        foreach (var line in output.Split('\n'))
        {
            if (line.Contains(":3389", StringComparison.Ordinal) &&
                line.Contains("LISTENING", StringComparison.OrdinalIgnoreCase))
            {
                return true;
            }
        }
        return false;
    }

    /// <summary>把多行 stderr 压成一行，回执里只放摘要。</summary>
    private static string Shorten(string text)
    {
        var one = text.Trim().Replace("\r", " ").Replace("\n", " ");
        return one.Length <= 120 ? one : one[..120] + "…";
    }

    /// <summary>启动一个受控的外部命令并收集输出。</summary>
    private static (bool Ok, string Message) RunTool(
        string fileName, string args, string action, int timeoutMs = 10000)
    {
        try
        {
            var psi = new ProcessStartInfo
            {
                FileName = fileName,
                Arguments = args,
                CreateNoWindow = true,
                UseShellExecute = false,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                StandardOutputEncoding = Encoding.UTF8,
                StandardErrorEncoding = Encoding.UTF8,
            };

            using var process = Process.Start(psi);
            if (process is null)
            {
                return (false, $"{action} 启动失败：无法创建进程");
            }

            var stdout = process.StandardOutput.ReadToEnd();
            var stderr = process.StandardError.ReadToEnd();
            if (!process.WaitForExit(timeoutMs))
            {
                try { process.Kill(); } catch { /* 已退出 */ }
                return (false, $"{action} 超时未返回");
            }

            var code = process.ExitCode;
            var detail = (stderr + " " + stdout).Trim().Replace("\r", " ").Replace("\n", " ");
            if (code == 0)
            {
                AppLog.Info($"{action} 已提交：{fileName} {args}");
                return (true, detail.Length > 0 ? detail : $"{action}已执行");
            }

            var message = $"{action}失败（退出码 {code}）{(detail.Length > 0 ? "：" + detail : "")}";
            AppLog.Error(message);
            return (false, message);
        }
        catch (Exception ex)
        {
            var message = $"{action}异常：{ex.Message}";
            AppLog.Error(message);
            return (false, message);
        }
    }
}
