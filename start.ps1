[CmdletBinding()]
param(
    [string]$Config = "opencode/agent.yaml",
    [string]$Task = ""
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

function Import-DotEnv {
    param([string]$Path)
    if (-not (Test-Path $Path)) { return }
    foreach ($rawLine in Get-Content $Path) {
        $line = $rawLine.Trim()
        if ($line.Length -eq 0 -or $line.StartsWith("#")) { continue }
        $idx = $line.IndexOf("=")
        if ($idx -lt 1) { continue }
        $name = $line.Substring(0, $idx).Trim()
        $value = $line.Substring($idx + 1).Trim()
        if (
            ($value.StartsWith('"') -and $value.EndsWith('"')) -or
            ($value.StartsWith("'") -and $value.EndsWith("'"))
        ) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        [Environment]::SetEnvironmentVariable($name, $value, "Process")
    }
}

if (-not (Test-Path ".env")) {
    if (Test-Path ".env.example") {
        Copy-Item ".env.example" ".env"
        Write-Host "Created .env from .env.example"
    }
}
Import-DotEnv -Path ".env"

if ([string]::IsNullOrWhiteSpace($env:NVIDIA_API_KEY) -or $env:NVIDIA_API_KEY -eq "replace_me") {
    throw "NVIDIA_API_KEY is missing. Set it in .env first."
}

# Force CLI mode only.
[Environment]::SetEnvironmentVariable("AGENT_HTTP_ENABLE", "false", "Process")
if ([string]::IsNullOrWhiteSpace($env:DEFAULT_AGENT)) {
    [Environment]::SetEnvironmentVariable("DEFAULT_AGENT", "build", "Process")
}

$runDir = Join-Path $PSScriptRoot ".run"
if (-not (Test-Path $runDir)) {
    New-Item -ItemType Directory -Path $runDir | Out-Null
}
$exePath = Join-Path $runDir "agent-cli.exe"

Write-Host ""
Write-Host "==> Building OpenCode-style go-agent" -ForegroundColor Cyan
Write-Host "Command: go build -o $exePath ./cmd/agent"
go build -o $exePath ./cmd/agent
if ($LASTEXITCODE -ne 0) {
    throw "go build failed with exit code $LASTEXITCODE"
}

$runArgs = @("--config", $Config)
if (-not [string]::IsNullOrWhiteSpace($Task)) {
    $runArgs += "--task"
    $runArgs += $Task
}

Write-Host ""
Write-Host "==> Starting OpenCode-style go-agent" -ForegroundColor Cyan
Write-Host "Command: $exePath $($runArgs -join ' ')"
Write-Host ""
& $exePath @runArgs
