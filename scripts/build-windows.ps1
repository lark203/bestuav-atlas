# ============================================================
# bestuav-atlas - Windows build script (PowerShell)
# ------------------------------------------------------------
# Outputs (file names follow mygo.json "slug" = quantum):
#   build/quantum-<version>-windows-amd64.exe        (raw executable)
#   build/quantum-setup-<version>-amd64.exe          (NSIS installer)
# ============================================================
#
# [Prerequisites]
#   1. Go 1.27+ on PATH (check with: go version).
#      NOTE: go.mod requires go >= 1.27.3; older Go refuses to build.
#   2. mygo CLI: declared as a tool in go.mod
#      (tool github.com/egoist/mygo/cmd/mygo), invoked via `go tool mygo`.
#      No separate install needed; first run auto-builds it (needs network).
#   3. Run on Windows. NSIS is auto-downloaded by mygo when missing.
#   4. (Optional) code-signing cert: configure in mygo.json "windows"
#      section or pass -sign; without it the installer is unsigned and
#      SmartScreen will warn at install time.
#
# [Auto-update (mygo.Updater)]
#   mygo.json "updates" configures the GitHub update feed + ed25519 public key.
#   `go tool mygo build` injects the feed/key at link time, and emits a signed
#   archive + manifest (update-<target>.json) per platform.
#   - Signing private key: export MYGO_UPDATER_PRIVATE_KEY before building
#     (contents of the mygo-update.key from `go tool mygo keygen`).
#     Without it mygo emits an UNSIGNED archive, and installed apps cannot
#     verify / self-update.
#   - Publish to a GitHub Release (as draft): `go tool mygo build -upload`
#     needs the gh CLI logged in (gh auth login) or GH_TOKEN. updates.github
#     is lark203/bestuav-atlas; if the repo moves, update it and re-release,
#     because the feed is linked into the binary.
#   - The full three-platform release runs on GitHub Actions
#     (.github/workflows/release.yml) when a v* tag is pushed; this script
#     is the local, Windows-only path.
#   - At runtime: Settings > Update > "Update now" downloads, verifies the
#     signature, swaps the app in place and relaunches. Dev builds (go run)
#     disable updates; Settings shows "only release builds support updates".
#
# [About MSI]
#   mygo produces an NSIS .exe installer, NOT .msi.
#   If .msi is mandated by policy, wrap the raw exe with WiX Toolset
#   separately. This script follows the standard mygo build path only.
#
# [Cross-platform note]
#   `go tool mygo build` is pure-Go cross-compilation (no cgo), so Windows
#   can also emit macOS/Linux raw binaries; but .dmg/.deb packages must be
#   produced on macOS/Linux respectively. This script targets Windows only.
#   When cross-compiling, make sure CGO_ENABLED is not exported as 1: mygo
#   only defaults it to 0 when the variable is empty. With CGO_ENABLED=1 the
#   host gcc tries to build the cgo runtime for the target and fails with
#   sigaction/setenv errors. The CI workflow sets CGO_ENABLED=0 explicitly.
#
# [Usage - MUST run from the REPO ROOT (where mygo.json lives)]
#   powershell -ExecutionPolicy Bypass -File scripts/build-windows.ps1
#   (PowerShell 7 / pwsh also works: pwsh scripts/build-windows.ps1)
#   Do NOT double-click the .ps1; launch it from a PowerShell terminal.
#
# [Encoding note]
#   This file is ASCII-only on purpose: Windows PowerShell 5.1 decodes
#   .ps1 via the system codepage (GBK on Chinese Windows), so a UTF-8
#   file without BOM misreads Chinese bytes and breaks string literals.
#   If you prefer Chinese comments, save this file as UTF-8 WITH BOM.
# ============================================================

$ErrorActionPreference = "Stop"

# Auto-load the updater signing key so `mygo build` emits a SIGNED update
# archive (required for installed apps to self-update). The key is the
# mygo-update.key produced by `go tool mygo keygen`; it lives OUTSIDE the
# repo and must never be committed.
if (-not $env:MYGO_UPDATER_PRIVATE_KEY) {
    $keyPath = Join-Path $env:APPDATA "mygo\update-keys\bestuav-atlas\mygo-update.key"
    if (Test-Path $keyPath) {
        $env:MYGO_UPDATER_PRIVATE_KEY = (Get-Content -Raw -Encoding utf8 $keyPath).Trim()
        Write-Host "==> Loaded updater signing key from $keyPath"
    } else {
        Write-Warning ("MYGO_UPDATER_PRIVATE_KEY is not set and no key at {0}. " +
            "The build will produce an UNSIGNED update archive; installed apps " +
            "will NOT be able to verify/self-update. Generate one with " +
            "'go tool mygo keygen' and re-run, or export the key first." -f $keyPath)
    }
}

# Read version from mygo.json (kept in sync with build output naming).
$config = Get-Content -Raw -Encoding utf8 .\mygo.json | ConvertFrom-Json
$version = $config.version
Write-Host "==> Building Windows target (version: $version)"

# Build Windows/amd64 only. Output dir is set by mygo.json "out" (build/).
# `mygo build` injects the update feed/key from mygo.json "updates" at link
# time, so the installed app can check for updates automatically.
go tool mygo build -platform windows/amd64

# List produced artifacts for confirmation.
Write-Host "==> Build done. Artifacts in build/:"
Get-ChildItem build\*-windows-* -ErrorAction SilentlyContinue | ForEach-Object { Write-Host "    $($_.Name)" }

Write-Host "==> Done. Double-click quantum-setup-*-amd64.exe to install."
