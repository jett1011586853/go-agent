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

function Get-ConfigPath {
    param([string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return $null }
    if (Test-Path $Path) {
        return (Resolve-Path $Path).Path
    }
    $joined = Join-Path $PSScriptRoot $Path
    if (Test-Path $joined) {
        return (Resolve-Path $joined).Path
    }
    return $null
}

function Get-YamlScalarValue {
    param(
        [string]$ConfigPath,
        [string]$Key
    )
    if ([string]::IsNullOrWhiteSpace($ConfigPath) -or -not (Test-Path $ConfigPath)) {
        return $null
    }
    $pattern = '^\s*' + [regex]::Escape($Key) + '\s*:\s*(.+?)\s*$'
    foreach ($line in Get-Content $ConfigPath) {
        $trim = $line.Trim()
        if ($trim.StartsWith("#")) { continue }
        if ($line -match $pattern) {
            $val = $Matches[1].Trim()
            if ($val.StartsWith('"') -and $val.EndsWith('"')) {
                $val = $val.Substring(1, $val.Length - 2)
            } elseif ($val.StartsWith("'") -and $val.EndsWith("'")) {
                $val = $val.Substring(1, $val.Length - 2)
            }
            return $val
        }
    }
    return $null
}

function Parse-Bool {
    param(
        [string]$Value,
        [bool]$Default
    )
    if ([string]::IsNullOrWhiteSpace($Value)) {
        return $Default
    }
    switch ($Value.Trim().ToLower()) {
        "1" { return $true }
        "true" { return $true }
        "yes" { return $true }
        "on" { return $true }
        "0" { return $false }
        "false" { return $false }
        "no" { return $false }
        "off" { return $false }
        default { return $Default }
    }
}

function Resolve-BoolSetting {
    param(
        [string]$EnvName,
        [string]$ConfigPath,
        [string]$ConfigKey,
        [bool]$Default
    )
    $envVal = [Environment]::GetEnvironmentVariable($EnvName, "Process")
    if (-not [string]::IsNullOrWhiteSpace($envVal)) {
        return Parse-Bool -Value $envVal -Default $Default
    }
    $cfgVal = Get-YamlScalarValue -ConfigPath $ConfigPath -Key $ConfigKey
    return Parse-Bool -Value $cfgVal -Default $Default
}

function Resolve-StringSetting {
    param(
        [string]$EnvName,
        [string]$ConfigPath,
        [string]$ConfigKey,
        [string]$Default
    )
    $envVal = [Environment]::GetEnvironmentVariable($EnvName, "Process")
    if (-not [string]::IsNullOrWhiteSpace($envVal)) {
        return $envVal.Trim()
    }
    $cfgVal = Get-YamlScalarValue -ConfigPath $ConfigPath -Key $ConfigKey
    if (-not [string]::IsNullOrWhiteSpace($cfgVal)) {
        return $cfgVal.Trim()
    }
    return $Default
}

function Resolve-IntSetting {
    param(
        [string]$EnvName,
        [string]$ConfigPath,
        [string]$ConfigKey,
        [int]$Default
    )
    $envVal = [Environment]::GetEnvironmentVariable($EnvName, "Process")
    if (-not [string]::IsNullOrWhiteSpace($envVal)) {
        $n = 0
        if ([int]::TryParse($envVal.Trim(), [ref]$n) -and $n -gt 0) {
            return $n
        }
    }
    $cfgVal = Get-YamlScalarValue -ConfigPath $ConfigPath -Key $ConfigKey
    if (-not [string]::IsNullOrWhiteSpace($cfgVal)) {
        $n = 0
        if ([int]::TryParse($cfgVal.Trim(), [ref]$n) -and $n -gt 0) {
            return $n
        }
    }
    return $Default
}

function Test-LocalBaseURL {
    param([string]$Url)
    try {
        $u = [uri]$Url
        $uriHost = $u.Host.Trim().ToLower()
        return ($uriHost -eq "localhost" -or $uriHost -eq "127.0.0.1" -or $uriHost -eq "::1")
    } catch {
        return $false
    }
}

function Get-PortFromURL {
    param(
        [string]$Url,
        [int]$Default
    )
    try {
        $u = [uri]$Url
        if ($Url -match '^[a-zA-Z]+://[^/]+:[0-9]+') {
            if ($u.Port -gt 0) {
                return $u.Port
            }
        }
    } catch {
    }
    return $Default
}

function Test-DockerAvailable {
    & docker version --format '{{.Server.Version}}' *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "Docker daemon is not available. Start Docker Desktop first."
    }
}

