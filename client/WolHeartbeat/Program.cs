using System;
using Avalonia;

namespace WolHeartbeat;

internal static class Program
{
    private const string MutexName = @"Local\WolHeartbeat.SingleInstance";

    // Avalonia 初始化前不要调用任何 Avalonia API（见 Avalonia 官方模板注释）。
    [STAThread]
    public static int Main(string[] args)
    {
        // 无界面模式：--once 只上报一次，便于排查与验收（退出码 0 成功 / 1 失败 / 2 配置有误）。
        if (CommandLine.HasFlag(args, "--once"))
        {
            return CommandLine.RunOnceAsync(args).GetAwaiter().GetResult();
        }

        // 只允许一个实例：双开会导致同一台机器重复上报、日志互相覆盖。
        using var mutex = new System.Threading.Mutex(true, MutexName, out var isFirst);
        if (!isFirst)
        {
            AppLog.Warn("已有另一个客户端实例在运行，本次启动退出（避免重复上报）");
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
