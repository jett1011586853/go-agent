[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

$profilePath = $PROFILE.CurrentUserCurrentHost
if (-not (Test-Path $profilePath)) {
    Write-Host "Profile not found, nothing to remove: $profilePath" -ForegroundColor Yellow
    exit 0
}

$begin = "# >>> go-agent shortcut >>>"
$end = "# <<< go-agent shortcut <<<"
$raw = Get-Content -Path $profilePath -Raw
if ($null -eq $raw) {
    $raw = ""
}

$pattern = "(?s)" + [regex]::Escape($begin) + ".*?" + [regex]::Escape($end) + "\s*"
$updated = [regex]::Replace($raw, $pattern, "").TrimEnd()
Set-Content -Path $profilePath -Value ($updated + "`r`n") -Encoding UTF8

Write-Host "Removed go-agent shortcut block from profile:" -ForegroundColor Green
Write-Host "  $profilePath" -ForegroundColor DarkGray
