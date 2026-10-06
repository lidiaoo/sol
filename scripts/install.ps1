# sol 安装脚本（Windows）—— 零参数。
#
# 与 scripts/install.sh 是同一份契约（docs/install-design.md）：认出现状 → 生成/读取"怎么跑"的
# 配置（执行目录下的 install.yaml，里面只有 run.args；同目录再生成一份最小 sol.yaml）→ 展示 → 问一句 → 才动手。
#
#   powershell -ExecutionPolicy Bypass -File install.ps1     或双击 install.cmd
#   没有终端（管道喂答案）时只生成配置，不执行任何操作
#
# ⚠ 证据强度：本机没有 Windows 实机，这份脚本**只做过构造级校验（逐行审查）**，未在真机上跑过。
#    设计文档 §10 要求由 .github/workflows/install-smoke.yml 的 windows matrix 补上真机证据。
#
# ⚠ 升权重跑时带的内部参数必须是 PowerShell 原生写法（-SolAction install，不是 --sol-action=install）：
#   `powershell -File script.ps1 --sol-action=install` 会被当成参数名，子进程直接死在这一行。
#   用户不需要、也不应该记这些；正常执行时一个参数都不用给。
[CmdletBinding()]
param(
	[string]$SolAction = '',
	[string]$SolService = '',
	[string]$SolRunDir = '',
	[string]$SolUnitName = '',
	[string]$SolRoot = ''
)

# 环境变量（都有默认值；测试与多实例用）：
#   SOL_INSTALL_ROOT  把落点挂到另一个目录下（默认 C:\ProgramData）
#   SOL_UNIT_NAME     计划任务名（默认 sol）

$ErrorActionPreference = 'Stop'

$Design = 'docs/install-design.md'

function Say($msg) { Write-Host $msg }
function Head2($msg) { Write-Host ""; Write-Host $msg -ForegroundColor White }
function Warn2($msg) { Write-Warning $msg }
function Die($msg) { Write-Error $msg; exit 1 }
# .NET 里明确"UTF-8 且不带 BOM"。PowerShell 5.1 的 -Encoding UTF8 是带 BOM 的：
# 生成的 YAML/JSON 会被塞一个 BOM，而 .ps1 自身若没 BOM，5.1 又会按 ANSI/GBK 读它（中文变乱码、
# 甚至解析失败）。读写两头都用这里。
function Write-Utf8NoBom([string]$Path, $Lines) {
	$enc = New-Object System.Text.UTF8Encoding($false)
	[IO.File]::WriteAllLines($Path, [string[]]$Lines, $enc)
}
function Add-Utf8NoBom([string]$Path, [string]$Text) {
	$enc = New-Object System.Text.UTF8Encoding($false)
	[IO.File]::AppendAllText($Path, $Text, $enc)
}

# ───────────────────────── 平台与路径 ─────────────────────────

if ($env:SOL_INSTALL_ROOT) { $Root = $env:SOL_INSTALL_ROOT } else { $Root = 'C:\ProgramData' }
if ($env:SOL_UNIT_NAME) { $TaskName = $env:SOL_UNIT_NAME } else { $TaskName = 'sol' }

$InstallDir = Join-Path $Root 'sol'
$DestBin = Join-Path $InstallDir 'sol.exe'
$Ledger = Join-Path $InstallDir 'install.json'
$History = Join-Path $InstallDir 'install.log'

# 生成的文件就放在**你执行脚本的那个目录**：.\install.yaml 与 .\sol.yaml。
# 服务定义里用绝对路径，所以不踩"服务有自己的家目录"那个坑。
# 内部参数（UAC 升权重跑时把答案带过来；用户不需要、也不应该记这些）。
# 为什么不用环境变量：Start-Process -Verb RunAs 经过 shell 提权，环境不保证原样继承。
# 脚本是文件还是喂进来的（`irm … | iex`）：后者没有 $PSCommandPath，
# 也就没法用 UAC 重跑自己——那种情况下要明说，而不是装到一半失败。
$Piped = [string]::IsNullOrEmpty($PSCommandPath)

