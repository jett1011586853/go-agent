param(
    [string]$Config = "opencode/agent.yaml",
    [string]$Task = ""
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

if ([string]::IsNullOrWhiteSpace($Task)) {
    powershell -ExecutionPolicy Bypass -File .\start.ps1 -Config $Config
} else {
    powershell -ExecutionPolicy Bypass -File .\start.ps1 -Config $Config -Task $Task
}

