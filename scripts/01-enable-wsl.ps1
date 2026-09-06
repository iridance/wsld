<#
.SYNOPSIS
  wsld Phase 1：在 Windows 11 啟用 WSL2 並確保有一個 Ubuntu（WSL2）發行版。

.DESCRIPTION
  1. 檢查 Windows 11 與系統管理員權限
  2. 啟用 Microsoft-Windows-Subsystem-Linux、VirtualMachinePlatform（不自動重開機）
  3. wsl --update、wsl --set-default-version 2
  4. 若沒有任何 Ubuntu WSL2 發行版，執行 wsl --install -d <Distro> --no-launch
  5. 若需要重開機，明確提示；重開後在 Ubuntu 內執行 scripts/02-bootstrap-docker.sh

  冪等；可重複執行。

.PARAMETER Distro
  要安裝的 Ubuntu 發行版名稱（wsl --list --online 中的 NAME），預設 Ubuntu。

.PARAMETER DryRun
  只印出將執行的動作。

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\01-enable-wsl.ps1
#>
[CmdletBinding()]
param(
    [string]$Distro = 'Ubuntu',
    [switch]$DryRun
)

Set-StrictMode -Version 2
$ErrorActionPreference = 'Stop'

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
. (Join-Path $here 'lib\WslHelpers.ps1')

function Write-Step([string]$msg) { Write-Host "[wsld] $msg" }
function Write-Warn([string]$msg) { Write-Host "[wsld] WARN: $msg" -ForegroundColor Yellow }
function Fail([string]$msg, [int]$code = 1) { Write-Host "[wsld] ERROR: $msg" -ForegroundColor Red; exit $code }

function Invoke-Step([string]$desc, [scriptblock]$action) {
    if ($DryRun) { Write-Host "[dry-run] $desc"; return $null }
    Write-Step $desc
    return & $action
}

# ---------------------------------------------------------------- 0. 前置檢查
$build = [int](Get-CimInstance Win32_OperatingSystem).BuildNumber
if (-not (Test-WsldIsWindows11 -Build $build)) {
    Fail "需要 Windows 11（目前 build $build）" 3
}

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin -and -not $DryRun) {
    Fail '請以「系統管理員身分」執行 PowerShell 後再跑本腳本' 3
}

# ---------------------------------------------------------------- 1. Windows 功能
$features = @('Microsoft-Windows-Subsystem-Linux', 'VirtualMachinePlatform')
$featureResults = @()
foreach ($f in $features) {
    if (-not $isAdmin) {
        # 只有 DryRun 會走到這裡（非管理員早已 Fail）
        Write-Host "[dry-run] (需提權) 查詢/啟用 Windows 功能 $f"
        continue
    }
    $state = (Get-WindowsOptionalFeature -Online -FeatureName $f).State
    if ($state -eq 'Enabled') {
        Write-Step "$f 已啟用"
        continue
    }
    $r = Invoke-Step "啟用 Windows 功能 $f" {
        Enable-WindowsOptionalFeature -Online -FeatureName $f -NoRestart -All
    }
    if ($r) { $featureResults += $r }
}
$needReboot = Get-WsldRebootRequired -Results $featureResults

# ---------------------------------------------------------------- 2. WSL 核心與預設版本
if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
    Fail 'wsl.exe 不存在；請先重開機讓 Windows 功能生效，再重跑本腳本' 3
}

Invoke-Step 'wsl --update' { & wsl.exe --update | Out-Host } | Out-Null
Invoke-Step 'wsl --set-default-version 2' { & wsl.exe --set-default-version 2 | Out-Host } | Out-Null

# ---------------------------------------------------------------- 3. Ubuntu 發行版
$lines = @()
try { $lines = @(& wsl.exe -l -v 2>$null) } catch { $lines = @() }
$distros = @(ConvertFrom-WslList -Lines $lines)
$ubuntu = Select-WsldUbuntuDistro -Distros $distros

if ($ubuntu) {
    Write-Step "已有 Ubuntu WSL2 發行版：$($ubuntu.Name)（$($ubuntu.State)）"
} else {
    Invoke-Step "安裝發行版 $Distro（wsl --install -d $Distro --no-launch）" {
        & wsl.exe --install -d $Distro --no-launch | Out-Host
    } | Out-Null
    Write-Warn "第一次啟動 $Distro 時會要求建立 Linux 使用者；請在「開始」選單開啟 $Distro 完成設定。"
}

# ---------------------------------------------------------------- 4. 收尾
if ($needReboot) {
    Write-Warn '已啟用 Windows 功能，需要重新開機後才能使用 WSL2。'
    Write-Host ''
    Write-Host '  重開機後：'
} else {
    Write-Host ''
    Write-Host '  下一步：'
}
Write-Host "    1. 開啟 Ubuntu（WSL）終端"
Write-Host "    2. 執行 repo 內的 scripts/02-bootstrap-docker.sh"
Write-Host ''
if ($needReboot) { exit 10 }
exit 0