$ActionArg = $SolAction
$ServiceArg = $SolService
$RunDirArg = $SolRunDir
if ($SolUnitName) { $env:SOL_UNIT_NAME = $SolUnitName }
if ($SolRoot) { $env:SOL_INSTALL_ROOT = $SolRoot }
foreach ($a in $args) {
	if ($a -like '--sol-action=*') { $ActionArg = $a -replace '^--sol-action=', '' }
	elseif ($a -like '--sol-service=*') { $ServiceArg = $a -replace '^--sol-service=', '' }
	elseif ($a -like '--sol-run-dir=*') { $RunDirArg = $a -replace '^--sol-run-dir=', '' }
	elseif ($a -like '--sol-unit-name=*') { $env:SOL_UNIT_NAME = $a -replace '^--sol-unit-name=', '' }
	elseif ($a -like '--sol-root=*') { $env:SOL_INSTALL_ROOT = $a -replace '^--sol-root=', '' }
	else { Die "这个脚本不接受参数（$a）。所有选择都在生成的配置文件和问答里。" }
}

if ($RunDirArg) { $RunDir = $RunDirArg } else { $RunDir = (Get-Location).Path }
$UserConfig = Join-Path $RunDir 'install.yaml'
$DesktopConfig = Join-Path $RunDir 'sol.yaml'

try {
	$probe = Join-Path $RunDir '.sol-write-probe'
	Set-Content -Path $probe -Value 'x' -ErrorAction Stop
	Remove-Item $probe -Force
} catch {
	Die "在这个目录里写不了文件：$UserConfig 与 $DesktopConfig 要生成在这儿。换一个你有写权限的目录再跑一次。"
}

$IsAdmin = $false
try {
	$IsAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
} catch {
	# 极少数环境里这个探测本身会抛（受限容器、非 Windows 的 pwsh）。别让它把整个脚本带走。
	$IsAdmin = $false
}

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
	try { $LedgerJson = Get-Content -Raw -Encoding UTF8 $Ledger | ConvertFrom-Json } catch { $LedgerJson = $null }
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
	# 先在数组外拼好这一行：数组字面量里的字符串拼接会被 PowerShell 拆成多个元素，
	# 结果 install.yaml 被写成好几行（`args: []` 一行、参数一行、`]` 一行），脚本自己都读不懂。
	$argsLine = '  args: [' + ($DefaultArgs -join ', ') + ']'
	$lines = @(
		'# 由 install.ps1 生成：sol 怎么跑。改这里，然后重新运行一遍脚本。',
		'run:',
		$argsLine
	)
	Write-Utf8NoBom $UserConfig $lines
}

