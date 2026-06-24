<#
.SYNOPSIS
    Install or update jacques from the latest GitHub release.
.DESCRIPTION
    Downloads the latest jacques binary for your OS/arch from GitHub Releases
    and places it in ~/.jacques/bin. Adds that directory to your PATH if needed.
.PARAMETER Version
    Specific version tag to install (e.g. "v0.1.0"). Defaults to latest.
.PARAMETER InstallDir
    Where to put the binary. Defaults to ~/.jacques/bin.
#>
param(
    [string]$Version,
    [string]$InstallDir = (Join-Path $HOME ".jacques" "bin")
)

$ErrorActionPreference = "Stop"
$repo = "jack-work/jacques"

if (-not $Version) {
    $latest = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
    $Version = $latest.tag_name
    Write-Host "Latest version: $Version"
}

if ($IsLinux) { $os = "linux" }
elseif ($IsMacOS) { $os = "darwin" }
else { $os = "windows" }

$cpuArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLower()
switch ($cpuArch) {
    "arm64" { $arch = "arm64" }
    default { $arch = "amd64" }
}

$ext = if ($os -eq "windows") { ".exe" } else { "" }

$assetName = "jacques-${Version}-${os}-${arch}${ext}"
$url = "https://github.com/$repo/releases/download/$Version/$assetName"

New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
$dest = Join-Path $InstallDir "jacques$ext"

Write-Host "Downloading $assetName"
Write-Host "  from $url"

try {
    Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing
} catch {
    Write-Error "Download failed: $_`nCheck that a release asset exists for your platform ($os/$arch) at:`n  https://github.com/$repo/releases/tag/$Version"
    return
}

if ($os -ne "windows") {
    chmod +x $dest
}

$userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
if ($userPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("PATH", "$userPath;$InstallDir", "User")
    Write-Host "Added $InstallDir to user PATH (restart your shell to pick it up)"
}

Write-Host "Installed jacques $Version to $dest"
& $dest version
