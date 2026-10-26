using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace WolHeartbeat;

/// <summary>
/// 客户端配置。存放于程序所在目录的 config.json，与 exe 一起拷贝即迁移。
/// </summary>
public sealed class ClientConfig
{
    public string Server { get; set; } = "";

    public string Secret { get; set; } = "";

    public string Mac { get; set; } = "";

    public int IntervalSeconds { get; set; } = 60;

    /// <summary>启动程序后自动开始上报。</summary>
    public bool StartOnLaunch { get; set; } = true;

    /// <summary>随 Windows 登录自动启动（写入 HKCU 的 Run 项）。</summary>
    public bool RunAtLogon { get; set; }
}

public static class ConfigStore
{
    private static readonly JsonSerializerOptions Json = new()
    {
        WriteIndented = true,
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        PropertyNameCaseInsensitive = true,
    };

    private static readonly Regex SecretPattern = new("^[A-Za-z0-9_-]{16,64}$", RegexOptions.Compiled);

    public static string Path => System.IO.Path.Combine(AppContext.BaseDirectory, "config.json");

    public static ClientConfig Load()
    {
        try
        {
            if (!File.Exists(Path))
            {
                return new ClientConfig();
            }

            var json = File.ReadAllText(Path, Encoding.UTF8);
            var cfg = JsonSerializer.Deserialize<ClientConfig>(json, Json);
            if (cfg is null)
            {
                AppLog.Warn("config.json 内容为空，按默认配置启动");
                return new ClientConfig();
            }

            cfg.Server = cfg.Server.Trim();
            cfg.Secret = cfg.Secret.Trim();
            cfg.Mac = cfg.Mac.Trim();
            if (cfg.IntervalSeconds is < 15 or > 600)
            {
                cfg.IntervalSeconds = 60;
            }

            return cfg;
        }
        catch (Exception ex)
        {
            AppLog.Error($"读取 config.json 失败，改用默认配置：{ex.Message}");
            return new ClientConfig();
        }
    }

    public static bool Save(ClientConfig cfg)
    {
        try
        {
            File.WriteAllText(Path, JsonSerializer.Serialize(cfg, Json) + "\n", new UTF8Encoding(false));
            AppLog.Info($"配置已保存：{Path}");
            return true;
        }
        catch (Exception ex)
        {
            AppLog.Error($"保存 config.json 失败：{ex.Message}");
            return false;
        }
    }

    /// <summary>返回校验错误信息；空列表表示可用。</summary>
    public static List<string> Validate(ClientConfig cfg)
    {
        var errors = new List<string>();

        if (!TryNormalizeServer(cfg.Server, out _, out var serverError))
        {
            errors.Add(serverError);
        }

        if (!SecretPattern.IsMatch(cfg.Secret ?? ""))
        {
            errors.Add("心跳密钥需为 16–64 位，只能是字母、数字、下划线或连字符（与网页里设备的密钥一致）");
        }

        if (MacUtil.Normalize(cfg.Mac) is null)
        {
            errors.Add("MAC 地址格式不对，示例：AA:BB:CC:DD:EE:FF");
        }

        if (cfg.IntervalSeconds is < 15 or > 600)
        {
            errors.Add("上报间隔需在 15–600 秒之间");
        }

        return errors;
    }

    /// <summary>把用户填的地址补全为 http(s)://主机[:端口] 形式。</summary>
    public static bool TryNormalizeServer(string raw, out string normalized, out string error)
    {
        normalized = "";
        error = "";

        var text = (raw ?? "").Trim();
        if (text.Length == 0)
        {
            error = "请填写服务器地址，例如 http://192.168.1.10:8080";
            return false;
        }

        if (!text.Contains("://", StringComparison.Ordinal))
        {
            text = "http://" + text;
        }

        if (!Uri.TryCreate(text, UriKind.Absolute, out var uri) ||
            (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps) ||
            string.IsNullOrEmpty(uri.Host))
        {
            error = "服务器地址无法解析，示例：http://192.168.1.10:8080";
            return false;
        }

        normalized = uri.GetLeftPart(UriPartial.Authority);
        return true;
    }
}
