# wsld：01-enable-wsl.ps1 使用的純函式（可被 Pester 測試，無副作用）。
# 相容 Windows PowerShell 5.1。

function ConvertFrom-WslList {
    <#
    .SYNOPSIS 把 `wsl -l -v` 的文字輸出解析成物件（Name/State/Version/IsDefault）。
    #>
    param([Parameter(Mandatory = $true)][AllowEmptyCollection()][string[]]$Lines)

    $result = @()
    foreach ($raw in $Lines) {
        # wsl.exe 以 UTF-16 輸出，被當 UTF-8 讀時會夾雜 NUL
        $line = ($raw -replace "`0", '').TrimEnd()
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        if ($line -match '^\s*\*?\s*NAME\s+STATE\s+VERSION') { continue }

        $isDefault = $line.TrimStart().StartsWith('*')
        $body = $line.TrimStart().TrimStart('*').Trim()
        $parts = $body -split '\s+'
        if ($parts.Count -lt 3) { continue }

        $result += [pscustomobject]@{
            Name      = $parts[0]
            State     = $parts[1]
            Version   = [int]$parts[2]
            IsDefault = $isDefault
        }
    }
    return $result
}

function Select-WsldUbuntuDistro {
    <#
    .SYNOPSIS 從發行版清單挑出要用的 Ubuntu（WSL2）：預設者優先，否則第一個 Ubuntu*。
    #>
    param([Parameter(Mandatory = $true)][AllowEmptyCollection()][object[]]$Distros)

    $ubuntu = @($Distros | Where-Object { $_.Name -like 'Ubuntu*' -and $_.Version -eq 2 })
    if ($ubuntu.Count -eq 0) { return $null }
    $def = $ubuntu | Where-Object { $_.IsDefault } | Select-Object -First 1
    if ($def) { return $def }
    return $ubuntu[0]
}

function Test-WsldIsWindows11 {
    param([Parameter(Mandatory = $true)][int]$Build)
    return ($Build -ge 22000)
}

function Get-WsldRebootRequired {
    param([AllowEmptyCollection()][object[]]$Results = @())
    foreach ($r in $Results) {
        if ($r.RestartNeeded) { return $true }
    }
    return $false
}
