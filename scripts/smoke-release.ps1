param(
    [Parameter(Mandatory = $true)]
    [string]$Binary
)

$ErrorActionPreference = 'Stop'
$Binary = (Resolve-Path -LiteralPath $Binary).Path
$Root = Join-Path ([System.IO.Path]::GetTempPath()) ("oxpio-release-smoke-" + [guid]::NewGuid().ToString('N'))
$Vault = Join-Path $Root 'vault'
$Server = $null
New-Item -ItemType Directory -Path $Vault | Out-Null

try {
    $Version = (& $Binary version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) { throw "oxpio version exited $LASTEXITCODE" }
    $VersionFlag = (& $Binary --version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) { throw "oxpio --version exited $LASTEXITCODE" }
    if ($Version -ne $VersionFlag) { throw 'version forms differ' }
    if ($Version -notmatch '^oxpio version=[^ ]+ commit=(?!unknown)[^ ]+ date=(?!unknown)[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z type=(snapshot|release)$') {
        throw "invalid release-shaped version: $Version"
    }

    Push-Location $Vault
    try {
        & $Binary init
        if ($LASTEXITCODE -ne 0) { throw "oxpio init exited $LASTEXITCODE" }
        & $Binary build
        if ($LASTEXITCODE -ne 0) { throw "oxpio build exited $LASTEXITCODE" }
    }
    finally {
        Pop-Location
    }
    if (-not (Test-Path -LiteralPath (Join-Path $Vault 'public/index.html') -PathType Leaf)) {
        throw 'default build did not create public/index.html'
    }

    $Listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $Listener.Start()
    $Port = ([System.Net.IPEndPoint]$Listener.LocalEndpoint).Port
    $Listener.Stop()

    $Stdout = Join-Path $Root 'serve.stdout.log'
    $Stderr = Join-Path $Root 'serve.stderr.log'
    $Server = Start-Process -FilePath $Binary -ArgumentList @('serve', '--port', "$Port") -WorkingDirectory $Vault -RedirectStandardOutput $Stdout -RedirectStandardError $Stderr -PassThru
    $Response = $null
    for ($Attempt = 0; $Attempt -lt 50; $Attempt++) {
        try {
            $Response = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/" -TimeoutSec 2
            if ($Response.StatusCode -eq 200) { break }
        }
        catch {
            if ($Server.HasExited) { break }
        }
        Start-Sleep -Milliseconds 200
    }
    if ($null -eq $Response -or $Response.StatusCode -ne 200) {
        $Log = (Get-Content -LiteralPath $Stdout, $Stderr -ErrorAction SilentlyContinue | Out-String)
        throw "release serve smoke did not return HTTP 200`n$Log"
    }
    if ($Response.Content -notmatch '<html') { throw 'served response is not HTML' }

    Write-Host "release smoke verified: $Version"
}
finally {
    if ($null -ne $Server -and -not $Server.HasExited) {
        Stop-Process -Id $Server.Id -Force -ErrorAction SilentlyContinue
        $Server.WaitForExit()
    }
    Remove-Item -LiteralPath $Root -Recurse -Force -ErrorAction SilentlyContinue
}
