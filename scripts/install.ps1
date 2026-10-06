# sol 安装脚本（Windows）—— 零参数。
#
# 与 scripts/install.sh 是同一份契约（docs/install-design.md）：认出现状 → 生成/读取"怎么跑"的
# 配置（%APPDATA%\sol\install.yaml，里面只有 run.args）→ 展示 → 问一句 → 才动手。
#
#   powershell -ExecutionPolicy Bypass -File install.ps1     或双击 install.cmd
#   没有终端（管道喂答案）时只生成配置，不执行任何操作
#
# ⚠ 证据强度：本机没有 Windows 实机，这份脚本**只做过构造级校验（逐行审查）**，未在真机上跑过。
#    设计文档 §10 要求由 .github/workflows/install-smoke.yml 的 windows matrix 补上真机证据。
#
# 环境变量（都有默认值；测试与多实例用）：
#   SOL_INSTALL_ROOT  把落点挂到另一个目录下（默认 C:\ProgramData）
#   SOL_UNIT_NAME     计划任务名（默认 sol）

[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$Design = 'docs/install-design.md'

function Say($msg) { Write-Host $msg }
function Head2($msg) { Write-Host ""; Write-Host $msg -ForegroundColor White }
function Warn2($msg) { Write-Warning $msg }
function Die($msg) { Write-Error $msg; exit 1 }

# ───────────────────────── 平台与路径 ─────────────────────────

if ($env:SOL_INSTALL_ROOT) { $Root = $env:SOL_INSTALL_ROOT } else { $Root = 'C:\ProgramData' }
if ($env:SOL_UNIT_NAME) { $TaskName = $env:SOL_UNIT_NAME } else { $TaskName = 'sol' }

$InstallDir = Join-Path $Root 'sol'
$DestBin = Join-Path $InstallDir 'sol.exe'
$Ledger = Join-Path $InstallDir 'install.json'
$History = Join-Path $InstallDir 'install.log'
$DesktopConfig = Join-Path $InstallDir 'sol.yaml'
$UserConfig = Join-Path $env:APPDATA 'sol\install.yaml'

$IsAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

function Has-Sha256($path) {
	if (-not (Test-Path $path)) { return '' }
	(Get-FileHash -Algorithm SHA256 -Path $path).Hash.ToLower()
}

# ───────────────────────── 认出现状 ─────────────────────────

$ScriptDir = $PSScriptRoot
$candidates = @(
	(Join-Path $ScriptDir 'sol.exe'),
	(Join-Path (Get-Location).Path 'sol.exe')
)
$LocalBin = ''
foreach ($c in $candidates) { if (Test-Path $c) { $LocalBin = $c; break } }
if (-not $LocalBin) {
	$cmd = Get-Command sol.exe -ErrorAction SilentlyContinue
	if ($cmd) { $LocalBin = $cmd.Source }
}

$PathSols = @()
foreach ($d in ($env:PATH -split ';')) {
	if (-not $d) { continue }
	$p = Join-Path $d 'sol.exe'
	if (Test-Path $p) { $PathSols += $p }
}

function Binary-Version($path) {
	if (-not $path -or -not (Test-Path $path)) { return '' }
	try { ((& $path --version) 2>$null | Select-Object -First 1) } catch { '' }
}

$LedgerExists = Test-Path $Ledger
$LedgerJson = $null
if ($LedgerExists) {
	try { $LedgerJson = Get-Content -Raw $Ledger | ConvertFrom-Json } catch { $LedgerJson = $null }
}

function Task-State {
	try { $null = schtasks /Query /TN $TaskName /XML 2>$null; if ($LASTEXITCODE -eq 0) { return 'installed' } } catch { }
	return 'absent'
}

# ───────────────────────── 安装配置（只有 run.args）─────────────────────────

$DefaultArgs = @('listen', '--config', $DesktopConfig)

function Write-UserConfig {
	$dir = Split-Path -Parent $UserConfig
	if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
	$lines = @(
		'# 由 install.ps1 生成：sol 怎么跑。改这里，然后重新运行一遍脚本。',
		'run:',
		'  args: [' + ($DefaultArgs -join ', ') + ']'
	)
	Set-Content -Path $UserConfig -Value $lines -Encoding UTF8
}

function Read-Args {
	$out = @()
	# 注意：调用处必须 @(...) 包一层——PowerShell 会把单元素数组拆成标量。
	foreach ($line in (Get-Content $UserConfig)) {
		if ($line -match '^\s*args:\s*(.*)$') {
			$val = $Matches[1].Trim().Trim('[', ']')
			foreach ($tok in ($val -split ',')) {
				$t = $tok.Trim()
				if ($t) { $out += $t }
			}
		}
	}
	$out
}

$ConfigGenerated = $false
if (-not (Test-Path $UserConfig)) { Write-UserConfig; $ConfigGenerated = $true }
$ArgsList = @(Read-Args)
if (-not $ArgsList) { Die "$UserConfig 里没有 run.args（写法：run:{ args: [$($DefaultArgs -join ', ')] }）" }

function Test-Args {
	if (-not $LocalBin) { return }
	if ($ArgsList[0] -ne 'listen') { Die "run.args 第一个词必须是 listen（当前是 $($ArgsList[0])）" }
	$help = ''
	try { $help = (& $LocalBin listen --help 2>$null) -join "`n" } catch { return }
	$known = [regex]::Matches($help, '--[a-zA-Z][a-zA-Z-]*') | ForEach-Object { $_.Value } | Sort-Object -Unique
	foreach ($tok in $ArgsList) {
		if ($tok.StartsWith('-') -and ($known -notcontains $tok)) {
			Die "run.args 里有 sol listen 不认识的参数：$tok（见 sol listen --help）"
		}
	}
}
Test-Args

$RunConfig = $DesktopConfig
for ($i = 0; $i -lt $ArgsList.Count - 1; $i++) {
	if ($ArgsList[$i] -eq '--config') { $RunConfig = $ArgsList[$i + 1] }
}

function Get-ThresholdPorts {
	$ports = @()
	if (-not (Test-Path $RunConfig)) { return $ports }
	foreach ($line in (Get-Content $RunConfig)) {
		if ($line -match '^\s*ports:\s*\[([0-9,\s]*)\]') {
			foreach ($n in ($Matches[1] -split ',')) { if ($n.Trim()) { $ports += [int]$n.Trim() } }
		}
	}
	@($ports)
}

# ───────────────────────── 现状展示 ─────────────────────────

function Show-State {
	Head2 '== 现状 =='
	if ($LocalBin) {
		Say ("二进制      {0}（{1}，sha256 {2}…）" -f $LocalBin, (Binary-Version $LocalBin), (Has-Sha256 $LocalBin).Substring(0, [Math]::Min(12, (Has-Sha256 $LocalBin).Length)))
	} else {
		Say '二进制      没有找到可用的 sol.exe：脚本旁边、当前目录、PATH 里都没有'
	}
	if ($PathSols.Count -gt 0) {
		Say 'PATH 上的 sol.exe：'
		foreach ($p in $PathSols) {
			if ($p -eq $LocalBin) { Say "  $p  ← 生效" } else { Say "  $p" }
		}
	}
	if ($LedgerExists) {
		Say "已安装      $($LedgerJson.installed_version)（由安装脚本安装，$Ledger）"
	} else {
		Say '已安装      无台账（从没被这个脚本装过；下面按你已有的二进制处理）'
		if (Test-Path (Join-Path $env:SystemRoot "System32\Tasks\$TaskName")) {
			Warn2 "有计划任务但没有台账（$TaskName）：不是我装的，我不动它。
      要手工清掉：
        schtasks /Delete /TN $TaskName /F"
		}
	}
	$st = Task-State
	if ($st -eq 'installed') { Say "服务        计划任务 $TaskName 已注册" } else { Say "服务        没有（计划任务 $TaskName 不存在）" }
	if (Test-Path $RunConfig) { Say "运行配置    $RunConfig（sol 会读它）" } else { Say "运行配置    还不存在：$RunConfig" }
	if (-not $IsAdmin) { Say '权限        当前不是管理员：装服务 / 改机器 PATH 那几步会失败（脚本会告诉你）' }
}

function Show-Config {
	Head2 '== 安装配置 =='
	Say $UserConfig
	Say ''
	foreach ($l in (Get-Content $UserConfig)) { Say "  $l" }
	Say ''
	if ($ConfigGenerated) { Say '（刚生成的。改它，然后重新运行脚本；脚本不会覆盖已存在的这份文件。）' }
}

# ───────────────────────── 问答 ─────────────────────────

$script:NoAnswer = $false

function Ask-Yes($prompt, $default) {
	$suffix = if ($default -eq 'y') { '[Y/n]' } else { '[y/N]' }
	$ans = ''
	try { $ans = (Read-Host "$prompt $suffix") } catch { $script:NoAnswer = $true; return $false }
	if ($null -eq $ans) { $script:NoAnswer = $true; return $false }
	$ans = $ans.Trim().ToLower()
	if (-not $ans) { $ans = $default }
	return ($ans -eq 'y' -or $ans -eq 'yes')
}

function Ask-Choice($prompt) {
	$ans = ''
	try { $ans = (Read-Host $prompt) } catch { $script:NoAnswer = $true; return 'n' }
	if ($null -eq $ans) { $script:NoAnswer = $true; return 'n' }
	$ans = $ans.Trim().ToLower()
	if (-not $ans) { return 'apply' }
	$ans
}

# ───────────────────────── 动作清单 ─────────────────────────

$script:Actions = @()
function Act($kind, $what, $cmd) {
	$line = "[$kind] $what"
	if ($cmd) { $line += "`n        `$ $cmd" }
	$script:Actions += $line
}

function Write-Ledger($incomplete, $prevVersion, $prevSha) {
	if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null }
	$sha = Has-Sha256 $DestBin
	$ports = Get-ThresholdPorts
	$obj = [ordered]@{
		schema            = 1
		method            = 'script'
		installed_version = (Binary-Version $DestBin)
		installed_at      = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
		prefix            = $InstallDir
		binary            = $DestBin
		sha256            = $sha
		service           = [ordered]@{
			kind         = 'schtasks'
			name         = $TaskName
			unit_path    = "\\$TaskName"
			created_unit = $script:ServiceChosen
			created_user = $false
			cap          = ''
		}
		firewall_rules    = @()
		config_paths      = @($RunConfig)
		log_paths         = @()
		previous          = [ordered]@{ installed_version = $prevVersion; sha256 = $prevSha }
		incomplete        = $incomplete
	}
	$obj | ConvertTo-Json -Depth 5 | Set-Content -Path $Ledger -Encoding UTF8
}

