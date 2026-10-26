using System;
using System.Linq;
using System.Net.NetworkInformation;
using System.Net.Sockets;
using System.Text;

namespace WolHeartbeat;

public static class MacUtil
{
    /// <summary>归一化为 AA:BB:CC:DD:EE:FF；格式非法返回 null。</summary>
    public static string? Normalize(string? raw)
    {
        if (raw is null)
        {
            return null;
        }

        var digits = new StringBuilder(12);
        foreach (var ch in raw)
        {
            if (ch is ':' or '-' or '.' or ' ')
            {
                continue;
            }

            if (!Uri.IsHexDigit(ch))
            {
                return null;
            }

            digits.Append(ch);
        }

        if (digits.Length != 12)
        {
            return null;
        }

        var hex = digits.ToString().ToUpperInvariant();
        var parts = Enumerable.Range(0, 6).Select(i => hex.Substring(i * 2, 2));
        return string.Join(':', parts);
    }

    /// <summary>
    /// 读取本机物理网卡 MAC：优先有默认网关的已启用网卡，
    /// 跳过回环、隧道、虚拟网卡（Hyper-V / VirtualBox / WSL 之类）。
    /// </summary>
    public static string? DetectLocal()
    {
        try
        {
            var candidates = NetworkInterface.GetAllNetworkInterfaces()
                .Where(n => n.OperationalStatus == OperationalStatus.Up)
                .Where(n => n.NetworkInterfaceType is NetworkInterfaceType.Ethernet
                    or NetworkInterfaceType.GigabitEthernet
                    or NetworkInterfaceType.Wireless80211
                    or NetworkInterfaceType.FastEthernetT)
                .Where(n => n.GetPhysicalAddress().GetAddressBytes().Length == 6)
                .Where(n => !LooksVirtual(n.Name + " " + n.Description))
                .Select(n => new
                {
                    Mac = Normalize(n.GetPhysicalAddress().ToString()),
                    HasGateway = HasGateway(n),
                })
                .Where(x => x.Mac is not null)
                .OrderByDescending(x => x.HasGateway)
                .ToList();

            return candidates.FirstOrDefault()?.Mac;
        }
        catch
        {
            return null;
        }
    }

    private static bool HasGateway(NetworkInterface nic)
    {
        try
        {
            return nic.GetIPProperties().GatewayAddresses
                .Any(g => g.Address is { AddressFamily: AddressFamily.InterNetwork } addr
                          && !addr.ToString().StartsWith("0.", StringComparison.Ordinal));
        }
        catch
        {
            return false;
        }
    }

    private static bool LooksVirtual(string text)
    {
        var lowered = text.ToLowerInvariant();
        string[] markers =
        [
            "hyper-v", "vethernet", "virtualbox", "vmware", "loopback",
            "tap-windows", "wintun", "wireguard", "npcap", "bluetooth",
            "docker", "wsl", "teredo", "isatap",
        ];
        return markers.Any(lowered.Contains);
    }
}
