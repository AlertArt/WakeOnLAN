using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net.NetworkInformation;
using System.Net.Sockets;
using System.Runtime.InteropServices;

namespace WolHeartbeat;

/// <summary>
/// F11 轻量指标采集：CPU / 内存 / 磁盘 / 网络速率 / IP / 开机时长 / 主机名 / 系统。
/// 设计原则：任何一项采不到都只影响自己（返回 null 字段），绝不抛出拖垮心跳。
/// </summary>
public static class MetricsCollector
{
    private static PerformanceCounter? _cpu;
    private static List<PerformanceCounter>? _netSent;
    private static List<PerformanceCounter>? _netRecv;
    private static bool _cpuPrimed;

    public static object? Collect()
    {
        try
        {
            var cpu = CpuPercent();
            var (memUsed, memTotal) = MemoryMb();
            var (diskUsed, diskTotal) = DiskGb();
            var (netUp, netDown) = NetBytesPerSec();

            var sample = new
            {
                cpu = Round(cpu),
                mem_used = Round(memUsed),
                mem_total = Round(memTotal),
                disk_used = Round(diskUsed),
                disk_total = Round(diskTotal),
                net_up = Round(netUp),
                net_down = Round(netDown),
                ip = LocalIPv4(),
                uptime_s = Environment.TickCount64 / 1000,
                hostname = Environment.MachineName,
                os = OsName(),
            };

            // 全零视为采集失败，心跳就不带 metrics 字段了。
            if (sample.cpu <= 0 && sample.mem_total <= 0 && sample.disk_total <= 0 &&
                sample.net_up <= 0 && sample.net_down <= 0 && string.IsNullOrEmpty(sample.ip))
            {
                return null;
            }
            return sample;
        }
        catch (Exception ex)
        {
            AppLog.Warn($"指标采集失败（不影响心跳）：{ex.Message}");
            return null;
        }
    }

    // ---------- 各项采集，单独兜底 ----------

    private static double CpuPercent()
    {
        try
        {
            _cpu ??= new PerformanceCounter("Processor", "% Processor Time", "_Total");
            var v = _cpu.NextValue();
            if (!_cpuPrimed)
            {
                // 计数器第一次取样恒为 0：先空采一次，本轮直接用第二次的值。
                _cpuPrimed = true;
                System.Threading.Thread.Sleep(300);
                v = _cpu.NextValue();
            }
            return v;
        }
        catch (Exception ex)
        {
            AppLog.Warn($"CPU 指标不可用：{ex.Message}");
            return 0;
        }
    }

    private static (double Used, double Total) MemoryMb()
    {
        try
        {
            var st = new MEMORYSTATUSEX
            {
                dwLength = (uint)Marshal.SizeOf<MEMORYSTATUSEX>(),
            };
            if (GlobalMemoryStatusEx(ref st))
            {
                return (st.ullTotalPhys > 0
                    ? (st.ullTotalPhys - st.ullAvailPhys) / 1024.0 / 1024.0
                    : 0,
                    st.ullTotalPhys / 1024.0 / 1024.0);
            }
        }
        catch (Exception ex)
        {
            AppLog.Warn($"内存指标不可用：{ex.Message}");
        }
        return (0, 0);
    }

    private static (double Used, double Total) DiskGb()
    {
        try
        {
            double total = 0, free = 0;
            foreach (var d in DriveInfo.GetDrives())
            {
                if (d.DriveType != DriveType.Fixed || !d.IsReady)
                {
                    continue;
                }

                total += d.TotalSize;
                free += d.AvailableFreeSpace;
            }

            return total > 0 ? ((total - free) / 1e9, total / 1e9) : (0, 0);
        }
        catch (Exception ex)
        {
            AppLog.Warn($"磁盘指标不可用：{ex.Message}");
            return (0, 0);
        }
    }

    private static (double Up, double Down) NetBytesPerSec()
    {
        try
        {
            _netSent ??= BuildNetCounters("Bytes Sent/sec");
            _netRecv ??= BuildNetCounters("Bytes Received/sec");

            double up = 0, down = 0;
            foreach (var c in _netSent)
            {
                up += SafeNext(c);
            }

            foreach (var c in _netRecv)
            {
                down += SafeNext(c);
            }

            return (up, down);
        }
        catch (Exception ex)
        {
            AppLog.Warn($"网络指标不可用：{ex.Message}");
            return (0, 0);
        }
    }

    private static List<PerformanceCounter> BuildNetCounters(string counterName)
    {
        var list = new List<PerformanceCounter>();
        var category = new PerformanceCounterCategory("Network Interface");
        foreach (var name in category.GetInstanceNames())
        {
            // 环回与隧道伪接口没有流量意义，排除掉减少噪音。
            var n = name.ToUpperInvariant();
            if (n.Contains("ISATAP") || n.Contains("TEREDO") || n.Contains("LOOPBACK"))
            {
                continue;
            }

            try
            {
                list.Add(new PerformanceCounter("Network Interface", counterName, name));
            }
            catch
            {
                // 实例在枚举与创建之间消失，跳过。
            }
        }

        return list;
    }

    private static double SafeNext(PerformanceCounter c)
    {
        try
        {
            return c.NextValue();
        }
        catch
        {
            return 0;
        }
    }

    private static string LocalIPv4()
    {
        try
        {
            var candidates = NetworkInterface.GetAllNetworkInterfaces()
                .Where(n => n.OperationalStatus == OperationalStatus.Up &&
                            n.NetworkInterfaceType != NetworkInterfaceType.Loopback &&
                            n.NetworkInterfaceType != NetworkInterfaceType.Tunnel)
                .OrderByDescending(n => n.GetIPProperties().GatewayAddresses.Count > 0)
                .ThenBy(n => n.Id);

            foreach (var nic in candidates)
            {
                foreach (var addr in nic.GetIPProperties().UnicastAddresses)
                {
                    if (addr.Address.AddressFamily == AddressFamily.InterNetwork)
                    {
                        return addr.Address.ToString();
                    }
                }
            }
        }
        catch (Exception ex)
        {
            AppLog.Warn($"IP 指标不可用：{ex.Message}");
        }

        return "";
    }

    private static string OsName()
    {
        try
        {
            var desc = System.Runtime.InteropServices.RuntimeInformation.OSDescription; // 如 "Microsoft Windows 10.0.19045"
            if (desc.StartsWith("Microsoft ", StringComparison.OrdinalIgnoreCase))
            {
                desc = desc["Microsoft ".Length..];
            }

            var v = Environment.OSVersion.Version;
            // Windows 11 的版本号仍是 10.0.x，用 Build 号区分（>=22000 为 11）。
            var name = desc.Contains("Windows") && v.Build >= 22000 ? "Windows 11" : desc;
            return name.Length > 64 ? name[..64] : name;
        }
        catch
        {
            return "";
        }
    }

    private static double Round(double v) => Math.Round(v, 1);

    // ---------- GlobalMemoryStatusEx P/Invoke ----------

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GlobalMemoryStatusEx(ref MEMORYSTATUSEX lpBuffer);

    [StructLayout(LayoutKind.Sequential)]
    private struct MEMORYSTATUSEX
    {
        public uint dwLength;
        public uint dwMemoryLoad;
        public ulong ullTotalPhys;
        public ulong ullAvailPhys;
        public ulong ullTotalPageFile;
        public ulong ullAvailPageFile;
        public ulong ullTotalVirtual;
        public ulong ullAvailVirtual;
        public ulong ullAvailExtendedVirtual;
    }
}
