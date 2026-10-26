using System;
using System.IO;
using Microsoft.Win32;

namespace WolHeartbeat;

/// <summary>
/// 登录自启：写入当前用户的 Run 项（HKCU），不需要管理员权限，也不注册服务。
/// 开发时以 dotnet.exe 运行时自动跳过，避免把 dotnet 写进自启项。
/// </summary>
internal static class AutoStart
{
    private const string RunKeyPath = @"Software\Microsoft\Windows\CurrentVersion\Run";
    private const string ValueName = "WolHeartbeat";

    public static bool IsSupported
    {
        get
        {
            if (!OperatingSystem.IsWindows())
            {
                return false;
            }

            var path = Environment.ProcessPath;
            if (string.IsNullOrEmpty(path) || !path.EndsWith(".exe", StringComparison.OrdinalIgnoreCase))
            {
                return false;
            }

            return !string.Equals(Path.GetFileName(path), "dotnet.exe", StringComparison.OrdinalIgnoreCase);
        }
    }

    public static bool IsEnabled()
    {
        if (!OperatingSystem.IsWindows())
        {
            return false;
        }

        try
        {
            using var key = Registry.CurrentUser.OpenSubKey(RunKeyPath);
            return key?.GetValue(ValueName) is string value && value.Length > 0;
        }
        catch
        {
            return false;
        }
    }

    /// <summary>返回是否设置成功。</summary>
    public static bool Set(bool enabled)
    {
        if (!OperatingSystem.IsWindows() || !IsSupported)
        {
            AppLog.Warn("当前运行方式不支持设置登录自启（需以发布出的 exe 运行）");
            return false;
        }

        try
        {
            using var key = Registry.CurrentUser.CreateSubKey(RunKeyPath, writable: true);
            if (key is null)
            {
                return false;
            }

            if (enabled)
            {
                key.SetValue(ValueName, $"\"{Environment.ProcessPath}\"");
            }
            else if (key.GetValue(ValueName) is not null)
            {
                key.DeleteValue(ValueName, throwOnMissingValue: false);
            }

            return true;
        }
        catch (Exception ex)
        {
            AppLog.Error($"设置登录自启失败：{ex.Message}");
            return false;
        }
    }
}
