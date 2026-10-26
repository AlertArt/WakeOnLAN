using System;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace WolHeartbeat;

public sealed record HeartbeatResult(bool Ok, int Code, string Message, int IntervalSeconds, DateTime At);

/// <summary>
/// 心跳上报：POST {server}/api/heartbeat，请求体只有 mac 与 secret。
/// 只发起出站请求，不需要目标机开放任何入站端口。
/// </summary>
public sealed class HeartbeatService : IDisposable
{
    private const int RetrySeconds = 15;

    private readonly HttpClient _http;

    public HeartbeatService()
    {
        // 心跳走局域网直连：系统代理会把内网地址变成 502/407，一律绕开。
        var handler = new HttpClientHandler
        {
            UseProxy = false,
            UseDefaultCredentials = true,
        };
        _http = new HttpClient(handler)
        {
            Timeout = TimeSpan.FromSeconds(10),
        };
    }

    private CancellationTokenSource? _cts;
    private Task? _loop;
    private string _endpoint = "";
    private string _secret = "";
    private string _mac = "";
    private int _intervalSeconds = 60;

    /// <summary>每完成一次上报触发（后台线程），界面需自行切回 UI 线程。</summary>
    public event Action<HeartbeatResult>? Reported;

    /// <summary>下次上报时间变化时触发，null 表示已停止。</summary>
    public event Action<DateTime?>? NextDueChanged;

    public bool IsRunning => _loop is { IsCompleted: false };

    public DateTime? NextDueAt { get; private set; }

    /// <summary>先装配参数（不启动循环），供 --once 单次上报使用。</summary>
    public void Configure(ClientConfig cfg)
    {
        if (!ConfigStore.TryNormalizeServer(cfg.Server, out var server, out var error))
        {
            throw new InvalidOperationException(error);
        }

        _endpoint = server.TrimEnd('/') + "/api/heartbeat";
        _secret = cfg.Secret.Trim();
        _mac = MacUtil.Normalize(cfg.Mac) ?? throw new InvalidOperationException("MAC 地址格式不对");
        _intervalSeconds = cfg.IntervalSeconds;
    }

    public void Start(ClientConfig cfg)
    {
        Stop();
        Configure(cfg);

        _cts = new CancellationTokenSource();
        var token = _cts.Token;

        AppLog.Info($"开始上报：{_endpoint}，MAC {_mac}，间隔 {_intervalSeconds} 秒");
        _loop = Task.Run(() => LoopAsync(token), token);
    }

    public void Stop()
    {
        var cts = _cts;
        if (cts is null)
        {
            return;
        }

        _cts = null;
        try
        {
            cts.Cancel();
        }
        catch
        {
            // 忽略并发取消异常。
        }

        cts.Dispose();
        _loop = null;
        SetNextDue(null);
        AppLog.Info("已停止上报");
    }

    /// <summary>上报一次，返回结果（不发事件、不改调度）。</summary>
    public async Task<HeartbeatResult> SendOnceAsync(CancellationToken ct = default)
    {
        var payload = JsonSerializer.Serialize(new { mac = _mac, secret = _secret });

        try
        {
            using var content = new StringContent(payload, Encoding.UTF8, "application/json");
            using var response = await _http.PostAsync(_endpoint, content, ct).ConfigureAwait(false);
            var body = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
            var code = (int)response.StatusCode;

            if (response.IsSuccessStatusCode)
            {
                var interval = ReadInt(body, "interval_seconds", _intervalSeconds);
                var result = new HeartbeatResult(true, code, $"上报成功 · 服务端建议间隔 {interval} 秒", interval, DateTime.Now);
                AppLog.Info($"心跳成功：{_endpoint} → {body.Trim()}");
                return result;
            }

            var (message, isRetryable) = DescribeFailure(code, body);
            LogFailure(code, message, isRetryable);
            return new HeartbeatResult(false, code, message, 0, DateTime.Now);
        }
        catch (OperationCanceledException) when (!ct.IsCancellationRequested)
        {
            const string message = "连接超时：检查服务器地址、端口与网络连通性";
            AppLog.Warn($"心跳失败：{message}（{_endpoint}）");
            return new HeartbeatResult(false, 0, message, 0, DateTime.Now);
        }
        catch (HttpRequestException ex)
        {
            var message = $"无法连接服务器：{ex.Message}";
            AppLog.Warn($"心跳失败：{message}（{_endpoint}）");
            return new HeartbeatResult(false, 0, message, 0, DateTime.Now);
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            var message = $"上报异常：{ex.Message}";
            AppLog.Error($"心跳失败：{message}");
            return new HeartbeatResult(false, 0, message, 0, DateTime.Now);
        }
    }

    private static (string Message, bool Retryable) DescribeFailure(int code, string body) => code switch
    {
        400 => ("请求被拒绝（400）：报文格式有误，请升级客户端", false),
        401 => ("被拒绝（401）：MAC 或密钥与网页里该设备不一致", false),
        404 => ("地址不对（404）：确认服务器地址含端口，且服务端版本为 1.1.0 及以上", false),
        429 => ("请求过于频繁（429）：把上报间隔调大", true),
        >= 500 => ($"服务端错误（HTTP {code}）：稍后自动重试", true),
        _ => ($"上报失败（HTTP {code}）：{Truncate(body)}", true),
    };

    private void LogFailure(int code, string message, bool retryable)
    {
        if (code == 401)
        {
            AppLog.Error($"心跳被拒绝：{message}");
            return;
        }

        AppLog.Warn($"心跳失败：{message}{(retryable ? "，15 秒后重试" : "")}");
    }

    private static int ReadInt(string body, string name, int fallback)
    {
        try
        {
            using var doc = JsonDocument.Parse(body);
            if (doc.RootElement.TryGetProperty(name, out var value) && value.TryGetInt32(out var number))
            {
                return number;
            }
        }
        catch (JsonException)
        {
            // 响应体不是 JSON 时按默认间隔走。
        }

        return fallback;
    }

    private static string Truncate(string text)
    {
        var trimmed = (text ?? "").Trim().Replace('\n', ' ');
        return trimmed.Length <= 120 ? trimmed : trimmed[..120] + "…";
    }

    private void SetNextDue(DateTime? due)
    {
        NextDueAt = due;
        NextDueChanged?.Invoke(due);
    }

    private async Task LoopAsync(CancellationToken token)
    {
        while (!token.IsCancellationRequested)
        {
            var result = await SendOnceAsync(token).ConfigureAwait(false);
            if (token.IsCancellationRequested)
            {
                break;
            }

            Reported?.Invoke(result);

            var wait = result.Ok && result.IntervalSeconds > 0 ? result.IntervalSeconds : RetrySeconds;
            SetNextDue(DateTime.Now.AddSeconds(wait));

            try
            {
                await Task.Delay(TimeSpan.FromSeconds(wait), token).ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                break;
            }
        }

        SetNextDue(null);
    }

    public void Dispose()
    {
        Stop();
        _http.Dispose();
    }
}