function Append-History($what) {
	$block = @("== $((Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')) $what ==") + $script:Actions
	if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null }
	Add-Content -Path $History -Value $block -Encoding UTF8
	$script:Actions = @()
}

# ───────────────────────── 预检 / 装 / 卸 ─────────────────────────

function Invoke-Precheck {
	if (-not $LocalBin) { return $true }
	if (-not (Test-Path $RunConfig)) {
		Warn2 "运行配置 $RunConfig 还不存在，没法预检；服务起来后会立刻退出（sol 拒绝在没有规则时启动）"
		return $false
	}
	$errFile = [System.IO.Path]::GetTempFileName()
	try {
		$p = Start-Process -FilePath $LocalBin -ArgumentList ($ArgsList + '--dry-run') -PassThru -RedirectStandardError $errFile -WindowStyle Hidden
		Start-Sleep -Seconds 3
		if ($p.HasExited) {
			$err = (Get-Content -Path $errFile -ErrorAction SilentlyContinue | Select-Object -First 3) -join "`n"
			Say '预检        被拒绝：'
			Say ($err -split "`n" | ForEach-Object { "            $_" } | Out-String)
			return $false
		}
		Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
		Say '预检        通过（sol 用这份配置起来了 3 秒，匹配了也不会真做事：listen 是 dry-run）'
		return $true
	} finally {
		Remove-Item $errFile -ErrorAction SilentlyContinue
	}
}

