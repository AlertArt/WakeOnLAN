<#
.SYNOPSIS
    向 WakeOnLAN 服务端上报心跳，让服务端知道这台 Windows 机器在线。

.DESCRIPTION
    服务端不使用 ping 判断在线（Windows 默认丢弃入站 ICMP），而是等这台机器
    主动打电话回家：本脚本每 60 秒 POST 一次 /api/heartbeat，带上本机 MAC 与
    设备密钥。服务端 3 分钟收不到心跳即判离线。

    不需要在防火墙上开任何入站端口，只需要能访问服务端的 8080。

.PARAMETER Server
    服务端地址，例如 http://192.168.1.10:8080。省略协议时按 http 处理。

.PARAMETER Secret
    设备的心跳密钥，必须与网页里该设备「心跳密钥」一栏完全一致（16–64 位）。

.PARAMETER Mac
    本机 MAC。省略时自动从已启用的物理网卡读取，一般不用填。

.PARAMETER IntervalSeconds
    上报间隔，默认 60 秒。服务端在响应的 interval_seconds 里也会给出建议值。

.PARAMETER LogPath
    日志文件路径，默认 %ProgramData%\wol-heartbeat\heartbeat.log。

.PARAMETER Once
    只上报一次就退出。用于首次配置时验证密钥是否正确。

.EXAMPLE
    .\heartbeat.ps1 -Server http://192.168.1.10:8080 -Secret hb-7f3c9a2d5e8b1f4c -Once

.EXAMPLE
    .\heartbeat.ps1 -Server http://192.168.1.10:8080 -Secret hb-7f3c9a2d5e8b1f4c
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Server,
    [Parameter(Mandatory = $true)][string]$Secret,
    [string]$Mac = '',
    [int]$IntervalSeconds = 60,
    [string]$LogPath = '',
    [switch]$Once
)

$ErrorActionPreference = 'Stop'

# 日志默认放 ProgramData：SYSTEM 账号与普通用户都有写权限。
if ([string]::IsNullOrWhiteSpace($LogPath)) {
    $base = $env:ProgramData
    if ([string]::IsNullOrWhiteSpace($base)) { $base = $PSScriptRoot }
    $LogPath = Join-Path $base 'wol-heartbeat\heartbeat.log'
}

$logDir = Split-Path -Parent $LogPath
if (-not (Test-Path -LiteralPath $logDir)) {
    New-Item -ItemType Directory -Path $logDir -Force | Out-Null
}

function Write-Log {
    param([string]$Text)
    $line = '{0} {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Text
    try {
        # 单文件超过 1 MB 就轮转一份，避免长期运行把磁盘写满。
        if ((Test-Path -LiteralPath $LogPath) -and ((Get-Item -LiteralPath $LogPath).Length -gt 1MB)) {
            Move-Item -LiteralPath $LogPath -Destination ($LogPath + '.1') -Force
        }
        Add-Content -LiteralPath $LogPath -Value $line -Encoding UTF8
    } catch {
        # 日志写不了不能影响上报。
    }
    Write-Host $line
}

function Get-LocalMac {
    # 优先用 Get-NetAdapter -Physical：它会滤掉 Hyper-V / VMware 的虚拟网卡，
    # 避免把 vEthernet 的 MAC 当成"本机 MAC"上报（那样永远对不上设备记录）。
    try {
        $physical = Get-NetAdapter -Physical -ErrorAction Stop |
            Where-Object { $_.Status -eq 'Up' -and -not [string]::IsNullOrWhiteSpace($_.MacAddress) } |
            Sort-Object ifIndex
        foreach ($a in $physical) { return $a.MacAddress }
    } catch {
        Write-Log ('Get-NetAdapter 不可用，改用 CIM 查询：' + $_.Exception.Message)
    }

    try {
        $adapters = Get-CimInstance -ClassName Win32_NetworkAdapter -Filter 'PhysicalAdapter = TRUE AND NetEnabled = TRUE'
        foreach ($a in $adapters) {
            if (-not [string]::IsNullOrWhiteSpace($a.MACAddress)) { return $a.MACAddress }
        }
    } catch {
        Write-Log ('读取网卡信息失败：' + $_.Exception.Message)
    }
    return ''
}

function Normalize-Mac {
    param([string]$Value)
    $hex = ($Value -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
    if ($hex.Length -ne 12) { return '' }
    $parts = @()
    for ($i = 0; $i -lt 12; $i += 2) { $parts += $hex.Substring($i, 2) }
    return ($parts -join ':')
}

# ---- 参数自检 -------------------------------------------------------------
if ($Server -notmatch '^https?://') { $Server = 'http://' + $Server }
$endpoint = $Server.TrimEnd('/') + '/api/heartbeat'

if ($Secret.Length -lt 16) {
    Write-Host '心跳密钥至少 16 位，请与网页中该设备的「心跳密钥」保持一致。' -ForegroundColor Red
    exit 1
}
if ($IntervalSeconds -lt 10) { $IntervalSeconds = 10 }

if ([string]::IsNullOrWhiteSpace($Mac)) {
    $Mac = Get-LocalMac
    if ([string]::IsNullOrWhiteSpace($Mac)) {
        Write-Host '无法自动识别本机 MAC，请用 -Mac AA:BB:CC:DD:EE:FF 指定。' -ForegroundColor Red
        exit 1
    }
}
$Mac = Normalize-Mac $Mac
if ([string]::IsNullOrWhiteSpace($Mac)) {
    Write-Host 'MAC 格式不正确，应为 AA:BB:CC:DD:EE:FF。' -ForegroundColor Red
    exit 1
}

Write-Log ('启动心跳：server={0} mac={1} interval={2}s' -f $endpoint, $Mac, $IntervalSeconds)

function Send-Heartbeat {
    $payload = @{ mac = $Mac; secret = $Secret } | ConvertTo-Json -Compress
    $body = [System.Text.Encoding]::UTF8.GetBytes($payload)
    try {
        $resp = Invoke-RestMethod -Uri $endpoint -Method Post -Body $body `
            -ContentType 'application/json; charset=utf-8' -TimeoutSec 15
        Write-Log ('心跳成功：status={0}，服务端建议间隔 {1} 秒' -f $resp.status, $resp.interval_seconds)
        return $true
    } catch {
        $msg = $_.Exception.Message
        $status = 0
        if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode }
        switch ($status) {
            401 { Write-Log '心跳被拒绝（401）：请检查网页里该设备的 MAC 与心跳密钥是否与脚本一致' ; break }
            404 { Write-Log ('心跳地址不存在（404）：请确认服务端版本支持心跳，且地址正确：' + $endpoint) ; break }
            default {
                if ($status -gt 0) { Write-Log ('心跳失败：HTTP {0} {1}' -f $status, $msg) }
                else { Write-Log ('心跳失败：{0}' -f $msg) }
            }
        }
        return $false
    }
}

# ---- 主循环 ---------------------------------------------------------------
while ($true) {
    $ok = Send-Heartbeat
    if ($Once) { if ($ok) { exit 0 } else { exit 1 } }

    # 失败时 15 秒后重试，别等满一个周期；成功则按间隔上报。
    if ($ok) { $wait = $IntervalSeconds } else { $wait = [Math]::Min(15, $IntervalSeconds) }
    Start-Sleep -Seconds $wait
}
