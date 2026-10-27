using System;
using System.Threading;
using Avalonia;
using Avalonia.Controls;
using Avalonia.Controls.ApplicationLifetimes;
using Avalonia.Markup.Xaml;
using Avalonia.Platform;
using Avalonia.Threading;

namespace WolHeartbeat;

public partial class App : Application
{
    /// <summary>两次点击落在这个间隔内视为双击（Windows 托盘只会连发两次 Clicked）。</summary>
    private const int DoubleClickMs = 400;

    private MainWindow? _window;
    private TrayIcon? _tray;
    private DateTime _lastTrayClick = DateTime.MinValue;

    public override void Initialize()
    {
        AvaloniaXamlLoader.Load(this);
    }

    public override void OnFrameworkInitializationCompleted()
    {
        if (ApplicationLifetime is IClassicDesktopStyleApplicationLifetime desktop)
        {
            // 托盘常驻：窗口隐藏时不退出进程，只有菜单"退出"才结束。
            desktop.ShutdownMode = ShutdownMode.OnExplicitShutdown;

            _window = new MainWindow();
            _window.StatusChanged += (_, text) => UpdateTrayToolTip(text);
            _window.Initialize(); // 窗口不显示也立刻开始上报

            SetupTrayIcon(desktop);
            ListenShowRequests();
        }

        base.OnFrameworkInitializationCompleted();
    }

    // ---------- 托盘 ----------

    private void SetupTrayIcon(IClassicDesktopStyleApplicationLifetime desktop)
    {
        var menu = new NativeMenu();

        var showItem = new NativeMenuItem("显示界面");
        showItem.Click += (_, _) => ShowWindow();
        menu.Items.Add(showItem);

        menu.Items.Add(new NativeMenuItemSeparator());

        var exitItem = new NativeMenuItem("退出");
        exitItem.Click += (_, _) => ExitApplication(desktop);
        menu.Items.Add(exitItem);

        _tray = new TrayIcon
        {
            Icon = new WindowIcon(AssetLoader.Open(new Uri("avares://WolHeartbeat/Assets/app.ico"))),
            ToolTipText = "WOL 心跳客户端 · 双击打开",
            Menu = menu,
            IsVisible = true,
        };

        _tray.Clicked += (_, _) => OnTrayClicked();
        TrayIcon.SetIcons(this, new TrayIcons { _tray });

        AppLog.Info("已常驻托盘：双击图标打开窗口，右键可显示界面或退出");
    }

    /// <summary>
    /// 托盘只会上报 Clicked；Windows 上双击等于连发两次 Clicked，
    /// 所以要在间隔内收到第二次点击才认为用户想打开窗口。
    /// </summary>
    private void OnTrayClicked()
    {
        var now = DateTime.UtcNow;
        if ((now - _lastTrayClick).TotalMilliseconds <= DoubleClickMs)
        {
            _lastTrayClick = DateTime.MinValue;
            ShowWindow();
            return;
        }

        _lastTrayClick = now;
    }

    private void UpdateTrayToolTip(string status)
    {
        if (_tray is not null)
        {
            _tray.ToolTipText = $"WOL 心跳客户端 · {status}（双击打开）";
        }
    }

    public void ShowWindow()
    {
        _window?.ShowFromTray();
    }

    private void ExitApplication(IClassicDesktopStyleApplicationLifetime desktop)
    {
        _tray?.Dispose();
        _tray = null;

        _window?.RequestExit();
        _window?.Close();

        desktop.Shutdown();
    }

    // ---------- 重复启动时把已有窗口弹出来 ----------

    private void ListenShowRequests()
    {
        var signal = SingleInstance.ShowSignal();
        var listener = new Thread(() =>
        {
            while (true)
            {
                signal.WaitOne();
                Dispatcher.UIThread.Post(ShowWindow);
            }
        })
        {
            IsBackground = true,
            Name = "wol-show-window-listener",
        };
        listener.Start();
    }
}
