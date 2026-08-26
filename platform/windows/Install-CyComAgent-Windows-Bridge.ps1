[CmdletBinding()]
param(
    [ValidateRange(1024,65535)][int]$Port = 7332,
    [string]$InstallDir = (Join-Path $env:ProgramData 'CyComAgent\WindowsBridge')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this installer from an elevated PowerShell session.'
}

$source = Join-Path $PSScriptRoot 'CyComAgent-Windows-Bridge.ps1'
if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
    throw "Bridge source not found: $source"
}

New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
$dest = Join-Path $InstallDir 'CyComAgent-Windows-Bridge.ps1'
Copy-Item -LiteralPath $source -Destination $dest -Force

$token = Join-Path $InstallDir 'bridge.token'
$log = Join-Path $InstallDir 'bridge.log'
$taskName = 'CyComAgent Windows Bridge'
$existingTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($null -ne $existingTask) {
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 300
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
}
$powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$arguments = '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File "{0}" -BindAddress 127.0.0.1 -Port {1} -TokenFile "{2}" -LogFile "{3}"' -f $dest,$Port,$token,$log

$action = New-ScheduledTaskAction -Execute $powershell -Argument $arguments
$trigger = New-ScheduledTaskTrigger -AtStartup
$principalTask = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)

Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principalTask -Settings $settings -Description 'CyComAgent elevated local Windows bridge (background service-style task).' -Force | Out-Null
Start-ScheduledTask -TaskName $taskName
Start-Sleep -Milliseconds 500

Write-Host "Installed CyComAgent Windows Bridge"
Write-Host "MCP endpoint: http://127.0.0.1:$Port/mcp"
Write-Host "Token file:   $token"
Write-Host "Task:         $taskName"