function Test-DockerContainerExists {
    param([string]$Name)
    $items = & docker ps -a --filter "name=^/$Name$" --format '{{.Names}}' 2>$null
    if ($LASTEXITCODE -ne 0) { return $false }
    foreach ($item in $items) {
        if ($item -eq $Name) { return $true }
    }
    return $false
}

function Test-DockerContainerRunning {
    param([string]$Name)
    $items = & docker ps --filter "name=^/$Name$" --format '{{.Names}}' 2>$null
    if ($LASTEXITCODE -ne 0) { return $false }
    foreach ($item in $items) {
        if ($item -eq $Name) { return $true }
    }
    return $false
}

function Get-DockerContainerImage {
    param([string]$Name)
    $img = & docker inspect --format '{{.Config.Image}}' $Name 2>$null
    if ($LASTEXITCODE -ne 0) {
        return $null
    }
    if ($null -eq $img) { return $null }
    return ($img | Select-Object -First 1).ToString().Trim()
}

function Ensure-DockerImage {
    param([string]$Image)
    & docker image inspect $Image *> $null
    if ($LASTEXITCODE -eq 0) {
        return
    }
    Write-Host "Pulling NIM image: $Image" -ForegroundColor Cyan
    & docker pull $Image
    if ($LASTEXITCODE -ne 0) {
        throw "docker pull failed for image: $Image"
    }
}

function Get-DockerLogsTail {
    param(
        [string]$Name,
        [int]$Lines = 80
    )
    $cmd = "docker logs --tail $Lines ""$Name"" 2>&1"
    return (cmd /c $cmd)
}

function Start-NimContainer {
    param(
        [string]$Name,
        [string]$Image,
        [int]$HostPort,
        [string]$CacheDir,
        [string]$NgcApiKey,
        [hashtable]$ExtraEnv = @{}
    )
    $cachePath = [System.IO.Path]::GetFullPath($CacheDir)
    if (-not (Test-Path $cachePath)) {
        New-Item -ItemType Directory -Path $cachePath -Force | Out-Null
    }

    $exists = Test-DockerContainerExists -Name $Name
    if ($exists) {
        $currentImage = Get-DockerContainerImage -Name $Name
        if (-not [string]::IsNullOrWhiteSpace($currentImage) -and $currentImage -ne $Image) {
            Write-Host "$Name image mismatch ($currentImage != $Image), recreating container." -ForegroundColor Yellow
            & docker rm -f $Name | Out-Null
            if ($LASTEXITCODE -ne 0) {
                throw "docker rm -f failed for container: $Name"
            }
            $exists = $false
        }
    }

    if ($exists -and (Test-DockerContainerRunning -Name $Name)) {
        Write-Host "$Name is already running." -ForegroundColor DarkGray
        return
    }

    if ($exists) {
        Write-Host "Starting existing container: $Name" -ForegroundColor Cyan
        & docker start $Name | Out-Null
        if ($LASTEXITCODE -ne 0) {
            throw "docker start failed for container: $Name"
        }
        return
    }

    if ([string]::IsNullOrWhiteSpace($NgcApiKey)) {
        throw "NGC_API_KEY is required to start local NIM container: $Name"
    }

    Ensure-DockerImage -Image $Image
    Write-Host "Starting new container: $Name ($Image) on localhost:$HostPort" -ForegroundColor Cyan
    $dockerArgs = @(
        "run", "-d",
        "--name", $Name,
        "--gpus", "all",
        "--shm-size=16GB",
        "-e", "NGC_API_KEY=$NgcApiKey",
        "-v", "${cachePath}:/opt/nim/.cache",
        "-p", "${HostPort}:8000"
    )
    foreach ($k in $ExtraEnv.Keys) {
        if ([string]::IsNullOrWhiteSpace($k)) { continue }
        $v = [string]$ExtraEnv[$k]
        if ([string]::IsNullOrWhiteSpace($v)) {
            $dockerArgs += "-e"
            $dockerArgs += $k
        } else {
            $dockerArgs += "-e"
            $dockerArgs += "$k=$v"
        }
    }
    $dockerArgs += $Image
    & docker @dockerArgs | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "docker run failed for container: $Name"
    }
}

