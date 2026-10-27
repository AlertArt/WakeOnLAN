using System.Threading;

namespace WolHeartbeat;

/// <summary>
/// 单实例之间的唤起通道：再次双击 exe 时，不新建进程，而是让已在托盘里的实例把窗口弹出来。
/// 否则用户看不到界面会以为程序没启动。
/// </summary>
internal static class SingleInstance
{
    private const string ShowEventName = @"Local\WolHeartbeat.ShowWindow";

    /// <summary>创建（或打开）唤起事件，供常驻实例等待。</summary>
    public static EventWaitHandle ShowSignal()
    {
        return new EventWaitHandle(false, EventResetMode.AutoReset, ShowEventName);
    }

    /// <summary>通知已运行的实例显示窗口；没有实例在跑时什么也不做。</summary>
    public static void RequestShow()
    {
        try
        {
            using var signal = new EventWaitHandle(false, EventResetMode.AutoReset, ShowEventName);
            signal.Set();
        }
        catch
        {
            // 唤不起来不影响大局：用户仍可点托盘图标。
        }
    }
}
