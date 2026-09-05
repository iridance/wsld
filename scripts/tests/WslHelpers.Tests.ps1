# Pester 3.x 相容測試：scripts/lib/WslHelpers.ps1
# 執行（Windows PowerShell）：Invoke-Pester scripts/tests

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
. (Join-Path $here '..\lib\WslHelpers.ps1')

Describe 'ConvertFrom-WslList' {
    It 'parses wsl -l -v output with default marker' {
        $raw = @(
            '  NAME            STATE           VERSION',
            '* Ubuntu-26.04    Running         2',
            '  docker-desktop  Stopped         2',
            '  Legacy          Stopped         1'
        )
        $r = @(ConvertFrom-WslList -Lines $raw)
        $r.Count | Should Be 3
        $r[0].Name | Should Be 'Ubuntu-26.04'
        $r[0].State | Should Be 'Running'
        $r[0].Version | Should Be 2
        $r[0].IsDefault | Should Be $true
        $r[1].IsDefault | Should Be $false
        $r[2].Version | Should Be 1
    }

    It 'strips NUL characters produced by wsl.exe UTF-16 output' {
        $raw = @("  N`0A`0M`0E`0   S`0T`0A`0T`0E`0   V`0E`0R`0S`0I`0O`0N`0", "* U`0b`0u`0n`0t`0u`0   R`0u`0n`0n`0i`0n`0g`0   2`0")
        $r = @(ConvertFrom-WslList -Lines $raw)
        $r.Count | Should Be 1
        $r[0].Name | Should Be 'Ubuntu'
    }

    It 'returns empty array when no distros' {
        $r = @(ConvertFrom-WslList -Lines @('  NAME  STATE  VERSION'))
        $r.Count | Should Be 0
    }
}

Describe 'Select-WsldUbuntuDistro' {
    It 'prefers the default distro when it is Ubuntu' {
        $d = @(
            [pscustomobject]@{ Name='Ubuntu-22.04'; State='Stopped'; Version=2; IsDefault=$false },
            [pscustomobject]@{ Name='Ubuntu-26.04'; State='Running'; Version=2; IsDefault=$true }
        )
        (Select-WsldUbuntuDistro -Distros $d).Name | Should Be 'Ubuntu-26.04'
    }

    It 'falls back to first Ubuntu when default is not Ubuntu' {
        $d = @(
            [pscustomobject]@{ Name='Debian'; State='Running'; Version=2; IsDefault=$true },
            [pscustomobject]@{ Name='Ubuntu-24.04'; State='Stopped'; Version=2; IsDefault=$false }
        )
        (Select-WsldUbuntuDistro -Distros $d).Name | Should Be 'Ubuntu-24.04'
    }

    It 'ignores WSL1 distros' {
        $d = @([pscustomobject]@{ Name='Ubuntu'; State='Stopped'; Version=1; IsDefault=$true })
        Select-WsldUbuntuDistro -Distros $d | Should Be $null
    }

    It 'returns null when no Ubuntu present' {
        $d = @([pscustomobject]@{ Name='docker-desktop'; State='Running'; Version=2; IsDefault=$true })
        Select-WsldUbuntuDistro -Distros $d | Should Be $null
    }
}

Describe 'Test-WsldIsWindows11' {
    It 'accepts build 22000 and above' {
        Test-WsldIsWindows11 -Build 22000 | Should Be $true
        Test-WsldIsWindows11 -Build 26100 | Should Be $true
    }
    It 'rejects Windows 10 builds' {
        Test-WsldIsWindows11 -Build 19045 | Should Be $false
    }
}

Describe 'Get-WsldRebootRequired' {
    It 'is true when any feature reports RestartNeeded' {
        $r = @(
            [pscustomobject]@{ FeatureName='VirtualMachinePlatform'; RestartNeeded=$true },
            [pscustomobject]@{ FeatureName='Microsoft-Windows-Subsystem-Linux'; RestartNeeded=$false }
        )
        Get-WsldRebootRequired -Results $r | Should Be $true
    }
    It 'is false when nothing needs restart' {
        $r = @([pscustomobject]@{ FeatureName='X'; RestartNeeded=$false })
        Get-WsldRebootRequired -Results $r | Should Be $false
    }
    It 'is false for empty input' {
        Get-WsldRebootRequired -Results @() | Should Be $false
    }
}
