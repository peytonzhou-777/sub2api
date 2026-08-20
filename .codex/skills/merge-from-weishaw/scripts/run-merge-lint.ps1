[CmdletBinding()]
param(
    [ValidateSet('Upstream', 'LocalTaint')]
    [string]$Mode = 'Upstream',

    [string]$RepositoryRoot = '',

    [string]$UpstreamRef = 'Wei-Shaw/main',

    [string]$TaintVersion = 'v2.11.4',

    [ValidateRange(1, 180)]
    [int]$ProcessTimeoutMinutes = 35
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Resolve-RepositoryRoot {
    param([string]$RequestedRoot)

    if (-not [string]::IsNullOrWhiteSpace($RequestedRoot)) {
        $resolved = (Resolve-Path -LiteralPath $RequestedRoot).Path
    } else {
        $resolved = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..\..\..')).Path
    }

    if (-not (Test-Path -LiteralPath (Join-Path $resolved '.git'))) {
        throw "仓库根目录无效：$resolved"
    }
    return $resolved
}

function Unquote-YamlScalar {
    param([string]$Value)

    $trimmed = $Value.Trim()
    if ($trimmed.Length -ge 2) {
        $first = $trimmed[0]
        $last = $trimmed[$trimmed.Length - 1]
        if (($first -eq [char]39 -and $last -eq [char]39) -or
            ($first -eq [char]34 -and $last -eq [char]34)) {
            return $trimmed.Substring(1, $trimmed.Length - 2)
        }
    }
    return $trimmed
}

# 只读取 golangci-lint action 所属 step，避免误取其他 action 的 version 字段。
function Get-UpstreamLintSettings {
    param([string]$Root)

    $workflowPath = Join-Path $Root '.github\workflows\backend-ci.yml'
    $lines = @(Get-Content -LiteralPath $workflowPath -Encoding UTF8)
    $actionIndex = -1

    for ($index = 0; $index -lt $lines.Count; $index++) {
        if ($lines[$index] -match 'uses:\s*golangci/golangci-lint-action@') {
            $actionIndex = $index
            break
        }
    }
    if ($actionIndex -lt 0) {
        throw "未在 $workflowPath 找到 golangci-lint-action。"
    }

    $actionLine = $lines[$actionIndex]
    $actionIndent = $actionLine.Length - $actionLine.TrimStart().Length
    $version = ''
    $arguments = ''
    $workingDirectory = ''

    for ($index = $actionIndex + 1; $index -lt $lines.Count; $index++) {
        $line = $lines[$index]
        $trimmed = $line.Trim()
        if ([string]::IsNullOrWhiteSpace($trimmed)) {
            continue
        }

        $indent = $line.Length - $line.TrimStart().Length
        if ($indent -le $actionIndent -and $trimmed.StartsWith('- ')) {
            break
        }

        if ($trimmed -match '^version:\s*(.+)$') {
            $version = Unquote-YamlScalar $Matches[1]
        } elseif ($trimmed -match '^args:\s*(.+)$') {
            $arguments = Unquote-YamlScalar $Matches[1]
        } elseif ($trimmed -match '^working-directory:\s*(.+)$') {
            $workingDirectory = Unquote-YamlScalar $Matches[1]
        }
    }

    if ([string]::IsNullOrWhiteSpace($version)) {
        throw 'golangci-lint action 未声明 version。'
    }
    if ([string]::IsNullOrWhiteSpace($workingDirectory)) {
        $workingDirectory = '.'
    }

    return [pscustomobject]@{
        DeclaredVersion = $version
        Arguments = $arguments
        WorkingDirectory = $workingDirectory
        WorkflowPath = $workflowPath
    }
}

function Resolve-ExactVersion {
    param([string]$DeclaredVersion)

    if ($DeclaredVersion -match '^v\d+\.\d+$') {
        return "$DeclaredVersion.0"
    }
    if ($DeclaredVersion -notmatch '^v\d+\.\d+\.\d+(?:-.+)?$') {
        throw "不支持的 golangci-lint 版本格式：$DeclaredVersion"
    }
    return $DeclaredVersion
}

function Get-LintArguments {
    param([string]$ArgumentText)

    if ([string]::IsNullOrWhiteSpace($ArgumentText)) {
        return @()
    }

    $tokens = @($ArgumentText -split '\s+' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    foreach ($token in $tokens) {
        if ($token -notmatch '^--[A-Za-z0-9][A-Za-z0-9-]*(?:=[^\s]+)?$') {
            throw "上游 lint args 含不支持的复杂参数，请人工核对：$token"
        }
    }
    return $tokens
}

function Get-GolangCILintBinary {
    param(
        [string]$Root,
        [string]$Version
    )

    $toolDirectory = Join-Path $Root "backend\.tmp\codex-tools\golangci-lint\$Version"
    $executableName = if ($IsWindows) { 'golangci-lint.exe' } else { 'golangci-lint' }
    $binaryPath = Join-Path $toolDirectory $executableName
    $expectedVersion = $Version.TrimStart('v')
    $needsInstall = -not (Test-Path -LiteralPath $binaryPath)

    if (-not $needsInstall) {
        $versionOutput = (& $binaryPath version 2>&1) -join [Environment]::NewLine
        $needsInstall = $LASTEXITCODE -ne 0 -or
            $versionOutput -notmatch "has version $([regex]::Escape($expectedVersion))\b"
    }

    if ($needsInstall) {
        New-Item -ItemType Directory -Path $toolDirectory -Force | Out-Null
        $previousGoBin = $env:GOBIN
        try {
            $env:GOBIN = $toolDirectory
            Push-Location (Join-Path $Root 'backend')
            try {
                & go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$Version"
                if ($LASTEXITCODE -ne 0) {
                    throw "安装 golangci-lint $Version 失败，退出码：$LASTEXITCODE"
                }
            } finally {
                Pop-Location
            }
        } finally {
            $env:GOBIN = $previousGoBin
        }
    }

    $actualOutput = (& $binaryPath version 2>&1) -join [Environment]::NewLine
    if ($LASTEXITCODE -ne 0 -or
        $actualOutput -notmatch "has version $([regex]::Escape($expectedVersion))\b") {
        throw "golangci-lint 版本校验失败。期望 $expectedVersion，实际输出：$actualOutput"
    }

    Write-Host "LINT_BINARY=$binaryPath"
    Write-Host "LINT_ACTUAL=$actualOutput"
    return $binaryPath
}

function Stop-ProcessTree {
    param([int]$ProcessId)

    if ($IsWindows) {
        $children = @(Get-CimInstance Win32_Process |
            Where-Object { $_.ParentProcessId -eq $ProcessId })
        foreach ($child in $children) {
            Stop-ProcessTree -ProcessId ([int]$child.ProcessId)
        }
    }

    Stop-Process -Id $ProcessId -Force -ErrorAction SilentlyContinue
}

function ConvertTo-ProcessArgumentLine {
    param([string[]]$Arguments)

    $quoted = foreach ($argument in $Arguments) {
        if ($argument.Contains('"')) {
            throw "进程参数包含不支持的双引号：$argument"
        }
        if ($argument -match '[\s"]') {
            '"' + $argument + '"'
        } else {
            $argument
        }
    }
    return $quoted -join ' '
}

# 外层 watchdog 会终止完整进程树，避免分析器不响应内部 timeout 时遗留进程。
function Invoke-LintProcess {
    param(
        [string]$BinaryPath,
        [string[]]$Arguments,
        [string]$WorkingDirectory,
        [int]$TimeoutMinutes
    )

    $argumentLine = ConvertTo-ProcessArgumentLine $Arguments
    Write-Output "LINT_WORKDIR=$WorkingDirectory"
    Write-Output "LINT_COMMAND=$BinaryPath $argumentLine"

    $process = Start-Process -FilePath $BinaryPath `
        -ArgumentList $argumentLine `
        -WorkingDirectory $WorkingDirectory `
        -PassThru `
        -NoNewWindow

    try {
        Wait-Process -Id $process.Id -Timeout ($TimeoutMinutes * 60) -ErrorAction Stop
    } catch {
        if (-not $process.HasExited) {
            Stop-ProcessTree -ProcessId $process.Id
            throw "golangci-lint 超过进程级时限 ${TimeoutMinutes} 分钟，已终止进程树。"
        }
        throw
    }

    $process.Refresh()
    if ($process.ExitCode -ne 0) {
        throw "golangci-lint 失败，退出码：$($process.ExitCode)"
    }
}

function Test-GeneratedGoFile {
    param([string]$Path)

    if (-not (Test-Path -LiteralPath $Path)) {
        return $false
    }

    $header = @(Get-Content -LiteralPath $Path -Encoding UTF8 -TotalCount 5)
    return @($header | Where-Object { $_ -match '^// Code generated .* DO NOT EDIT\.$' }).Count -gt 0
}

function Get-ChangedGoPackages {
    param(
        [string]$Root,
        [string]$BaseRef
    )

    & git -C $Root rev-parse --verify $BaseRef *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "无法解析上游引用：$BaseRef"
    }

    $changedFiles = @(& git -C $Root diff --name-only --diff-filter=ACMRT "$BaseRef..HEAD" -- backend)
    if ($LASTEXITCODE -ne 0) {
        throw '读取本地 Go 变更失败。'
    }

    $changedGoFiles = @($changedFiles | Where-Object { $_ -match '^backend/.+\.go$' })
    $directories = @($changedGoFiles |
        Group-Object { [IO.Path]::GetDirectoryName($_).Replace('\', '/') } |
        Where-Object {
            $group = $_
            $nonGeneratedFiles = @($group.Group | Where-Object {
                -not (Test-GeneratedGoFile (Join-Path $Root $_))
            })
            if ($nonGeneratedFiles.Count -eq 0) {
                Write-Host "TAINT_SKIP_GENERATED=$($group.Name)"
                return $false
            }
            return $true
        } |
        ForEach-Object { $_.Name } |
        Sort-Object -Unique)

    if ($directories.Count -eq 0) {
        return @()
    }

    $backendRoot = Join-Path $Root 'backend'
    $packages = @()
    Push-Location $backendRoot
    try {
        foreach ($directory in $directories) {
            $relativeDirectory = $directory.Substring('backend/'.Length)
            $packageArgument = './' + $relativeDirectory
            $listOutput = @(& go list $packageArgument 2>&1)
            if ($LASTEXITCODE -ne 0) {
                if (($listOutput -join "`n") -match 'build constraints exclude all Go files') {
                    Write-Host "TAINT_SKIP_BUILD_TAGS=$directory"
                    continue
                }
                throw "无法解析本地变更 package：$packageArgument"
            }
            $packages += $packageArgument
        }
    } finally {
        Pop-Location
    }

    return @($packages | Sort-Object -Unique)
}

$root = Resolve-RepositoryRoot $RepositoryRoot

if ($Mode -eq 'Upstream') {
    $settings = Get-UpstreamLintSettings $root
    $exactVersion = Resolve-ExactVersion $settings.DeclaredVersion
    $lintArguments = @(Get-LintArguments $settings.Arguments)
    $workingDirectory = [IO.Path]::GetFullPath((Join-Path $root $settings.WorkingDirectory))
    $rootPrefix = $root.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    $isRepositoryRoot = $workingDirectory.Equals($root, [StringComparison]::OrdinalIgnoreCase)
    if (-not $isRepositoryRoot -and
        -not $workingDirectory.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "上游 lint working-directory 越出仓库：$workingDirectory"
    }

    Write-Output "LINT_WORKFLOW=$($settings.WorkflowPath)"
    Write-Output "LINT_DECLARED=$($settings.DeclaredVersion)"
    Write-Output "LINT_RESOLVED=$exactVersion"

    $binary = Get-GolangCILintBinary -Root $root -Version $exactVersion
    $arguments = @('run') + $lintArguments + @('./...')
    Invoke-LintProcess -BinaryPath $binary `
        -Arguments $arguments `
        -WorkingDirectory $workingDirectory `
        -TimeoutMinutes $ProcessTimeoutMinutes
    exit 0
}

$packages = @(Get-ChangedGoPackages -Root $root -BaseRef $UpstreamRef)
if ($packages.Count -eq 0) {
    Write-Output "Wei-Shaw 基线 $UpstreamRef 之后没有本地 Go 文件变更，跳过污点检查。"
    exit 0
}

$exactTaintVersion = Resolve-ExactVersion $TaintVersion
$taintBinary = Get-GolangCILintBinary -Root $root -Version $exactTaintVersion
$taintConfig = [IO.Path]::GetFullPath(
    (Join-Path $PSScriptRoot '..\assets\golangci-local-taint.yml')
)
$taintArguments = @(
    'run',
    '--config', $taintConfig,
    '--tests=false',
    '--new-from-rev', $UpstreamRef
) + $packages

Write-Output "TAINT_BASE=$UpstreamRef"
Write-Output "TAINT_VERSION=$exactTaintVersion"
Write-Output "TAINT_PACKAGES=$($packages -join ',')"
Invoke-LintProcess -BinaryPath $taintBinary `
    -Arguments $taintArguments `
    -WorkingDirectory (Join-Path $root 'backend') `
    -TimeoutMinutes ([Math]::Min($ProcessTimeoutMinutes, 10))
