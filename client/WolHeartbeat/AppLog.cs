using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text;

namespace WolHeartbeat;

/// <summary>
/// 文件日志：写在程序所在目录的 logs/ 下，按天分文件（heartbeat-2026-10-26.log）。
/// 程序目录不可写时（例如装进 Program Files 又没给写权限）退化到
/// %LOCALAPPDATA%\WolHeartbeat\logs，并在界面上提示实际位置。
/// </summary>
public static class AppLog
{
    private const long MaxBytes = 2 * 1024 * 1024;
    private const int KeepDays = 7;

    private static readonly object Gate = new();
    private static readonly UTF8Encoding Utf8 = new(encoderShouldEmitUTF8Identifier: false);
    private static readonly Lazy<(string Dir, bool IsFallback)> Location = new(ResolveDirectory);

    /// <summary>每写入一行触发一次（后台线程），供界面实时追加。</summary>
    public static event Action<string>? LineWritten;

    public static string DirectoryPath => Location.Value.Dir;

    public static bool IsFallbackDirectory => Location.Value.IsFallback;

    public static string CurrentFile => Path.Combine(DirectoryPath, $"heartbeat-{DateTime.Now:yyyy-MM-dd}.log");

    public static void Info(string message) => Write("INFO", message);

    public static void Warn(string message) => Write("WARN", message);

    public static void Error(string message) => Write("ERROR", message);

    /// <summary>读取当前日志文件最后若干行，用于界面展示。</summary>
    public static IReadOnlyList<string> Tail(int maxLines = 400)
    {
        try
        {
            if (!File.Exists(CurrentFile))
            {
                return Array.Empty<string>();
            }

            var lines = File.ReadLines(CurrentFile, Utf8);
            var buffer = new Queue<string>(maxLines);
            foreach (var line in lines)
            {
                if (buffer.Count == maxLines)
                {
                    buffer.Dequeue();
                }

                buffer.Enqueue(line);
            }

            return buffer.ToArray();
        }
        catch
        {
            return Array.Empty<string>();
        }
    }

    private static void Write(string level, string message)
    {
        var line = $"{DateTime.Now:yyyy-MM-dd HH:mm:ss.fff} {level,-5} {message}";

        lock (Gate)
        {
            try
            {
                Directory.CreateDirectory(DirectoryPath);
                RotateIfNeeded();
                File.AppendAllText(CurrentFile, line + "\n", Utf8);
                TrimOldFiles();
            }
            catch
            {
                // 日志失败不能拖垮上报流程，界面仍会显示状态。
            }
        }

        LineWritten?.Invoke(line);
    }

    private static void RotateIfNeeded()
    {
        var file = new FileInfo(CurrentFile);
        if (!file.Exists || file.Length < MaxBytes)
        {
            return;
        }

        var backup = CurrentFile + ".1";
        try
        {
            if (File.Exists(backup))
            {
                File.Delete(backup);
            }

            File.Move(CurrentFile, backup);
        }
        catch
        {
            // 忽略：下一行继续往原文件写。
        }
    }

    private static void TrimOldFiles()
    {
        try
        {
            var limit = DateTime.Now.AddDays(-KeepDays);
            foreach (var path in Directory.EnumerateFiles(DirectoryPath, "heartbeat-*.log*"))
            {
                if (File.GetLastWriteTime(path) < limit)
                {
                    File.Delete(path);
                }
            }
        }
        catch
        {
            // 同上，清理失败无影响。
        }
    }

    private static (string Dir, bool IsFallback) ResolveDirectory()
    {
        var preferred = Path.Combine(AppContext.BaseDirectory, "logs");
        try
        {
            Directory.CreateDirectory(preferred);
            var probe = Path.Combine(preferred, ".write-test");
            File.WriteAllText(probe, "ok", Utf8);
            File.Delete(probe);
            return (preferred, false);
        }
        catch
        {
            var fallback = Path.Combine(
                Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
                "WolHeartbeat",
                "logs");
            try
            {
                Directory.CreateDirectory(fallback);
            }
            catch
            {
                // 极端情况下连用户目录都不可写，返回路径即可，写文件会静默失败。
            }

            return (fallback, true);
        }
    }
}