$script:ServiceChosen = $false

function Invoke-Install {
	if (-not $LocalBin) { Die '没有可用的 sol.exe：把它放在脚本旁边或 PATH 里，再运行一次' }
	$newSha = Has-Sha256 $LocalBin

	Write-Ledger $true $LedgerJson.installed_version $LedgerJson.sha256
	Act 'user' '写安装台账（先写 incomplete，中途失败也留痕）' ''

	# 你说"应用"但什么都没变 → 不折腾
	if ($LedgerExists -and $newSha -eq $LedgerJson.sha256) {
		$taskOk = ((Task-State) -eq 'installed') -or (-not $script:ServiceChosen)
		if ($taskOk) {
			Say ''
			Say "已是最新，无需操作：二进制没变（sha256 $newSha 与台账一致），计划任务与配置没变。"
			$script:Noop = $true
			return
		}
	}

	if (-not (Invoke-Precheck)) { Die '预检没通过，什么都没动。改完配置再运行一次。' }

	# Windows 陷阱：正在运行的 exe 覆盖不了 → 先结束任务
	if ((Task-State) -eq 'installed') {
		schtasks /End /TN $TaskName 2>$null | Out-Null
		Act 'admin' '结束计划任务（运行中的 exe 有文件锁）' "schtasks /End /TN $TaskName"
		Start-Sleep -Seconds 1
	}

	if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null }
	$same = (Test-Path $DestBin) -and ((Has-Sha256 $DestBin) -eq $newSha)
	if ($same) {
		Say '二进制      已经就是这份（sha256 相同），不动'
	} else {
		if (Test-Path $DestBin) {
			Copy-Item -Path $DestBin -Destination "$DestBin.bak" -Force
			Act 'admin' '保留旧二进制（回滚用）' "Copy-Item $DestBin $DestBin.bak"
			Say ("二进制      换版本：{0} → {1}" -f (Binary-Version $DestBin), (Binary-Version $LocalBin))
		}
		Copy-Item -Path $LocalBin -Destination $DestBin -Force
		Act 'admin' '放置二进制' "Copy-Item $LocalBin $DestBin"
	}

	# 机器 PATH（只加一次）
	$machinePath = [Environment]::GetEnvironmentVariable('PATH', 'Machine')
	if ($machinePath -notlike "*$InstallDir*") {
		[Environment]::SetEnvironmentVariable('PATH', "$machinePath;$InstallDir", 'Machine')
		Act 'admin' '把落点目录加进机器 PATH' "[Environment]::SetEnvironmentVariable('PATH', '...;$InstallDir', 'Machine')"
	}

	if ($script:ServiceChosen) { Register-SolService }

	# 计划任务不收集 stdout：审计日志只能靠 sol 自己写文件，配置里没写就提醒一句。
	if ($script:ServiceChosen -and (Test-Path $RunConfig)) {
		$hasFileLog = (Get-Content $RunConfig) | Where-Object { $_ -match 'output:\s*file' }
		if (-not $hasFileLog) {
			Warn2 "计划任务不收集 stdout：$RunConfig 里建议写 logging: { output: file, file: C:\ProgramData\sol\sol.log }，否则审计记录无处可去。"
		}
	}

	# 防火墙（按运行配置里的端口；读不到就不猜）
	$ports = Get-ThresholdPorts
	if ($ports.Count -gt 0) {
		$existing = Get-NetFirewallRule -DisplayName 'sol (WoL)' -ErrorAction SilentlyContinue
		if (-not $existing) {
			try {
				New-NetFirewallRule -DisplayName 'sol (WoL)' -Direction Inbound -Protocol UDP -LocalPort $ports -Action Allow | Out-Null
				Act 'admin' "放行 UDP 端口（$($ports -join ','))" "New-NetFirewallRule -DisplayName 'sol (WoL)' -Direction Inbound -Protocol UDP -LocalPort $($ports -join ',') -Action Allow"
			} catch { Warn2 "加防火墙规则失败（不是致命，但魔法包可能进不来）：$_" }
		}
	} else {
		Warn2 "读不到 $RunConfig 里的端口，没加防火墙规则。要放行：New-NetFirewallRule -DisplayName 'sol (WoL)' -Direction Inbound -Protocol UDP -LocalPort <端口> -Action Allow"
	}

	Write-Ledger $false $LedgerJson.installed_version $LedgerJson.sha256
	Act 'user' '更新安装台账（incomplete=false）' ''
	Append-History 'install'
}

