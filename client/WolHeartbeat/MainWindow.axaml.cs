using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Threading.Tasks;
using Avalonia.Controls;
using Avalonia.Interactivity;
using Avalonia.Media;
using Avalonia.Threading;

namespace WolHeartbeat;

public partial class MainWindow : Window
{
    private const int MaxLogLines = 400;

    private static readonly IBrush OkBrush = new SolidColorBrush(Color.Parse("#16A34A"));
    private static readonly IBrush ErrBrush = new SolidColorBrush(Color.Parse("#DC2626"));
    private static readonly IBrush IdleBrush = new SolidColorBrush(Color.Parse("#9CA3AF"));
    private static readonly IBrush BusyBrush = new SolidColorBrush(Color.Parse("#D97706"));

    private readonly HeartbeatService _service = new();
    private readonly DispatcherTimer _tick;
    private readonly Queue<string> _lines = new();

    public MainWindow()
    {
        InitializeComponent();

        _tick = new DispatcherTimer { Interval = TimeSpan.FromSeconds(1) };
        _tick.Tick += (_, _) => UpdateCountdown();
        _tick.Start();

        ApplyToForm(ConfigStore.Load());

        _service.Reported += result => Dispatcher.UIThread.Post(() => ShowResult(result));
        _service.NextDueChanged += _ => Dispatcher.UIThread.Post(UpdateCountdown);
        AppLog.LineWritten += line => Dispatcher.UIThread.Post(() => AppendLine(line));

        Loaded += OnWindowLoaded;
        Closed += OnWindowClosed;
    }

    private void OnWindowLoaded(object? sender, RoutedEventArgs e)
    {
        LoadLogTail();
        UpdateLogPath();

        AppLog.Info($"客户端启动，日志目录 {AppLog.DirectoryPath}");

        if (StartOnLaunchCheck.IsChecked == true && ConfigStore.Validate(ReadForm()).Count == 0)
        {
            StartRun(save: false);
        }
    }

    private void OnWindowClosed(object? sender, EventArgs e)
    {
        _tick.Stop();
        _service.Dispose();
        AppLog.Info("客户端退出");
    }

    // ---------- 按钮 ----------

    private void OnStart(object? sender, RoutedEventArgs e) => StartRun(save: true);

    private void OnStop(object? sender, RoutedEventArgs e)
    {
        _service.Stop();
        SetStatus("已停止", IdleBrush);
        StartButton.IsEnabled = true;
        StopButton.IsEnabled = false;
        LastReportText.Text = "最近上报：—";
        UpdateCountdown();
    }

    private async void OnSendOnce(object? sender, RoutedEventArgs e)
    {
        var cfg = ReadForm();
        var errors = ConfigStore.Validate(cfg);
        if (errors.Count > 0)
        {
            ShowError(errors);
            return;
        }

        HideError();
        SendOnceButton.IsEnabled = false;
        SetStatus("正在上报…", BusyBrush);

        try
        {
            _service.Configure(cfg);
            var result = await _service.SendOnceAsync();
            ShowResult(result);
        }
        catch (Exception ex)
        {
            SetStatus("上报失败", ErrBrush);
            ShowError([ex.Message]);
        }
        finally
        {
            SendOnceButton.IsEnabled = true;
        }
    }

    private void OnDetectMac(object? sender, RoutedEventArgs e)
    {
        var mac = MacUtil.DetectLocal();
        if (mac is null)
        {
            ShowError(["没有识别到已连接的物理网卡，请手动填写 MAC"]);
            return;
        }

        MacBox.Text = mac;
        HideError();
        AppLog.Info($"自动识别本机 MAC：{mac}");
    }

    private void OnOpenLog(object? sender, RoutedEventArgs e)
    {
        try
        {
            Directory.CreateDirectory(AppLog.DirectoryPath);
            Process.Start(new ProcessStartInfo
            {
                FileName = AppLog.DirectoryPath,
                UseShellExecute = true,
            });
        }
        catch (Exception ex)
        {
            ShowError([$"打不开日志目录：{ex.Message}"]);
        }
    }

    private void OnRunAtLogonChanged(object? sender, RoutedEventArgs e)
    {
        var wanted = RunAtLogonCheck.IsChecked == true;
        if (AutoStart.Set(wanted))
        {
            AppLog.Info(wanted ? "已设置登录时自动启动" : "已取消登录时自动启动");
        }
        else
        {
            RunAtLogonCheck.IsChecked = AutoStart.IsEnabled();
        }

        var cfg = ConfigStore.Load();
        cfg.RunAtLogon = AutoStart.IsEnabled();
        ConfigStore.Save(cfg);
    }