function Wait-NimReady {
    param(
        [string]$Name,
        [ScriptBlock]$Probe,
        [int]$TimeoutSeconds = 300,
        [string[]]$FatalPatterns = @(),
        [switch]$ContinueOnTimeout
    )
    Write-Host "Waiting for $Name readiness..." -ForegroundColor DarkGray
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $attempt = 0
    while ((Get-Date) -lt $deadline) {
        $attempt++
        if (& $Probe) {
            Write-Host "$Name is ready." -ForegroundColor Green
            return $true
        }
        if (($attempt % 2) -eq 0) {
            $remain = [int][Math]::Max(0, ($deadline - (Get-Date)).TotalSeconds)
            Write-Host "$Name still not ready, retrying... (${remain}s left, attempt=$attempt)" -ForegroundColor DarkGray
        }
        if (($attempt % 3) -eq 0 -and $FatalPatterns.Count -gt 0) {
            $tail = (Get-DockerLogsTail -Name $Name -Lines 120) -join "`n"
            foreach ($pattern in $FatalPatterns) {
                if ($tail -match $pattern) {
                    Write-Host ""
                    Write-Host "$Name reported fatal backend error pattern: $pattern" -ForegroundColor Red
                    Write-Host "Last docker logs:" -ForegroundColor Yellow
                    Get-DockerLogsTail -Name $Name -Lines 80 | ForEach-Object { Write-Host $_ }
                    if ($ContinueOnTimeout) {
                        return $false
                    }
                    throw "$Name encountered fatal backend errors during startup"
                }
            }
        }
        Start-Sleep -Seconds 5
    }

    Write-Host ""
    Write-Host "Timed out waiting for $Name. Last docker logs:" -ForegroundColor Yellow
    Get-DockerLogsTail -Name $Name -Lines 80 | ForEach-Object { Write-Host $_ }
    if ($ContinueOnTimeout) {
        Write-Host "Continue without $Name for this run." -ForegroundColor Yellow
        return $false
    }
    throw "$Name did not become ready in ${TimeoutSeconds}s"
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

$configPath = Get-ConfigPath -Path $Config

# Auto-start local NIM services (embedding/rerank) in detached mode before agent.
$embeddingEnabled = Resolve-BoolSetting -EnvName "EMBEDDING_ENABLED" -ConfigPath $configPath -ConfigKey "embedding_enabled" -Default $false
$rerankEnabled = Resolve-BoolSetting -EnvName "RERANK_ENABLED" -ConfigPath $configPath -ConfigKey "rerank_enabled" -Default $false

if ($embeddingEnabled -or $rerankEnabled) {
    Test-DockerAvailable
}

$ngcApiKey = [Environment]::GetEnvironmentVariable("NGC_API_KEY", "Process")
if ([string]::IsNullOrWhiteSpace($ngcApiKey)) {
    # Many users keep only NVIDIA_API_KEY in .env; reuse it as fallback.
    $ngcApiKey = [Environment]::GetEnvironmentVariable("NVIDIA_API_KEY", "Process")
}

if ($embeddingEnabled) {
    $embeddingBaseURL = Resolve-StringSetting -EnvName "EMBEDDING_BASE_URL" -ConfigPath $configPath -ConfigKey "embedding_base_url" -Default "http://localhost:8001"
    if (Test-LocalBaseURL -Url $embeddingBaseURL) {
        $embeddingModel = Resolve-StringSetting -EnvName "EMBEDDING_MODEL" -ConfigPath $configPath -ConfigKey "embedding_model" -Default "nvidia/llama-3.2-nv-embedqa-1b-v2"
        $embedPort = Resolve-IntSetting -EnvName "EMBED_NIM_PORT" -ConfigPath $null -ConfigKey $null -Default (Get-PortFromURL -Url $embeddingBaseURL -Default 8001)
        $embedImage = Resolve-StringSetting -EnvName "EMBED_NIM_IMAGE" -ConfigPath $null -ConfigKey $null -Default "nvcr.io/nim/nvidia/llama-3.2-nv-embedqa-1b-v2:1.9.0"
        $embedContainer = Resolve-StringSetting -EnvName "EMBED_NIM_CONTAINER" -ConfigPath $null -ConfigKey $null -Default "embed-nim"
        $embedCache = Resolve-StringSetting -EnvName "EMBED_NIM_CACHE" -ConfigPath $null -ConfigKey $null -Default "D:\nim-cache-embed"

        Start-NimContainer -Name $embedContainer -Image $embedImage -HostPort $embedPort -CacheDir $embedCache -NgcApiKey $ngcApiKey

        $embedProbeBody = @{
            input      = @("health check")
            model      = $embeddingModel
            input_type = "query"
            modality   = "text"
        } | ConvertTo-Json -Depth 6

        $embedProbe = {
            try {
                Invoke-RestMethod -Method Post -Uri "$embeddingBaseURL/v1/embeddings" -ContentType "application/json" -Body $embedProbeBody -TimeoutSec 8 | Out-Null
                return $true
            } catch {
                return $false
            }
        }
        $embedReady = Wait-NimReady -Name $embedContainer -Probe $embedProbe -ContinueOnTimeout
        if (-not $embedReady) {
            [Environment]::SetEnvironmentVariable("EMBEDDING_ENABLED", "false", "Process")
            [Environment]::SetEnvironmentVariable("RERANK_ENABLED", "false", "Process")
            $embeddingEnabled = $false
            $rerankEnabled = $false
            Write-Host "Embedding NIM not ready, disable embedding/rerank for this run." -ForegroundColor Yellow
        }
    } else {
        Write-Host "embedding_base_url is non-local ($embeddingBaseURL), skip local container auto-start." -ForegroundColor DarkGray
    }
}

if ($rerankEnabled) {
    $rerankBaseURL = Resolve-StringSetting -EnvName "RERANK_BASE_URL" -ConfigPath $configPath -ConfigKey "rerank_base_url" -Default "http://localhost:8002"
    if (Test-LocalBaseURL -Url $rerankBaseURL) {
        $rerankModel = Resolve-StringSetting -EnvName "RERANK_MODEL" -ConfigPath $configPath -ConfigKey "rerank_model" -Default "nvidia/llama-3.2-nv-rerankqa-1b-v2"
        $rerankPort = Resolve-IntSetting -EnvName "RERANK_NIM_PORT" -ConfigPath $null -ConfigKey $null -Default (Get-PortFromURL -Url $rerankBaseURL -Default 8002)
        $rerankImage = Resolve-StringSetting -EnvName "RERANK_NIM_IMAGE" -ConfigPath $null -ConfigKey $null -Default "nvcr.io/nim/nvidia/llama-3.2-nv-rerankqa-1b-v2:1.7.0"
        $rerankContainer = Resolve-StringSetting -EnvName "RERANK_NIM_CONTAINER" -ConfigPath $null -ConfigKey $null -Default "rerank-nim"
        $rerankCache = Resolve-StringSetting -EnvName "RERANK_NIM_CACHE" -ConfigPath $null -ConfigKey $null -Default "D:\nim-cache-rerank"
        $rerankProfile = Resolve-StringSetting -EnvName "RERANK_NIM_PROFILE" -ConfigPath $null -ConfigKey $null -Default ""
        $rerankTokenizerInstances = Resolve-StringSetting -EnvName "RERANK_NIM_TOKENIZER_INSTANCES" -ConfigPath $null -ConfigKey $null -Default "1"
        $rerankModelInstances = Resolve-StringSetting -EnvName "RERANK_NIM_MODEL_INSTANCES" -ConfigPath $null -ConfigKey $null -Default "1"
        $rerankExtraEnv = @{
            "NIM_TRITON_TOKENIZER_INSTANCE_COUNT" = $rerankTokenizerInstances
            "NIM_TRITON_MODEL_INSTANCE_COUNT" = $rerankModelInstances
        }
        if (-not [string]::IsNullOrWhiteSpace($rerankProfile)) {
            $rerankExtraEnv["NIM_MODEL_PROFILE"] = $rerankProfile
        }

        Start-NimContainer -Name $rerankContainer -Image $rerankImage -HostPort $rerankPort -CacheDir $rerankCache -NgcApiKey $ngcApiKey -ExtraEnv $rerankExtraEnv

        $rerankProbeBody = @{
            model    = $rerankModel
            query    = @{ text = "health check" }
            passages = @(
                @{ text = "alpha" },
                @{ text = "beta" }
            )
            truncate = "END"
        } | ConvertTo-Json -Depth 6

        $rerankProbe = {
            try {
                Invoke-RestMethod -Method Post -Uri "$rerankBaseURL/v1/ranking" -ContentType "application/json" -Body $rerankProbeBody -TimeoutSec 8 | Out-Null
                return $true
            } catch {
                try {
                    Invoke-RestMethod -Method Post -Uri "$rerankBaseURL/v1/reranking" -ContentType "application/json" -Body $rerankProbeBody -TimeoutSec 8 | Out-Null
                    return $true
                } catch {
                    return $false
                }
            }
        }
        $rerankFatalPatterns = @(
            "boost::interprocess::lock_exception",
            "Non-graceful termination detected",
            "Triton service unavailable"
        )
        $rerankReady = Wait-NimReady -Name $rerankContainer -Probe $rerankProbe -FatalPatterns $rerankFatalPatterns -ContinueOnTimeout
        if (-not $rerankReady) {
            Write-Host "Rerank NIM unhealthy, recreating once..." -ForegroundColor Yellow
            & docker rm -f $rerankContainer | Out-Null
            if ($LASTEXITCODE -ne 0) {
                throw "docker rm -f failed for container: $rerankContainer"
            }
            Start-NimContainer -Name $rerankContainer -Image $rerankImage -HostPort $rerankPort -CacheDir $rerankCache -NgcApiKey $ngcApiKey -ExtraEnv $rerankExtraEnv
            $rerankReady = Wait-NimReady -Name $rerankContainer -Probe $rerankProbe -FatalPatterns $rerankFatalPatterns
            if (-not $rerankReady) {
                throw "Rerank NIM failed readiness after recreate. Run: docker logs --tail 200 $rerankContainer"
            }
        }
    } else {
        Write-Host "rerank_base_url is non-local ($rerankBaseURL), skip local container auto-start." -ForegroundColor DarkGray
    }
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
try {
    & $exePath @runArgs
} catch {
    Write-Host "Executable launch failed. Falling back to: go run ./cmd/agent" -ForegroundColor Yellow
    go run ./cmd/agent @runArgs
    if ($LASTEXITCODE -ne 0) {
        throw "go run failed with exit code $LASTEXITCODE"
    }
}
