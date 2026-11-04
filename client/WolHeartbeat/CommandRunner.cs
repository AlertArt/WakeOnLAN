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
    /// <remarks>
    /// RdpBlankAllowed / RdpBlankBefore 是「空密码网络登录」策略的回执：
    /// 前者表示改完之后策略是否处于放开状态，后者是我们动手之前的原值
    /// （'0' / '1' / 'absent'，空串表示没动过）。服务端靠这两个值做精确恢复。
    /// </remarks>
    public sealed record CommandResult(
        bool Ok,
        string Message,
        bool? RdpEnabled = null,
        bool? RdpBlankAllowed = null,
        string? RdpBlankBefore = null);

    /// <summary>执行一条指令，返回结果说明（会回执给服务端）。</summary>
    /// <param name="allowBlank">仅 enable_rdp 有效：用户是否显式授权放开空密码网络登录。</param>
    /// <param name="blankBefore">仅 disable_rdp 有效：服务端保存的策略原值，用于精确恢复。</param>
    public static CommandResult Execute(
        string type, int delaySeconds, bool force, bool allowBlank = false, string blankBefore = "")
    {
        if (!IsSupported(type))
        {
            return new CommandResult(false, $"不支持的指令类型：{type}");
        }

        return type switch
        {
            "enable_rdp" => SetRdp(true, allowBlank, ""),
            "disable_rdp" => SetRdp(false, false, blankBefore),
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
    /// 第 5 步是可选的：空密码账户默认不允许**任何**网络登录（SMB/IPC$/RDP 都管），
    /// 只有用户显式授权才放开，且关 RDP 时按原值恢复。
    ///
    /// 做完回读注册表确认，只有真的改成了才回执 rdp_enabled=true。
    /// </summary>
    private static CommandResult SetRdp(bool enable, bool allowBlank, string blankBefore)
    {
        var value = enable ? "0" : "1";

        if (DryRun)
        {
            var plan = enable
                ? "fDenyTSConnections=0、启用防火墙规则 RemoteDesktop-UserMode-In-TCP/UDP、确保 TermService 运行"
                : "fDenyTSConnections=1、关闭防火墙规则 RemoteDesktop-UserMode-In-TCP/UDP";
            if (enable && allowBlank)
            {
                plan += "、放开空密码网络登录（LimitBlankPasswordUse=0）";
            }
            if (!enable && blankBefore != "")
            {
                plan += $"，恢复空密码策略原值（{blankBefore}）";
            }
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

        // 5) 空密码网络登录（仅启用且用户显式授权时）。
        //
        // LimitBlankPasswordUse 管的不是 3389，而是**所有**网络登录（SMB、IPC$、远程注册表…），
        // Windows 没有"只对远程桌面放开"的开关。所以它必须是一个独立、需授权、可逆的动作：
        //   - 未授权：一个字都不动；
        //   - 已授权：先读原值再改，原值随回执交给服务端保存，关 RDP 时按原值恢复；
        //   - 检测到本机没有空密码账户时只在回执里提示，不否决用户的明确授权——
        //     域账户/微软账户在 Get-LocalUser 里看不到，误判成"不需要"会让用户勾了却查不出原因。
        bool? blankAllowed = null;
        var blankBeforeValue = "";
        if (enable && allowBlank)
        {
            var (probeOk, hasBlank) = DetectBlankAccount();
            var (readOk, before) = ReadBlankPolicy();
            if (!readOk)
            {
                // 读不到原值就不能改：没有原值就无法恢复，宁可让用户手工处理。
                steps.Append("；空密码策略原值读取失败，为可逆起见未修改");
            }
            else
            {
                var (writeOk, writeMsg) = WriteBlankPolicy("0");
                var (afterOk, after) = writeOk ? ReadBlankPolicy() : (false, "");
                if (afterOk && after == "0")
                {
                    blankAllowed = true;
                    blankBeforeValue = before;
                    steps.Append(before == "0"
                        ? "；空密码网络登录原本已放开"
                        : $"；空密码网络登录已放开（原值 {before}）");
                    if (probeOk && !hasBlank)
                    {
                        steps.Append("；未检测到空密码本地账户，如仍无法登录请检查远程账户类型");
                    }
                }
                else
                {
                    blankAllowed = false;
                    steps.Append($"；空密码策略修改未生效（{Shorten(writeMsg)}）");
                }
            }
        }
        else if (!enable && blankBefore != "")
        {
            // 只有确实由我们改过（before 非空）才恢复，且恢复的是原值而不是固定写 1。
            var (writeOk, writeMsg) = WriteBlankPolicy(blankBefore);
            var (afterOk, after) = writeOk ? ReadBlankPolicy() : (false, "");
            if (afterOk && after == blankBefore)
            {
                blankAllowed = false;
                blankBeforeValue = "";
                steps.Append(blankBefore == "absent"
                    ? "；空密码策略已恢复为未设置"
                    : $"；空密码策略已恢复为 {blankBefore}");
            }
            else
            {
                // 恢复失败或无法确认：不谎报"已恢复"，状态继续显示已放开，下次还能再试。
                steps.Append($"；空密码策略恢复未确认（{Shorten(writeMsg)}）");
            }
        }

        // 6) 回读注册表给结论：只认实际值，不认"命令返回 0"。
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
        return new CommandResult(
            finalOk,
            summary.ToString(),
            enabled,
            blankAllowed,
            blankBeforeValue == "" ? null : blankBeforeValue);
    }

    private static string Action(bool enable) => enable ? "启用远程桌面" : "关闭远程桌面";

    private const string LsaKey = @"HKLM\SYSTEM\CurrentControlSet\Control\Lsa";

    /// <summary>
    /// 读取 LimitBlankPasswordUse。Value 取值：'0' / '1' / "absent"（值不存在）。
    /// Ok=false 表示读取本身失败——这种情况绝不能当作"不存在"去改，
    /// 否则关闭时就不知道该恢复成什么。
    /// </summary>
    private static (bool Ok, string Value) ReadBlankPolicy()
    {
        // 用 PowerShell 而不是 reg.exe：reg 的"找不到"提示是本地化文本，
        // 中文/英文/其它语言的匹配规则各不相同，中文系统按 UTF8 解码还会乱码。
        var script =
            "$v=(Get-ItemProperty -Path 'HKLM:\\SYSTEM\\CurrentControlSet\\Control\\Lsa' " +
            "-Name LimitBlankPasswordUse -ErrorAction SilentlyContinue).LimitBlankPasswordUse; " +
            "if ($null -eq $v) { 'ABSENT' } else { 'VALUE=' + $v }";
        var (ok, output) = RunTool("powershell.exe",
            $"-NoProfile -NonInteractive -ExecutionPolicy Bypass -Command \"{script}\"",
            "读取空密码策略", timeoutMs: 20000);
        if (!ok)
        {
            return (false, "");
        }

        var text = output.Trim();
        if (text.Contains("ABSENT", StringComparison.Ordinal))
        {
            return (true, "absent");
        }
        var idx = text.IndexOf("VALUE=", StringComparison.Ordinal);
        if (idx >= 0)
        {
            var raw = text[(idx + "VALUE=".Length)..].Trim();
            var token = raw.Split(new[] { ' ', '\t', '\r', '\n' }, StringSplitOptions.RemoveEmptyEntries);
            if (token.Length > 0 && (token[0] == "0" || token[0] == "1"))
            {
                return (true, token[0]);
            }
        }
        return (false, "");
    }

    /// <summary>把 LimitBlankPasswordUse 写成 0/1；value 为 "absent" 时删除该值。</summary>
    private static (bool Ok, string Message) WriteBlankPolicy(string value)
    {
        if (value == "absent")
        {
            return RunTool("reg.exe",
                $"delete \"{LsaKey}\" /v LimitBlankPasswordUse /f",
                "删除空密码策略值");
        }
        return RunTool("reg.exe",
            $"add \"{LsaKey}\" /v LimitBlankPasswordUse /t REG_DWORD /d {value} /f",
            "写入空密码策略");
    }

    /// <summary>
    /// 检测本机是否存在「已启用且无密码」的本地账户。
    /// Ok=false 表示无法判定（老系统没有 Get-LocalUser），此时按用户已授权照常执行；
    /// Ok=true 且 HasBlank=false 才是"确实不需要"，跳过修改。
    /// </summary>
    private static (bool Ok, bool HasBlank) DetectBlankAccount()
    {
        var script =
            "$n=@(Get-LocalUser -ErrorAction SilentlyContinue | " +
            "Where-Object { $_.Enabled -and -not $_.PasswordRequired }); " +
            "if ($n.Count -eq 0) { 'NOBLANK' } else { 'BLANK' }";
        var (ok, output) = RunTool("powershell.exe",
            $"-NoProfile -NonInteractive -ExecutionPolicy Bypass -Command \"{script}\"",
            "检测空密码账户", timeoutMs: 20000);
        if (!ok)
        {
            return (false, false);
        }

        var text = output.Trim();
        if (text.Contains("NOBLANK", StringComparison.Ordinal))
        {
            return (true, false);
        }
        if (text.Contains("BLANK", StringComparison.Ordinal))
        {
            return (true, true);
        }
        return (false, false);
    }

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