    // ---------- 上报 ----------

    private void StartRun(bool save)
    {
        var cfg = ReadForm();
        var errors = ConfigStore.Validate(cfg);
        if (errors.Count > 0)
        {
            ShowError(errors);
            return;
        }

        HideError();

        if (ConfigStore.TryNormalizeServer(cfg.Server, out var normalized, out _))
        {
            cfg.Server = normalized;
            ServerBox.Text = normalized;
        }

        if (save)
        {
            ConfigStore.Save(cfg);
            AutoStart.Set(cfg.RunAtLogon);
        }

        try
        {
            _service.Start(cfg);
        }
        catch (Exception ex)
        {
            SetStatus("启动失败", ErrBrush);
            ShowError([ex.Message]);
            return;
        }

        SetStatus("上报中…", BusyBrush);
        StartButton.IsEnabled = false;
        StopButton.IsEnabled = true;
        UpdateCountdown();
    }

    private void ShowResult(HeartbeatResult result)
    {
        LastReportText.Text = $"最近上报：{result.At:HH:mm:ss} · {result.Message}";

        if (result.Ok)
        {
            SetStatus("上报正常", OkBrush);
        }
        else
        {
            SetStatus("上报失败", ErrBrush);
        }
    }

    private void SetStatus(string text, IBrush brush)
    {
        StatusText.Text = text;
        StatusDot.Fill = brush;
    }

    private void UpdateCountdown()
    {
        if (!_service.IsRunning)
        {
            NextDueText.Text = "下次上报：—";
            return;
        }

        var due = _service.NextDueAt;
        if (due is null)
        {
            NextDueText.Text = "下次上报：正在计算…";
            return;
        }

        var seconds = (int)Math.Round((due.Value - DateTime.Now).TotalSeconds);
        NextDueText.Text = seconds > 0
            ? $"下次上报：约 {seconds} 秒后"
            : "下次上报：即将发送";
    }

    // ---------- 表单与日志 ----------

    private void ApplyToForm(ClientConfig cfg)
    {
        ServerBox.Text = cfg.Server;
        SecretBox.Text = cfg.Secret;
        MacBox.Text = string.IsNullOrWhiteSpace(cfg.Mac) ? MacUtil.DetectLocal() ?? "" : cfg.Mac;
        IntervalBox.Value = Math.Clamp(cfg.IntervalSeconds, 15, 600);
        StartOnLaunchCheck.IsChecked = cfg.StartOnLaunch;
        RunAtLogonCheck.IsChecked = cfg.RunAtLogon || AutoStart.IsEnabled();
    }

    private ClientConfig ReadForm() => new()
    {
        Server = ServerBox.Text?.Trim() ?? "",
        Secret = SecretBox.Text?.Trim() ?? "",
        Mac = MacBox.Text?.Trim() ?? "",
        IntervalSeconds = (int)(IntervalBox.Value ?? 60),
        StartOnLaunch = StartOnLaunchCheck.IsChecked == true,
        RunAtLogon = RunAtLogonCheck.IsChecked == true,
    };

    private void ShowError(IEnumerable<string> errors)
    {
        ErrorText.Text = string.Join("\n", errors);
        ErrorText.IsVisible = true;
    }

    private void HideError()
    {
        ErrorText.IsVisible = false;
        ErrorText.Text = "";
    }

    private void UpdateLogPath()
    {
        LogPathText.Text = AppLog.IsFallbackDirectory
            ? $"日志：{AppLog.DirectoryPath}（程序目录不可写，已改用用户目录）"
            : $"日志：{AppLog.CurrentFile}";
    }

    private void LoadLogTail()
    {
        _lines.Clear();
        foreach (var line in AppLog.Tail(MaxLogLines))
        {
            _lines.Enqueue(line);
        }

        RenderLog();
    }

    private void AppendLine(string line)
    {
        _lines.Enqueue(line);
        while (_lines.Count > MaxLogLines)
        {
            _lines.Dequeue();
        }

        RenderLog();
    }

    private void RenderLog()
    {
        var text = string.Join("\n", _lines);
        LogBox.Text = text;
        LogBox.CaretIndex = text.Length;
    }
}
