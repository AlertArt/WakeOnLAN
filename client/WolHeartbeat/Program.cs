using System;
using Avalonia;

// 本程序是 Windows 专用（托盘/Win32 P/Invoke/shutdown.exe），汇编级标注让平台分析器不再告警。
[assembly: System.Runtime.Versioning.SupportedOSPlatform("windows")]

namespace WolHeartbeat;

internal static class Program
{
    private const string MutexName = @"Local\WolHeartbeat.SingleInstance";

    // Avalonia 初始化前不要调用任何 Avalonia API（见 Avalonia 官方模板注释）。
    [STAThread]
    public static int Main(string[] args)
    {
        // 演练模式：收到关机/重启指令只记录不执行，用于验收与演示。
        CommandRunner.DryRun = CommandLine.HasFlag(args, "--dry-run") ||
                               Environment.GetEnvironmentVariable("WOL_SHUTDOWN_DRYRUN") == "1";

        // 无界面模式：--once 只上报一次，便于排查与验收（退出码 0 成功 / 1 失败 / 2 配置有误）。
        if (CommandLine.HasFlag(args, "--once"))
        {
            return CommandLine.RunOnceAsync(args).GetAwaiter().GetResult();
        }

        // 只允许一个实例：双开会导致同一台机器重复上报、日志互相覆盖。
        // 第二次启动时通知已在托盘里的实例把窗口弹出来，避免用户以为没启动。
        using var mutex = new System.Threading.Mutex(true, MutexName, out var isFirst);
        if (!isFirst)
        {
            AppLog.Warn("已有另一个客户端实例在运行，已通知它显示窗口");
            SingleInstance.RequestShow();
            return 0;
        }

        BuildAvaloniaApp().StartWithClassicDesktopLifetime(args);
        return 0;
    }

    public static AppBuilder BuildAvaloniaApp()
        => AppBuilder.Configure<App>()
            .UsePlatformDetect()
            .WithInterFont()
            .LogToTrace();
}
