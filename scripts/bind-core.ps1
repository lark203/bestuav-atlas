<#
.SYNOPSIS
    Build the shared Go core into a mobile-linkable artifact
    (Android .aar / iOS .xcframework).

.DESCRIPTION
    The desktop app draws its own UI with mygo; the phone app uses Flutter.
    The only shared piece is the Go core under internal/store. gomobile is the
    official way to expose it to a phone, and this script pins the details that
    are otherwise scattered across env vars, NDK paths and output dirs.

    One-time setup:
        go install golang.org/x/mobile/cmd/gomobile@latest
        gomobile init

    Android needs an NDK. When ANDROID_NDK_HOME is unset, the script picks the
    highest version under %ANDROID_HOME%\ndk.

    NOTE: keep this file ASCII-only. Windows PowerShell 5.1 reads .ps1 files
    without a BOM using the ANSI code page, so any non-ASCII text (including
    comments) is enough to break parsing.

.EXAMPLE
    ./scripts/bind-core.ps1                        # Android arm64 -> build/atlas_core.aar
    ./scripts/bind-core.ps1 -AndroidArch arm64,arm # two ABIs in one aar
    ./scripts/bind-core.ps1 -Target ios            # macOS only (used by CI)

.NOTES
    Artifacts are not committed: bestuav-app rebuilds the core from an atlas
    tag, so the repo never carries a stale .aar from months ago.
#>
param(
    [ValidateSet('android', 'ios', 'all')]
    [string]$Target = 'android',

    [string]$OutDir = 'build',

    # Android min API: 26 matches Flutter's current default minSdk.
    [int]$AndroidAPI = 26,

    # Comma separated, e.g. "arm64" or "arm64,arm". Each ABI adds ~17MB to the aar.
    [string]$AndroidArch = 'arm64'
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $gomobile = Join-Path (go env GOBIN) 'gomobile.exe'
    if (-not (Test-Path $gomobile)) {
        $gomobile = Join-Path (go env GOPATH) 'bin\gomobile.exe'
    }
    if (-not (Test-Path $gomobile)) {
        throw "gomobile not found. Run: go install golang.org/x/mobile/cmd/gomobile@latest ; gomobile init"
    }

    New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

    if ($Target -eq 'android' -or $Target -eq 'all') {
        if (-not $env:ANDROID_NDK_HOME) {
            $sdk = if ($env:ANDROID_HOME) { $env:ANDROID_HOME } else { $env:ANDROID_SDK_ROOT }
            if (-not $sdk) { throw "Neither ANDROID_NDK_HOME nor ANDROID_HOME is set" }
            $ndk = Get-ChildItem (Join-Path $sdk 'ndk') -Directory -ErrorAction SilentlyContinue |
                Sort-Object Name -Descending | Select-Object -First 1
            if (-not $ndk) { throw "No NDK under $sdk\ndk; install one via SDK Manager" }
            $env:ANDROID_NDK_HOME = $ndk.FullName
        }
        Write-Host "NDK: $env:ANDROID_NDK_HOME"

        $aar = Join-Path $OutDir 'atlas_core.aar'
        $targets = ($AndroidArch -split ',' | ForEach-Object { "android/$($_.Trim())" }) -join ','
        Write-Host "Binding $targets -> $aar"
        & $gomobile bind "-target=$targets" "-androidapi=$AndroidAPI" -o $aar ./mobile
        if ($LASTEXITCODE -ne 0) { throw "gomobile bind failed (exit=$LASTEXITCODE)" }
        Write-Host ("Done: {0} ({1:N1} MB)" -f $aar, ((Get-Item $aar).Length / 1MB))
    }

    if ($Target -eq 'ios' -or $Target -eq 'all') {
        if ($env:OS -eq 'Windows_NT') {
            throw "iOS bindings need macOS: 'gomobile bind -target=ios' requires the Xcode toolchain. Run it in CI instead (see M4 in the mobile plan)."
        }
        $xc = Join-Path $OutDir 'AtlasCore.xcframework'
        Write-Host "Binding ios -> $xc"
        & $gomobile bind -target=ios -o $xc ./mobile
        if ($LASTEXITCODE -ne 0) { throw "gomobile bind (ios) failed (exit=$LASTEXITCODE)" }
        Write-Host "Done: $xc"
    }
}
finally {
    Pop-Location
}