function Read-Args {
	$out = @()
	# 注意：调用处必须 @(...) 包一层——PowerShell 会把单元素数组拆成标量。
	foreach ($line in (Get-Content -Encoding UTF8 $UserConfig)) {
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

# sol.yaml：最小可用的一份（一条 noop 规则，只记日志不做事）。没有才写，绝不覆盖。
$RuntimeConfigGenerated = $false
function Ensure-RuntimeConfig {
	if (Test-Path $RunConfig) { return }
	$lines = @(
		'# 由安装脚本生成的最小配置：一条 noop 规则——匹配到只记日志，什么也不做。',
		'# 端口、动作按需改；改完重跑安装脚本（或重启计划任务）即可。',
		'version: 1',
		'rules:',
		'  - match: { ports: [10010], content: { kind: none } }',
		'    action: noop'
	)
	if (-not (Test-Path $InstallDir)) { } # 配置跟执行目录走，不动 ProgramData
	Write-Utf8NoBom $RunConfig $lines
	$script:RuntimeConfigGenerated = $true
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
	# 行内（`- match: { ports: [10010] }`）和块写法都要认：只认行首会让行内写法静默失效，
	# 于是防火墙规则没加、包被挡在外面——"装完了但收不到魔法包"就是这个原因。
	foreach ($line in (Get-Content -Encoding UTF8 $RunConfig)) {
		foreach ($m in [regex]::Matches($line, 'ports:\s*\[([0-9,\s]*)\]')) {
			foreach ($n in ($m.Groups[1].Value -split ',')) { if ($n.Trim()) { $ports += [int]$n.Trim() } }
		}
		if ($line -match '^\s*-\s*(\d+)\s*$') { $ports += [int]$Matches[1] }
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
	if (Test-Path $RunConfig) {
		if ($script:RuntimeConfigGenerated) { Say "运行配置    $RunConfig（刚生成的最小配置：一条 noop 规则，按需改）" }
		else { Say "运行配置    $RunConfig（sol 会读它）" }
	} else { Say "运行配置    还不存在：$RunConfig" }
	if ($env:SOL_INSTALL_ROOT) { Say "权限        普通用户 + SOL_INSTALL_ROOT=$env:SOL_INSTALL_ROOT（自己的地盘，不会升权）" }
	elseif (-not $IsAdmin) { Say '权限        当前不是管理员：轮到需要权限的步骤时，会用 UAC 以管理员身份重跑一遍自己（不会再问一遍）' }
}

function Show-Config {
	Head2 '== 安装配置 =='
	Say $UserConfig
	Say ''
	foreach ($l in (Get-Content -Encoding UTF8 $UserConfig)) { Say "  $l" }
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
	Write-Utf8NoBom $Ledger ($obj | ConvertTo-Json -Depth 5)
}

function Append-History($what) {
	$block = @("== $((Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')) $what ==") + $script:Actions
	if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null }
	Add-Utf8NoBom $History $block
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
	# -WindowStyle 只在 Windows 上支持（受限环境/非 Windows 的 pwsh 会直接抛），
	# 而 Start-Process 本身也可能被拦（杀软、SmartScreen）。这步不该把整个安装带走。
	$spArgs = @{
		FilePath = $LocalBin
		ArgumentList = ($ArgsList + '--dry-run')
		PassThru = $true
		RedirectStandardError = $errFile
	}
	# $IsWindows 是 PowerShell 7+ 才有的自变量；5.1 里它是 $null，会被当成$false。
	if ((Get-Variable -Name IsWindows -ErrorAction SilentlyContinue -ValueOnly) -or $PSVersionTable.PSEdition -eq 'Desktop') { $spArgs['WindowStyle'] = 'Hidden' }
	try {
		$p = Start-Process @spArgs
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
	} catch {
		Warn2 "预检没能起起来 sol（$_）。没预检通过就不动手，所以此次什么都没改。"
		return $false
	} finally {
		Remove-Item $errFile -ErrorAction SilentlyContinue
	}
}

$script:ServiceChosen = $false

# ───────────────────────── 升权（UAC）─────────────────────────

# 与 install.sh 同一套规矩：需要管理员的事，在动手之前一次性升权，答案用参数带过去，
# 不再问第二遍；SOL_INSTALL_ROOT（自选的根）下永不升权；升权失败就明确报错，不装到一半。
function Escalate-IfNeeded([string]$action) {
	if ($IsAdmin) { return }
	if ($env:SOL_INSTALL_ELEVATED -eq '1') { return }   # 已经升过一次还是不行：别再套娃
	if ($env:SOL_INSTALL_ROOT) { return }               # 自选的根：自己的地盘
	$needs = $false
	if ($action -eq 'uninstall') { $needs = $true }
	else {
		if ($script:ServiceChosen) { $needs = $true }
		if (-not (Test-Path $InstallDir)) { $needs = $true }
	}
	if (-not $needs) { return }

	Say ''
	Say '这一步要写 ProgramData、注册计划任务、改机器 PATH，需要管理员。'
	Say '我用 UAC 以管理员身份重新执行一遍自己：同一个目录、你刚才的答案带过去，不会再问一遍。'

	if ($Piped) {
		Die "这一步需要管理员，但脚本是管道执行（irm … | iex）的：没有文件可以重新以管理员身份运行。请先存成文件再跑：`n  irm <install.ps1 的地址> -OutFile install.ps1; powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1`n  或者直接右键 install.cmd 选“以管理员身份运行”。"
	}

	$hostExe = (Get-Process -Id $PID).Path
	if (-not $hostExe) { $hostExe = Join-Path $PSHOME 'powershell.exe' }
	$svc = if ($script:ServiceChosen) { 'yes' } else { 'no' }
	# 带空格的取值要自己加引号：PowerShell 拼命令行时不会替你加，路径里有空格就会把参数切断。
	$rawArgs = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $PSCommandPath,
		'-SolAction', $action, '-SolService', $svc, '-SolRunDir', $RunDir, '-SolUnitName', $TaskName)
	if ($env:SOL_INSTALL_ROOT) { $rawArgs += @('-SolRoot', $env:SOL_INSTALL_ROOT) }
	$argList = @($rawArgs | ForEach-Object { if ($_ -match '\s') { '"' + $_ + '"' } else { $_ } })

	$env:SOL_INSTALL_ELEVATED = '1'
	try {
		# UAC 子进程有自己的窗口，它的输出在父进程里一个字都看不到——失败也是静悄悄的，
		# 而这正是"服务没注册"最难查的地方。子进程把全程写进 transcript，父进程随后打印出来。
		$elevLog = Join-Path $RunDir "elevated-$action.log"
		if (Test-Path $elevLog) { Remove-Item $elevLog -Force -ErrorAction SilentlyContinue }
		$p = Start-Process -FilePath $hostExe -Verb RunAs -PassThru -Wait -WorkingDirectory $RunDir -ArgumentList $argList
		Say ''
		if (Test-Path $elevLog) {
			Say '以管理员身份那一步的输出：'
			foreach ($l in (Get-Content -Encoding UTF8 $elevLog)) { Say "  $l" }
		} else {
			Warn2 "以管理员身份那一步没有留下输出（$elevLog 不存在）。退出码：$($p.ExitCode)"
		}
		exit $p.ExitCode
	} catch {
		Die "这一步需要管理员权限，但 UAC 提权没成功（被拒绝或不可用）。请右键 install.cmd 选“以管理员身份运行”，或在管理员 PowerShell 里再跑一次。原因：$_"
	}
}

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

	# 别人的计划任务不覆盖：卸载那条"只删自己建的"规矩，安装这边同样成立。
	if ($script:ServiceChosen -and ((Task-State) -eq 'installed') -and ($LedgerJson.service.created_unit -ne $true)) {
		Die "已经有一份计划任务 $TaskName，但它不是这个脚本建的：我不覆盖别人的任务定义。
要么手工清掉：schtasks /Delete /TN $TaskName /F
要么换个名字装：`$env:SOL_UNIT_NAME='sol-mine'; .\install.ps1"
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
		$hasFileLog = (Get-Content -Encoding UTF8 $RunConfig) | Where-Object { $_ -match 'output:\s*file' }
		if (-not $hasFileLog) {
			Warn2 "计划任务不收集 stdout：$RunConfig 里建议写 logging: { output: file, file: $($InstallDir)\sol.log }，否则审计记录无处可去。"
		}
	}

	# 防火墙（按运行配置里的端口；读不到就不猜）。缺 NetSecurity 模块（Server Core 之类）或任何
	# 失败都只提醒：防火墙是帮手，不是目的，绝不能把整个安装带走。
	try {
	$ports = Get-ThresholdPorts
	if (-not (Get-Command New-NetFirewallRule -ErrorAction SilentlyContinue)) {
		Warn2 '这台机器上没有 NetSecurity 模块，没加防火墙规则。要放行：New-NetFirewallRule -DisplayName "sol (WoL)" -Direction Inbound -Protocol UDP -LocalPort <端口> -Action Allow'
	} elseif ($ports.Count -gt 0) {
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
	} catch { Warn2 "防火墙那一步失败（不致命，但魔法包可能进不来）：$_" }

	Write-Ledger $false $LedgerJson.installed_version $LedgerJson.sha256
	Act 'user' '更新安装台账（incomplete=false）' ''
	Append-History 'install'
}

function Register-SolService {
	$tr = '"' + $DestBin + '" ' + (($ArgsList | ForEach-Object { if ($_ -match '\s') { '"' + $_ + '"' } else { $_ } }) -join ' ')
	# schtasks 自己的报错是"为什么没注册上"的唯一线索：绝不能吞掉它。
	$out = & schtasks /Create /TN $TaskName /TR $tr /SC ONSTART /RU SYSTEM /RL HIGHEST /F 2>&1
	$code = $LASTEXITCODE
	if ($code -ne 0) {
		Warn2 "schtasks /Create（带 /RL HIGHEST）失败，退出码 $code：$out"
		# 部分 Windows 上 /RL HIGHEST 与 /RU SYSTEM 不能同时给（SYSTEM 本身就是最高权限）。
		$out = & schtasks /Create /TN $TaskName /TR $tr /SC ONSTART /RU SYSTEM /F 2>&1
		$code = $LASTEXITCODE
		if ($code -ne 0) {
			Warn2 "schtasks /Create 仍然失败，退出码 $code：$out`n  手动来一遍：schtasks /Create /TN $TaskName /TR `"$tr`" /SC ONSTART /RU SYSTEM /F"
			return
		}
		Say '（去掉 /RL HIGHEST 后注册成功：/RU SYSTEM 的任务本身就是最高权限）'
	}
	$run = & schtasks /Run /TN $TaskName 2>&1
	if ($LASTEXITCODE -ne 0) { Warn2 "schtasks /Run 失败，退出码 $LASTEXITCODE：$run（任务已注册，只是这次没立刻起来；重启后会自动跑）" }
	Act 'admin' '注册并启动计划任务' "schtasks /Create /TN $TaskName /TR `"$tr`" /SC ONSTART /RU SYSTEM /F; schtasks /Run /TN $TaskName"
}

function Invoke-Uninstall {
	if (-not $LedgerExists) {
		Die "没有台账（$Ledger），拒绝瞎删。
能看到的是：$(if ($LocalBin) { "二进制 $LocalBin；" })$(if ((Task-State) -eq 'installed') { "计划任务 $TaskName；" })
手工删除：schtasks /Delete /TN $TaskName /F"
	}
	schtasks /Delete /TN $TaskName /F 2>$null | Out-Null
	Act 'admin' '删除计划任务' "schtasks /Delete /TN $TaskName /F"
	$rule = $null
	try { $rule = Get-NetFirewallRule -DisplayName 'sol (WoL)' -ErrorAction SilentlyContinue } catch { }
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
			Say "运行配置   $RunConfig$(if ($script:RuntimeConfigGenerated) { '（刚生成的最小配置：一条 noop 规则，按需改）' })"
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

# 已经被 UAC 升权重跑（带答案进来的）：不重复展示、也不再问，直接执行。
$ElevatedAction = $ActionArg
if (-not $ElevatedAction) { $ElevatedAction = $env:SOL_INSTALL_ACTION }
if ($ElevatedAction) {
	$env:SOL_INSTALL_ELEVATED = '1'
	$svc = $ServiceArg
	if (-not $svc) { $svc = $env:SOL_INSTALL_SERVICE }
	$script:ServiceChosen = ($svc -eq 'yes')
	# 父进程看不见这个窗口：全程写进 transcript，交给它打印。
	$elevLog = Join-Path $RunDir "elevated-$ElevatedAction.log"
	$transcript = $false
	try {
		Start-Transcript -Path $elevLog -Append -Force | Out-Null
		$transcript = $true
	} catch { }
	try {
		Say "（已用管理员权限重跑：$ElevatedAction）"
		switch ($ElevatedAction) {
			'install' { Invoke-Install; if ($script:Noop) { Say '（什么都没改。）' } else { Show-Report 'install' } }
			'uninstall' { Invoke-Uninstall; Show-Report 'uninstall' }
			default { Die "未知的 SOL_INSTALL_ACTION：$ElevatedAction" }
		}
	} catch {
		Say "出错了：$_"
		Say $_.ScriptStackTrace
	} finally {
		if ($transcript) { try { Stop-Transcript | Out-Null } catch { } }
	}
	exit 0
}

Ensure-RuntimeConfig

Show-State
Show-Config

$script:Noop = $false

if ($LedgerExists) {
	$choice = Ask-Choice "检测到已安装 $($LedgerJson.installed_version)（计划任务 $(Task-State)）。
请选择：[回车 = 按配置应用 / u = 卸载 / r = 重新生成配置 / n = 退出]"
	switch ($choice) {
		'u' { Escalate-IfNeeded 'uninstall'; Invoke-Uninstall; Show-Report 'uninstall' }
		'r' {
			Remove-Item -Path $UserConfig -Force -ErrorAction SilentlyContinue
			Write-UserConfig
			Say "已重新生成：$UserConfig（重新运行脚本就会按它执行）"
		}
		'n' { Show-Report 'none' }
		default {
			$script:ServiceChosen = ($LedgerJson.service.created_unit -eq $true)
			# 台账说没建过任务，而任务也确实不在（可能上次没跑完）：再问一次，
			# 否则"我想装服务"在这条路上永远没机会说出口。
			if (-not $script:ServiceChosen -and -not $env:SOL_INSTALL_ROOT -and (Task-State) -ne 'installed') {
				$script:ServiceChosen = Ask-Yes '把 sol 注册成开机启动的计划任务（后台常驻）？' 'y'
			}
			Escalate-IfNeeded 'install'
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
if (-not (Ask-Yes '执行吗？' 'y')) { Show-Report 'none'; exit 0 }

Escalate-IfNeeded 'install'
Invoke-Install
if ($script:Noop) { Say '（什么都没改。想强制重写计划任务就选 r 重新生成配置，或先卸载。）' }
else { Show-Report 'install' }