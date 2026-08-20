$ErrorActionPreference = "Stop"

$PluginRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$TunnelClientBin = $null
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

function Rest-Args {
    param([object[]]$Values)
    if (-not $Values -or $Values.Count -le 1) {
        return @()
    }
    return @($Values[1..($Values.Count - 1)])
}

if ($args.Count -ge 1 -and $args[0] -eq "--tunnel-client-bin") {
    if ($args.Count -lt 2) {
        [Console]::Error.WriteLine("error: --tunnel-client-bin requires a value")
        exit 2
    }
    if (Test-ExecutableFile $args[1]) {
        $TunnelClientBin = $args[1]
    } else {
        Add-Attempt "--tunnel-client-bin: $($args[1]) was not an executable file"
    }
    $args = $args[2..($args.Count - 1)]
} else {
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

$HintPath = Join-Path $PluginRoot ".tunnel-client-bin"
if (-not $TunnelClientBin -and (Test-Path -LiteralPath $HintPath -PathType Leaf)) {
    $HintedBin = (Get-Content -LiteralPath $HintPath -TotalCount 1).Trim()
    if (Test-ExecutableFile $HintedBin) {
        $TunnelClientBin = $HintedBin
    } else {
        Add-Attempt "installed .tunnel-client-bin hint: $HintedBin was not an executable file"
    }
} elseif (-not $TunnelClientBin) {
    Add-Attempt "installed .tunnel-client-bin hint: not present"
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
    $PathCandidate = $null
    foreach ($Name in @("tunnel-client.exe", "tunnel-client")) {
        $Command = Get-Command $Name -ErrorAction SilentlyContinue
        if ($Command) {
            $PathCandidate = $Command.Source
            break
        }
    }
    if ($PathCandidate) {
        Add-Attempt "PATH: found $PathCandidate but ignored because .tunnel-client-bin is missing; set TUNNEL_CLIENT_BIN or pass --tunnel-client-bin to use it explicitly"
    } else {
        Add-Attempt "PATH: no tunnel-client executable found"
    }
}

if ($args.Count -eq 0 -or $args[0] -in @("-h", "--help")) {
    @"
Usage: tunnel_mcp <command> [args]

Routes to native tunnel-client commands:
  create|connect|list|status|stop|disconnect|rm|cleanup
  admin-profiles <subcommand>
  diagnose [alias]

All routed commands default to --json.
"@ | Write-Output
    exit 0
}

$RoutedArgs = @()
switch ($args[0]) {
    "admin-profiles" {
        $RoutedArgs = @("admin-profiles") + (Rest-Args $args)
    }
    "diagnose" { $RoutedArgs = @("codex", "diagnose", "--plugin-root", $PluginRoot) + (Rest-Args $args) }
    "create" { $RoutedArgs = @("runtimes", "create") + (Rest-Args $args) }
    "connect" { $RoutedArgs = @("runtimes", "connect") + (Rest-Args $args) }
    "list" { $RoutedArgs = @("runtimes", "list") + (Rest-Args $args) }
    "status" { $RoutedArgs = @("runtimes", "status") + (Rest-Args $args) }
    "stop" { $RoutedArgs = @("runtimes", "stop") + (Rest-Args $args) }
    "disconnect" { $RoutedArgs = @("runtimes", "disconnect") + (Rest-Args $args) }
    "cleanup" { $RoutedArgs = @("runtimes", "cleanup") + (Rest-Args $args) }
    "rm" { $RoutedArgs = @("runtimes", "rm") + (Rest-Args $args) }
    "remove" { $RoutedArgs = @("runtimes", "rm") + (Rest-Args $args) }
    default {
        [Console]::Error.WriteLine("unsupported tunnel_mcp command; use create, connect, list, status, stop, disconnect, rm, cleanup, diagnose, or admin-profiles")
        exit 2
    }
}

if (-not ($RoutedArgs -contains "--json")) {
    $RoutedArgs += "--json"
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
- Then point the plugin at the binary with one of:
  - set TUNNEL_CLIENT_BIN to the full path to tunnel-client.exe
  - rerun with --tunnel-client-bin C:\path\to\tunnel-client.exe
- Or reinstall the plugin with --tunnel-client-bin C:\path\to\tunnel-client.exe

Executable naming guidance:
- macOS/Linux: tunnel-client
- Windows: tunnel-client.exe

This plugin does not auto-download, auto-clone, or auto-run remote tunnel-client binaries.
If the user explicitly asks Codex to set up tunnel-client, Codex may clone and build it from the public repo commands above.
"@ | Write-Error
    exit 2
}

& $TunnelClientBin @RoutedArgs
exit $LASTEXITCODE
