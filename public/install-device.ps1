# NexTerm 设备 agent 一键接入 (Windows)。
# 用法: iex "& { $(iwr -useb https://<server>/install-device.ps1) } -Server <接入地址> -Code <接入码> -Version <版本> [-Insecure] [-DataDir <目录>]"
# 支持 Windows AMD64/ARM64, 需要 PowerShell 5.1+; 免管理员: 以当前用户注册登录自启计划任务 (schtasks, 无窗口后台运行)。
# 二进制与同 release 的 SHA256SUMS 校验通过后才安装并 enroll, 最后注册 NexTermAgent 计划任务。
#Requires -Version 5.1

[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Server,
  [Parameter(Mandatory = $true)]
  [string]$Code,
  [Parameter(Mandatory = $true)]
  [string]$Version,
  [switch]$Insecure,
  [string]$DataDir = (Join-Path $env:LOCALAPPDATA 'NexTerm')
)

$ErrorActionPreference = 'Stop'

function Fail([string]$Message) {
  Write-Error "install-device: $Message"
}

if ($PSVersionTable.PSVersion -lt [version]'5.1') {
  Fail "需要 PowerShell 5.1+, 当前版本: $($PSVersionTable.PSVersion)"
}

[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

if ($Version -notmatch '^[0-9A-Za-z.-]+$') {
  Fail "非法 -Version: $Version"
}

$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -eq 'x86' -and $env:PROCESSOR_ARCHITEW6432) {
  $arch = $env:PROCESSOR_ARCHITEW6432
}
$AssetArch = $null
switch ($arch) {
  'AMD64' { $AssetArch = 'amd64' }
  'ARM64' { $AssetArch = 'arm64' }
  default { Fail "不支持的 CPU 架构: $arch (仅支持 AMD64/ARM64)" }
}

$ReleaseBase = 'https://github.com/Hello-CTF/NexTerm/releases/download'
$Asset = "NexTerm-agent_${Version}_windows_${AssetArch}.zip"
$AssetUrl = "$ReleaseBase/v$Version/$Asset"
$SumsUrl = "$ReleaseBase/v$Version/SHA256SUMS"

$Work = Join-Path ([IO.Path]::GetTempPath()) ('nexterm-install-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $Work | Out-Null

try {
  Write-Host "==> 下载 $AssetUrl"
  try {
    Invoke-WebRequest -UseBasicParsing -Uri $AssetUrl -OutFile (Join-Path $Work $Asset)
    Invoke-WebRequest -UseBasicParsing -Uri $SumsUrl -OutFile (Join-Path $Work 'SHA256SUMS')
  } catch {
    Fail "下载失败: $($_.Exception.Message)"
  }

  $Expected = $null
  foreach ($line in Get-Content (Join-Path $Work 'SHA256SUMS')) {
    $fields = $line.Trim() -split '\s+'
    if ($fields.Count -ge 2 -and $fields[-1].TrimStart('*') -eq $Asset) {
      $Expected = $fields[0]
      break
    }
  }
  if (-not $Expected -or $Expected -notmatch '^[0-9a-fA-F]{64}$') {
    Fail "SHA256SUMS 中没有 $Asset 的合法条目, 拒绝安装"
  }
  $Actual = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $Work $Asset)).Hash
  if ($Actual -ne $Expected) {
    Fail "SHA256 校验失败 ($Asset): 期望 $Expected, 实际 $Actual, 拒绝安装"
  }

  $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\NexTerm'
  $Target = Join-Path $InstallDir 'nexterm-agent.exe'
  Write-Host "==> 校验通过, 解压并安装到 $Target"
  $ExtractDir = Join-Path $Work 'extract'
  Expand-Archive -Path (Join-Path $Work $Asset) -DestinationPath $ExtractDir
  $Extracted = Join-Path $ExtractDir 'nexterm-agent.exe'
  if (-not (Test-Path $Extracted)) {
    Fail "发布包 $Asset 根目录缺少 nexterm-agent.exe"
  }
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
  if (Test-Path $Target) {
    # v1.x 迁移: 旧版以 sc.exe 系统服务运行, 先尽力停删 (非管理员会失败, 忽略);
    # 运行中映像不可覆盖但可改名, 先改名再拷贝, 保证任何权限下都能换上新二进制。
    & sc.exe stop NexTermAgent 2>$null | Out-Null
    & sc.exe delete NexTermAgent 2>$null | Out-Null
    Move-Item -Path $Target -Destination "$Target.old" -Force -ErrorAction SilentlyContinue
  }
  Copy-Item -Path $Extracted -Destination $Target -Force

  $UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (-not $UserPath -or (($UserPath -split ';') -notcontains $InstallDir)) {
    $NewPath = $InstallDir
    if ($UserPath) {
      $NewPath = $UserPath.TrimEnd(';') + ';' + $InstallDir
    }
    [Environment]::SetEnvironmentVariable('Path', $NewPath, 'User')
    Write-Host "==> 已把 $InstallDir 加入用户 PATH (新开终端生效)"
  }

  Write-Host '==> 注册设备 enroll (接入码不在日志回显)'
  $InsecureArgs = @()
  $InsecureText = ''
  if ($Insecure) {
    $InsecureArgs = @('--insecure')
    $InsecureText = ' --insecure'
  }
  Write-Host "    $Target enroll --server $Server --code ******** --data-dir $DataDir$InsecureText"
  & $Target enroll --server $Server --code $Code --data-dir $DataDir @InsecureArgs
  if ($LASTEXITCODE -ne 0) {
    Fail "设备注册失败 (exit $LASTEXITCODE)"
  }

  Write-Host '==> 注册计划任务 NexTermAgent (登录自启, 当前用户, 免管理员, 无窗口)'
  & $Target install --data-dir $DataDir
  if ($LASTEXITCODE -ne 0) {
    Fail "计划任务注册失败 (exit $LASTEXITCODE)"
  }

  Write-Host "==> 完成。数据目录: $DataDir"
  Write-Host "    查看状态: `"$Target`" status --data-dir `"$DataDir`""
} finally {
  Remove-Item -Recurse -Force $Work -ErrorAction SilentlyContinue
}
