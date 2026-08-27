# herdr [[build]] step (Windows). Download a prebuilt binary only when its
# digest is present in the reviewed release-checksums.txt allowlist. A checksum
# sidecar downloaded beside the binary is never trusted.
$ErrorActionPreference = "Stop"
# Git environment variables can redirect the checkout or substitute helpers.
# The build step only needs Git for a local remote identity check, so discard
# inherited GIT_* overrides before invoking it.
Get-ChildItem Env: | Where-Object { $_.Name -like "GIT_*" } | Remove-Item -ErrorAction SilentlyContinue
$env:GIT_CONFIG_NOSYSTEM = "1"
$env:GIT_CONFIG_GLOBAL = "NUL"
$env:GIT_CONFIG_SYSTEM = "NUL"
$env:GIT_TERMINAL_PROMPT = "0"
$env:GIT_PAGER = "cat"
$env:GH_HOST = "github.com"
$env:GH_PAGER = "cat"
$env:PAGER = "cat"
$env:LESS = "-FRX"
$env:NO_COLOR = "1"
$env:CLICOLOR = "0"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $root
$bin = "herdr-gh-checks"
$repo = "itisbryan/herdr-gh-checks"

# PowerShell can search the current directory before PATH. Keep only absolute
# PATH entries outside this checkout and resolve build tools explicitly, so a
# repository file named git.exe/go.exe cannot shadow the real tools.
$rootForCompare = $root.TrimEnd('\')
$safePathEntries = @()
$rawPath = [Environment]::GetEnvironmentVariable("Path", "Process")
foreach ($pathEntry in ($rawPath -split ';')) {
  if ([string]::IsNullOrWhiteSpace($pathEntry)) { continue }
  $expanded = [Environment]::ExpandEnvironmentVariables($pathEntry)
  try {
    if (-not [IO.Path]::IsPathRooted($expanded)) { continue }
    $full = [IO.Path]::GetFullPath($expanded).TrimEnd('\')
  } catch { continue }
  if ($full.Equals($rootForCompare, [StringComparison]::OrdinalIgnoreCase) -or
      $full.StartsWith($rootForCompare + '\', [StringComparison]::OrdinalIgnoreCase)) { continue }
  $safePathEntries += $full
}
if ($safePathEntries.Count -eq 0) {
  $safePathEntries = @("$env:SystemRoot\System32", "$env:SystemRoot")
}
$env:Path = [string]::Join(';', $safePathEntries)

function Resolve-SafeApplication {
  param([string]$Name)
  foreach ($dir in $safePathEntries) {
    foreach ($suffix in @(".exe", ".com", ".bat", ".cmd")) {
      $candidate = Join-Path $dir ($Name + $suffix)
      if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) { continue }
      try {
        $item = Get-Item -Force -LiteralPath $candidate
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { continue }
        $fullCandidate = $item.FullName
        if ($fullCandidate.Equals($rootForCompare, [StringComparison]::OrdinalIgnoreCase) -or
            $fullCandidate.StartsWith($rootForCompare + '\', [StringComparison]::OrdinalIgnoreCase)) { continue }
        return $fullCandidate
      } catch { continue }
    }
  }
  return $null
}

$gitExe = Resolve-SafeApplication "git"
$goExe = Resolve-SafeApplication "go"
$versionMatch = Select-String -Path herdr-plugin.toml -Pattern '^version = "(.*)"' | Select-Object -First 1
$version = if ($versionMatch) { $versionMatch.Matches[0].Groups[1].Value } else { "" }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }

function Assert-OutputIsNotLink {
  $target = Join-Path $root "$bin.exe"
  if (Test-Path -LiteralPath $target) {
    $item = Get-Item -Force -LiteralPath $target
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw "refusing to overwrite symlink/reparse point $target"
    }
    if ($item.PSIsContainer) {
      throw "refusing to overwrite non-regular output $target"
    }
  }
}

function Build-FromSource {
  if ($null -ne $goExe) {
    Write-Host "herdr-gh-checks: building from source with go"
    # Build outside the destination, then rename so a target reparse point
    # cannot be followed between a check and the compiler's output open.
    $buildTmp = Join-Path $root ("." + $bin + ".build." + [Guid]::NewGuid().ToString("N"))
    $oldToolchain = $env:GOTOOLCHAIN
    $env:GOTOOLCHAIN = "local"
    try {
      & $goExe build -trimpath -ldflags "-s -w" -o $buildTmp .
      $code = $LASTEXITCODE
      if ($code -ne 0) { exit $code }
      Assert-OutputIsNotLink
      Move-Item -Force -LiteralPath $buildTmp -Destination (Join-Path $root "$bin.exe")
    } finally {
      Remove-Item -Force -LiteralPath $buildTmp -ErrorAction SilentlyContinue
      if ($null -eq $oldToolchain) { Remove-Item Env:GOTOOLCHAIN -ErrorAction SilentlyContinue } else { $env:GOTOOLCHAIN = $oldToolchain }
    }
    exit 0
  }
  Write-Error "herdr-gh-checks: no reviewed prebuilt binary for windows/$arch and Go is not installed. Install Go 1.25.14+ or use WSL."
  exit 1
}

function Test-CanonicalSourceRepository {
  if (-not (Test-Path -LiteralPath ".git")) { return $true }
  if ($null -eq $gitExe) { return $false }
  $remote = ""
  try {
    & $gitExe -c protocol.ext.allow=never -c protocol.file.allow=never -c core.hooksPath=NUL -c core.fsmonitor=false -c core.pager=cat config --local --includes --get-regexp '^url\..*\.(insteadof|pushinsteadof)$' 2>$null | Out-Null
    if ($LASTEXITCODE -eq 0) { return $false }
    $remote = (& $gitExe -c protocol.ext.allow=never -c protocol.file.allow=never -c core.hooksPath=NUL -c core.fsmonitor=false -c core.pager=cat config --local --no-includes --get remote.origin.url 2>$null).Trim()
  } catch { return $false }
  $remote = $remote -replace '\.git$','' -replace '/$',''
  if ($remote -notin @(
    "https://github.com/$repo",
    "git@github.com:$repo",
    "ssh://git@github.com/$repo"
  )) { return $false }
  # A dirty checkout may have changed this installer, manifest, or source.
  # Build the local tree rather than selecting a binary using modified metadata.
  try {
    & $gitExe -c protocol.ext.allow=never -c protocol.file.allow=never -c core.hooksPath=NUL -c core.fsmonitor=false -c core.pager=cat status --porcelain --untracked-files=all > $null 2>$null
    if ($LASTEXITCODE -ne 0) { return $false }
    $dirty = (& $gitExe -c protocol.ext.allow=never -c protocol.file.allow=never -c core.hooksPath=NUL -c core.fsmonitor=false -c core.pager=cat status --porcelain --untracked-files=all 2>$null | Select-Object -First 1).Trim()
    return [string]::IsNullOrEmpty($dirty)
  } catch { return $false }
}

if (-not $version) { Build-FromSource }
if ($version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$') {
  Write-Warning "herdr-gh-checks: invalid manifest version; building from source"
  Build-FromSource
}
if (-not (Test-CanonicalSourceRepository)) {
  Write-Warning "herdr-gh-checks: checkout is not the canonical release repository; building from source"
  Build-FromSource
}
$asset = "$bin-windows-$arch.exe"
$base = "https://github.com/$repo/releases/download/v$version"
$key = "v$version/$asset"
$want = $null
if (Test-Path -LiteralPath "release-checksums.txt") {
  foreach ($line in Get-Content -LiteralPath "release-checksums.txt") {
    $parts = $line -split '\s+'
    if ($parts.Count -ge 2 -and -not $parts[0].StartsWith("#") -and $parts[0] -eq $key) {
      $want = $parts[1].ToLowerInvariant()
      break
    }
  }
}
if ($want -notmatch '^[0-9a-f]{64}$') {
  Write-Warning "herdr-gh-checks: no reviewed digest for $key; building from source"
  Build-FromSource
}

$tmpDir = Join-Path ([IO.Path]::GetTempPath()) ("herdr-gh-checks-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmpDir | Out-Null
$tmp = Join-Path $tmpDir "$bin.exe"
try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
  $response = Invoke-WebRequest -Uri "$base/$asset" -OutFile $tmp -UseBasicParsing -MaximumRedirection 5 -PassThru
  $finalUri = $null
  if ($null -ne $response.BaseResponse -and $null -ne $response.BaseResponse.ResponseUri) {
    $finalUri = $response.BaseResponse.ResponseUri
  } elseif ($null -ne $response.BaseResponse -and $null -ne $response.BaseResponse.RequestMessage) {
    $finalUri = $response.BaseResponse.RequestMessage.RequestUri
  }
  if ($null -eq $finalUri -or $finalUri.Scheme -ne "https") {
    throw "download did not remain on HTTPS"
  }
  $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $tmp).Hash.ToLowerInvariant()
  if ($want -eq $got) {
    Assert-OutputIsNotLink
    Move-Item -Force $tmp "$bin.exe"
    Write-Host "herdr-gh-checks: installed allowlisted prebuilt $asset v$version"
    exit 0
  }
  Write-Warning "herdr-gh-checks: digest mismatch for $asset; falling back to source"
} catch {
  Write-Warning "herdr-gh-checks: download failed ($_); falling back to source"
} finally {
  Remove-Item -Recurse -Force -LiteralPath $tmpDir -ErrorAction SilentlyContinue
}
Build-FromSource
