using System;
using System.Runtime.InteropServices;
using System.Threading.Tasks;

namespace WolHeartbeat;

/// <summary>
/// 无界面入口：--once 只上报一次后退出。
/// 用途：部署时先确认密钥对不对，不必打开窗口等人看。
/// 退出码：0 成功 / 1 上报失败 / 2 配置有误。
/// </summary>
internal static class CommandLine
{
    public static bool HasFlag(string[] args, string flag)
    {
        foreach (var arg in args)
        {
            if (string.Equals(arg, flag, StringComparison.OrdinalIgnoreCase))
            {
                return true;
            }
        }

        return false;
    }

    public static async Task<int> RunOnceAsync(string[] args)
    {
        AttachConsole(-1); // WinExe 默认没有控制台，attach 到调用者的控制台才能看到输出
        TryUseUtf8Console();

        var cfg = ConfigStore.Load();
        Override(cfg, args, "--server", v => cfg.Server = v);
        Override(cfg, args, "--secret", v => cfg.Secret = v);
        Override(cfg, args, "--mac", v => cfg.Mac = v);

        if (string.IsNullOrWhiteSpace(cfg.Mac) && MacUtil.DetectLocal() is { } detected)
        {
            cfg.Mac = detected;
        }

        var errors = ConfigStore.Validate(cfg);
        if (errors.Count > 0)
        {
            foreach (var error in errors)
            {
                AppLog.Error("配置有误：" + error);
                Say("配置有误：" + error);
            }

            return 2;
        }

        using var service = new HeartbeatService();
        service.Configure(cfg);
        var result = await service.SendOnceAsync().ConfigureAwait(false);

        Say(result.Message);
        return result.Ok ? 0 : 1;
    }

    private static void Override(ClientConfig cfg, string[] args, string name, Action<string> apply)
    {
        for (var i = 0; i < args.Length - 1; i++)
        {
            if (string.Equals(args[i], name, StringComparison.OrdinalIgnoreCase))
            {
                apply(args[i + 1]);
                return;
            }
        }
    }

    private static void Say(string text)
    {
        try
        {
            Console.WriteLine(text);
            Console.Out.Flush();
        }
        catch
        {
            // 没有控制台时只写日志。
        }
    }

    private static void TryUseUtf8Console()
    {
        try
        {
            Console.OutputEncoding = System.Text.Encoding.UTF8;
        }
        catch
        {
            // 个别宿主不允许改输出编码，中文会显示乱码但不影响退出码与日志。
        }
    }

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool AttachConsole(int processId);
}
