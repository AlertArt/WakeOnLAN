using System;
using System.Collections.Generic;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Threading;
using System.Threading.Tasks;

namespace WolHeartbeat;

public sealed record HeartbeatResult(bool Ok, int Code, string Message, int IntervalSeconds, DateTime At);

/// <summary>服务端下发的指令（字段与 docs/F10-command-contract.md 一致）。</summary>
public sealed record RemoteCommand(long Id, string Type, int DelaySeconds, bool Force);

/// <summary>指令执行结果，供界面展示。</summary>
public sealed record CommandOutcome(long Id, string Type, bool Ok, string Message);

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

    // 待回执队列：执行完指令后立刻随下一次心跳（必要时立即补一次）上报。
    private readonly List<CommandAck> _acks = [];
    private readonly Lock _ackLock = new();
    private bool _supportsCommands;

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

    /// <summary>收到并执行完一条远程指令后触发（后台线程）。</summary>
    public event Action<CommandOutcome>? CommandExecuted;

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
        var sentAcks = SnapshotAcks();
        var payload = sentAcks.Length == 0 || !_supportsCommands
            ? JsonSerializer.Serialize(new { mac = _mac, secret = _secret })
            : JsonSerializer.Serialize(new { mac = _mac, secret = _secret, acks = sentAcks });

        try
        {
            using var content = new StringContent(payload, Encoding.UTF8, "application/json");
            using var response = await _http.PostAsync(_endpoint, content, ct).ConfigureAwait(false);
            var body = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
            var code = (int)response.StatusCode;

            if (response.IsSuccessStatusCode)
            {
                ClearAcks(sentAcks);

                var interval = ReadInt(body, "interval_seconds", _intervalSeconds);
                HandleRemoteCommands(body);

                // 关机指令执行后机器随时会断，回执不能等到下一个周期，立刻补发一次。
                if (HasQueuedAcks && _supportsCommands)
                {
                    await FlushAcksAsync(ct).ConfigureAwait(false);
                }

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

    // ---------- F10：指令接收、执行与回执 ----------

    /// <summary>解析心跳回执里的 features 与 commands，逐条执行并排队回执。</summary>
    private void HandleRemoteCommands(string body)
    {
        JsonDocument doc;
        try
        {
            doc = JsonDocument.Parse(body);
        }
        catch (JsonException)
        {
            return;
        }

        using (doc)
        {
            if (doc.RootElement.TryGetProperty("features", out var features) &&
                features.ValueKind == JsonValueKind.Array)
            {
                foreach (var item in features.EnumerateArray())
                {
                    if (item.ValueKind == JsonValueKind.String && item.GetString() == "commands")
                    {
                        _supportsCommands = true;
                    }
                }
            }

            if (!doc.RootElement.TryGetProperty("commands", out var commands) ||
                commands.ValueKind != JsonValueKind.Array)
            {
                return;
            }

            foreach (var item in commands.EnumerateArray())
            {
                if (!item.TryGetProperty("id", out var idProp) || !idProp.TryGetInt64(out var id))
                {
                    continue;
                }

                var type = item.TryGetProperty("type", out var typeProp) ? typeProp.GetString() ?? "" : "";
                var delay = item.TryGetProperty("delay_seconds", out var delayProp) && delayProp.TryGetInt32(out var d) ? d : 60;
                var force = item.TryGetProperty("force", out var forceProp) && forceProp.ValueKind == JsonValueKind.True;

                AppLog.Info($"收到远程指令：{type}，延时 {delay} 秒，强制={force}");

                var (ok, message) = CommandRunner.Execute(type, delay, force);
                QueueAck(new CommandAck(id, ok, message));
                CommandExecuted?.Invoke(new CommandOutcome(id, type, ok, message));
            }
        }
    }

    private void QueueAck(CommandAck ack)
    {
        lock (_ackLock)
        {
            _acks.Add(ack);
        }
    }

    private bool HasQueuedAcks
    {
        get
        {
            lock (_ackLock)
            {
                return _acks.Count > 0;
            }
        }
    }

    private CommandAck[] SnapshotAcks()
    {
        lock (_ackLock)
        {
            return _acks.ToArray();
        }
    }

    private void ClearAcks(CommandAck[] sent)
    {
        if (sent.Length == 0)
        {
            return;
        }

        lock (_ackLock)
        {
            foreach (var ack in sent)
            {
                _acks.Remove(ack);
            }
        }
    }

    /// <summary>单独补发一次心跳，只为了把回执送到服务端；失败不影响主流程。</summary>
    private async Task FlushAcksAsync(CancellationToken ct)
    {
        var acks = SnapshotAcks();
        if (acks.Length == 0)
        {
            return;
        }

        try
        {
            var payload = JsonSerializer.Serialize(new { mac = _mac, secret = _secret, acks });
            using var content = new StringContent(payload, Encoding.UTF8, "application/json");
            using var response = await _http.PostAsync(_endpoint, content, ct).ConfigureAwait(false);
            if (response.IsSuccessStatusCode)
            {
                ClearAcks(acks);
                AppLog.Info($"指令回执已送达服务端（{acks.Length} 条）");
            }
            else
            {
                AppLog.Warn($"指令回执发送失败：HTTP {(int)response.StatusCode}");
            }
        }
        catch (Exception ex)
        {
            AppLog.Warn($"指令回执发送失败：{ex.Message}");
        }
    }

    /// <summary>回执报文，字段名与契约一致。</summary>
    private sealed record CommandAck(
        [property: JsonPropertyName("id")] long Id,
        [property: JsonPropertyName("ok")] bool Ok,
        [property: JsonPropertyName("message")] string Message);

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
