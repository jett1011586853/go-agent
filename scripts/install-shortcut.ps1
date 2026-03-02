[CmdletBinding()]
param(
    [switch]$EnableGoShim
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$startScript = Join-Path $repoRoot "start-agent.ps1"
if (-not (Test-Path $startScript)) {
    throw "Cannot find start-agent.ps1 at: $startScript"
}

$profilePath = $PROFILE.CurrentUserCurrentHost
$profileDir = Split-Path -Parent $profilePath
if (-not (Test-Path $profileDir)) {
    New-Item -ItemType Directory -Path $profileDir -Force | Out-Null
}
if (-not (Test-Path $profilePath)) {
    New-Item -ItemType File -Path $profilePath -Force | Out-Null
}

$begin = "# >>> go-agent shortcut >>>"
$end = "# <<< go-agent shortcut <<<"
$raw = Get-Content -Path $profilePath -Raw
if ($null -eq $raw) {
    $raw = ""
}
$pattern = "(?s)" + [regex]::Escape($begin) + ".*?" + [regex]::Escape($end) + "\s*"
$clean = [regex]::Replace($raw, $pattern, "").TrimEnd()

$startEscaped = $startScript.Replace("'", "''")
$block = @"
$begin
function Start-GoAgent {
    [CmdletBinding()]
    param(
        [Parameter(ValueFromRemainingArguments = `$true)]
        [string[]]`$Args
    )
    powershell -ExecutionPolicy Bypass -File '$startEscaped' @Args
}
Set-Alias -Name goagent -Value Start-GoAgent -Scope Global
Set-Alias -Name gga -Value Start-GoAgent -Scope Global
"@

if ($EnableGoShim) {
    $block += @"
if (-not (Get-Variable -Name __go_agent_real_go -Scope Global -ErrorAction SilentlyContinue)) {
    `$global:__go_agent_real_go = (Get-Command go -CommandType Application | Select-Object -First 1).Source
}
function go {
    [CmdletBinding()]
    param(
        [Parameter(ValueFromRemainingArguments = `$true)]
        [string[]]`$Args
    )
    if (`$Args.Count -ge 2 -and `$Args[0].ToLower() -eq 'go' -and `$Args[1].ToLower() -eq 'agent') {
        if (`$Args.Count -gt 2) {
            Start-GoAgent @(`$Args[2..(`$Args.Count - 1)])
        } else {
            Start-GoAgent
        }
        return
    }
    if ([string]::IsNullOrWhiteSpace(`$global:__go_agent_real_go)) {
        throw 'Cannot find real go executable.'
    }
    & `$global:__go_agent_real_go @Args
}
"@
}

$block += "`r`n$end`r`n"

if ([string]::IsNullOrWhiteSpace($clean)) {
    $updated = $block
} else {
    $updated = $clean + "`r`n`r`n" + $block
}

Set-Content -Path $profilePath -Value $updated -Encoding UTF8

Write-Host "Installed go-agent shortcuts into PowerShell profile:" -ForegroundColor Green
Write-Host "  $profilePath" -ForegroundColor DarkGray
Write-Host ""
Write-Host "Open a new PowerShell and run:" -ForegroundColor Cyan
Write-Host "  goagent" -ForegroundColor Yellow
Write-Host "  gga" -ForegroundColor Yellow
if ($EnableGoShim) {
    Write-Host "  go go agent" -ForegroundColor Yellow
}
