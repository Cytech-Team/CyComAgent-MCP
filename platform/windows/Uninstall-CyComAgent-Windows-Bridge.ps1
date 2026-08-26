[CmdletBinding(SupportsShouldProcess)]
param(
    [switch]$RemoveData,
    [string]$InstallDir = (Join-Path $env:ProgramData 'CyComAgent\WindowsBridge')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$taskName = 'CyComAgent Windows Bridge'

$task = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($null -ne $task) {
    if ($PSCmdlet.ShouldProcess($taskName,'Stop and unregister scheduled task')) {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
    }
}

if ($RemoveData -and (Test-Path -LiteralPath $InstallDir)) {
    if ($PSCmdlet.ShouldProcess($InstallDir,'Remove bridge files, token and log')) {
        Remove-Item -LiteralPath $InstallDir -Recurse -Force
    }
}