function Register-SolService {
	$tr = '"' + $DestBin + '" ' + (($ArgsList | ForEach-Object { if ($_ -match '\s') { '"' + $_ + '"' } else { $_ } }) -join ' ')
	schtasks /Create /TN $TaskName /TR $tr /SC ONSTART /RU SYSTEM /RL HIGHEST /F 2>$null | Out-Null
	if ($LASTEXITCODE -ne 0) { Warn2 "schtasks /Create 失败（要管理员）。手动：schtasks /Create /TN $TaskName /TR $tr /SC ONSTART /RU SYSTEM /RL HIGHEST /F" }
	schtasks /Run /TN $TaskName 2>$null | Out-Null
	Act 'admin' '注册并启动计划任务' "schtasks /Create /TN $TaskName /TR `"$tr`" /SC ONSTART /RU SYSTEM /RL HIGHEST /F; schtasks /Run /TN $TaskName"
}

function Invoke-Uninstall {
	if (-not $LedgerExists) {
		Die "没有台账（$Ledger），拒绝瞎删。
能看到的是：$(if ($LocalBin) { "二进制 $LocalBin；" })$(if ((Task-State) -eq 'installed') { "计划任务 $TaskName；" })
手工删除：schtasks /Delete /TN $TaskName /F"
	}
	schtasks /Delete /TN $TaskName /F 2>$null | Out-Null
	Act 'admin' '删除计划任务' "schtasks /Delete /TN $TaskName /F"
	$rule = Get-NetFirewallRule -DisplayName 'sol (WoL)' -ErrorAction SilentlyContinue
	if ($rule) {
		Remove-NetFirewallRule -DisplayName 'sol (WoL)' -ErrorAction SilentlyContinue
		Act 'admin' '删防火墙规则' "Remove-NetFirewallRule -DisplayName 'sol (WoL)'"
	}
	$machinePath = [Environment]::GetEnvironmentVariable('PATH', 'Machine')
	if ($machinePath -like "*$InstallDir*") {
		$newPath = (($machinePath -split ';') | Where-Object { $_ -and $_ -ne $InstallDir }) -join ';'
		[Environment]::SetEnvironmentVariable('PATH', $newPath, 'Machine')
		Act 'admin' '从机器 PATH 撤下落点目录' "[Environment]::SetEnvironmentVariable('PATH', '...', 'Machine')"
	}
	$bin = if ($LedgerJson.binary) { $LedgerJson.binary } else { $DestBin }
	Remove-Item -Path $bin, "$bin.bak" -Force -ErrorAction SilentlyContinue
	Act 'admin' '删二进制与 sol.bak' "Remove-Item $bin, $bin.bak"

	$script:DelConfigs = $false
	if (Ask-Yes '配置文件也一起删掉吗？' 'n') { $script:DelConfigs = $true }
	if ($script:DelConfigs) {
		Remove-Item -Path $UserConfig -Force -ErrorAction SilentlyContinue
		Remove-Item -Path $RunConfig -Force -ErrorAction SilentlyContinue
		Act 'user' '按你的选择删配置' "Remove-Item $UserConfig, $RunConfig"
	} else {
		Say "配置留着：$UserConfig、$RunConfig"
		Act 'user' '保留配置（未删）' ''
	}
	Append-History 'uninstall'
	Remove-Item -Path $Ledger -Force -ErrorAction SilentlyContinue
	Act 'admin' '删台账' "Remove-Item $Ledger"
}

# ───────────────────────── 报告 ─────────────────────────

function Show-Report($what) {
	switch ($what) {
		'install' {
			Head2 '== 完成 =='
			Say "$(Binary-Version $DestBin) -> $DestBin"
			if (Test-Path "$DestBin.bak") { Say "（旧版本留在 $DestBin.bak，回滚就是把它换回去）" }
			Say ''
			Say "安装配置   $UserConfig        改这里，然后重跑脚本"
			Say "运行配置   $RunConfig$(if (-not (Test-Path $RunConfig)) { '（还不存在；示例见 README Quick start）' })"
			Say "台账       $Ledger"
			Say "历史       $History（完整动作清单，带等价命令）"
			Say ''
			Say '校验'
			Say "  $DestBin --version   -> $(Binary-Version $DestBin)"
			if ($script:ServiceChosen) { Say "  schtasks /Query /TN $TaskName   -> $(Task-State)" }
			Say ''
			Say '接下来'
			Say "  schtasks /Query /TN $TaskName /V /FO LIST"
			Say '  重跑安装脚本可以随时看现状（它会先报再问；答 n 退出，什么都不改）'
		}
		'uninstall' {
			Head2 '== 已卸载 =='
			Say "服务       $(Task-State)（计划任务 $TaskName）"
			Say "二进制     $(if (Test-Path $DestBin) { "还在：$DestBin" } else { "已删除：$DestBin" })"
			if ($script:DelConfigs) { Say '配置       已按你的选择删除' } else {
				Say "配置       保留：$UserConfig、$RunConfig"
				Say "           要删：Remove-Item $UserConfig, $RunConfig"
			}
			Say "历史       $History"
		}
		'none' {
			Head2 '== 什么都没做 =='
			Say "配置在那里：$UserConfig"
			Say '改完重跑脚本即可；这次的没执行，也就没写历史。'
		}
	}
}

# ───────────────────────── 主流程 ─────────────────────────

Show-State
Show-Config

$script:Noop = $false

if ($LedgerExists) {
	$choice = Ask-Choice "检测到已安装 $($LedgerJson.installed_version)（计划任务 $(Task-State)）。
请选择：[回车 = 按配置应用 / u = 卸载 / r = 重新生成配置 / n = 退出]"
	switch ($choice) {
		'u' { Invoke-Uninstall; Show-Report 'uninstall' }
		'r' {
			Remove-Item -Path $UserConfig -Force -ErrorAction SilentlyContinue
			Write-UserConfig
			Say "已重新生成：$UserConfig（重新运行脚本就会按它执行）"
		}
		'n' { Show-Report 'none' }
		default {
			$script:ServiceChosen = ($LedgerJson.service.created_unit -eq $true)
			Invoke-Install
			if ($script:Noop) { Say '（什么都没改。想强制重写计划任务就选 r 重新生成配置，或先卸载。）' }
			else { Show-Report 'install' }
		}
	}
	exit 0
}

if ($script:NoAnswer) {
	Say ''
	Say '没有终端：只生成了配置，没有执行任何操作。'
	exit 0
}

$script:ServiceChosen = Ask-Yes '把 sol 注册成开机启动的计划任务（后台常驻）？' 'y'
if (Test-Path $RunConfig) {
	Say "运行配置   存在：$RunConfig"
} else {
	Say "运行配置   不存在：$RunConfig"
	Say '           服务需要它，否则 sol 会拒绝启动（no rules configured）。最小示例：'
	Say '             version: 1'
	Say '             rules:'
	Say '               - match: { ports: [10010], content: { kind: none } }'
	Say '                 action: noop'
}
if (-not (Ask-Yes '执行吗？' 'y')) { Show-Report 'none'; exit 0 }

Invoke-Install
if ($script:Noop) { Say '（什么都没改。想强制重写计划任务就选 r 重新生成配置，或先卸载。）' }
else { Show-Report 'install' }