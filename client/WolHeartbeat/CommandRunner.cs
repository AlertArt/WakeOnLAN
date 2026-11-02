using System;
using System.Diagnostics;
using System.Text;

namespace WolHeartbeat;

/// <summary>
/// 执行服务端下发的关机/重启指令。
/// 只做固定两种动作，不做任何字符串拼接执行，杜绝命令注入。
/// 无参数时由 Windows 的 shutdown.exe 完成，普通用户即可关本机，无需管理员权限。
/// </summary>
internal static class CommandRunner
{
    /// <summary>演练模式：只记录不执行，用于验收与演示。</summary>
    public static bool DryRun { get; set; }

    public static readonly string[] SupportedTypes = ["shutdown", "restart"];

    public static bool IsSupported(string type) =>
        Array.IndexOf(SupportedTypes, type) >= 0;

    /// <summary>执行一条指令，返回是否成功与说明（说明会回执给服务端）。</summary>
    public static (bool Ok, string Message) Execute(string type, int delaySeconds, bool force)
    {
        if (!IsSupported(type))
        {
            return (false, $"不支持的指令类型：{type}");
        }

        // 和服务端契约保持一致：延时下限 10 秒，给用户留撤销窗口。
        var delay = Math.Clamp(delaySeconds, 10, 600);
        var flag = type == "restart" ? "/r" : "/s";
        var args = $"{flag} /t {delay}" + (force ? " /f" : "") + " /c \"远程指令：WakeOnLAN 管理端\"";

        if (DryRun)
        {
            AppLog.Info($"[演练] 本应执行：shutdown.exe {args}");
            return (true, $"演练模式，未真正执行：shutdown.exe {args}");
        }

        return Run(args, $"{type} 指令（{delay} 秒后执行）");
    }

    /// <summary>撤销已经排期的关机（shutdown /a）。</summary>
    public static (bool Ok, string Message) Abort()
    {
        if (DryRun)
        {
            AppLog.Info("[演练] 本应执行：shutdown.exe /a");
            return (true, "演练模式，未真正执行：shutdown.exe /a");
        }

        return Run("/a", "撤销关机");
    }

    private static (bool Ok, string Message) Run(string args, string action)
    {
        try
        {
            var psi = new ProcessStartInfo
            {
                FileName = "shutdown.exe",
                Arguments = args,
                CreateNoWindow = true,
                UseShellExecute = false,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
            };

            using var process = Process.Start(psi);
            if (process is null)
            {
                return (false, $"{action} 启动失败：无法创建进程");
            }

            var stdout = process.StandardOutput.ReadToEnd();
            var stderr = process.StandardError.ReadToEnd();
            if (!process.WaitForExit(10000))
            {
                try { process.Kill(); } catch { /* 已退出 */ }
                return (false, $"{action} 超时未返回");
            }

            var code = process.ExitCode;
            var detail = (stderr + " " + stdout).Trim().Replace("\r", " ").Replace("\n", " ");
            if (code == 0)
            {
                AppLog.Info($"{action} 已提交：shutdown.exe {args}");
                return (true, $"{action}已执行");
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
