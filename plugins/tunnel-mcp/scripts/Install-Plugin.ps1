$ErrorActionPreference = "Stop"

$PluginRoot = Split-Path -Parent (Split-Path -Parent $PSCommandPath)
$TunnelClientBin = $null
$CodexHome = $null
$Attempts = New-Object System.Collections.Generic.List[string]

function Add-Attempt {
    param([string]$Message)
    $script:Attempts.Add("- $Message")
}

function Test-ExecutableFile {
    param([string]$PathValue)
    if ([string]::IsNullOrWhiteSpace($PathValue)) {
        return $false
    }
    return (Test-Path -LiteralPath $PathValue -PathType Leaf)
}

function Find-AdjacentBinary {
    param([string]$Root)
    $Current = (Resolve-Path -LiteralPath $Root).Path
    while ($true) {
        foreach ($Candidate in @(
            (Join-Path $Current "tunnel-client.exe"),
            (Join-Path $Current "tunnel-client"),
            (Join-Path (Join-Path $Current "bin") "tunnel-client.exe"),
            (Join-Path (Join-Path $Current "bin") "tunnel-client"),
            (Join-Path (Join-Path (Join-Path (Join-Path $Current "bazel-bin") "cmd") "client") "client.exe"),
            (Join-Path (Join-Path (Join-Path (Join-Path $Current "bazel-bin") "cmd") "client") "client"),
            (Join-Path (Join-Path (Join-Path (Join-Path (Join-Path (Join-Path $Current "bazel-bin") "api") "tunnel-client") "cmd") "client") "client.exe"),
            (Join-Path (Join-Path (Join-Path (Join-Path (Join-Path (Join-Path $Current "bazel-bin") "api") "tunnel-client") "cmd") "client") "client")
        )) {
            if (Test-ExecutableFile $Candidate) {
                return $Candidate
            }
        }
        $Parent = Split-Path -Parent $Current
        if ($Parent -eq $Current -or [string]::IsNullOrWhiteSpace($Parent)) {
            break
        }
        $Current = $Parent
    }
    return $null
}

function Show-Help {
@"
Usage: Install-Plugin.ps1 [--tunnel-client-bin C:\path\to\tunnel-client.exe] [--codex-home C:\path\to\codex-home]

Delegates to the selected tunnel-client binary:
  tunnel-client codex plugin install [--codex-home ...]

Binary discovery order:
  --tunnel-client-bin
  TUNNEL_CLIENT_BIN
  adjacent local build outputs
  PATH
"@ | Write-Output
}

for ($i = 0; $i -lt $args.Count; $i++) {
    switch ($args[$i]) {
        "--help" { Show-Help; exit 0 }
        "-h" { Show-Help; exit 0 }
        "--tunnel-client-bin" {
            if ($i + 1 -ge $args.Count) { Write-Error "--tunnel-client-bin requires a value" }
            $candidate = $args[$i + 1]
            if (Test-ExecutableFile $candidate) {
                $TunnelClientBin = $candidate
            } else {
                Add-Attempt "--tunnel-client-bin: $candidate was not an executable file"
            }
            $i++
        }
        "--codex-home" {
            if ($i + 1 -ge $args.Count) { Write-Error "--codex-home requires a value" }
            $CodexHome = $args[$i + 1]
            $i++
        }
        default {
            Write-Error "unsupported argument: $($args[$i])"
        }
    }
}

if (-not $TunnelClientBin) {
    Add-Attempt "--tunnel-client-bin: not provided"
}

if (-not $TunnelClientBin -and $env:TUNNEL_CLIENT_BIN) {
    if (Test-ExecutableFile $env:TUNNEL_CLIENT_BIN) {
        $TunnelClientBin = $env:TUNNEL_CLIENT_BIN
    } else {
        Add-Attempt "TUNNEL_CLIENT_BIN: $($env:TUNNEL_CLIENT_BIN) was not an executable file"
    }
} elseif (-not $TunnelClientBin) {
    Add-Attempt "TUNNEL_CLIENT_BIN: not set"
}

if (-not $TunnelClientBin) {
    $AdjacentBin = Find-AdjacentBinary $PluginRoot
    if ($AdjacentBin) {
        $TunnelClientBin = $AdjacentBin
    } else {
        Add-Attempt "adjacent build outputs: no executable tunnel-client binary found next to the plugin"
    }
}

if (-not $TunnelClientBin) {
    foreach ($Name in @("tunnel-client.exe", "tunnel-client")) {
        $Command = Get-Command $Name -ErrorAction SilentlyContinue
        if ($Command) {
            $TunnelClientBin = $Command.Source
            break
        }
    }
    if (-not $TunnelClientBin) {
        Add-Attempt "PATH: no tunnel-client executable found"
    }
}

if (-not $TunnelClientBin) {
    $AttemptLines = $Attempts -join [Environment]::NewLine
@"
error: tunnel-client was not found.

Discovery methods tried:
$AttemptLines

Next steps:
- Download a release binary from https://github.com/openai/tunnel-client/releases/latest
- Or clone and build from source from https://github.com/openai/tunnel-client:
  git clone https://github.com/openai/tunnel-client.git
  cd tunnel-client
  go build -o bin/tunnel-client ./cmd/client
  # Windows: go build -o bin/tunnel-client.exe ./cmd/client
- Then rerun this installer with --tunnel-client-bin C:\path\to\tunnel-client.exe
"@ | Write-Error
    exit 2
}

$RoutedArgs = @("codex", "plugin", "install")
if ($CodexHome) {
    $RoutedArgs += @("--codex-home", $CodexHome)
}
& $TunnelClientBin @RoutedArgs
exit $LASTEXITCODE
