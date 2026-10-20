# sol 安装脚本（Windows）—— 零参数。
#
# 与 scripts/install.sh 是同一份契约（docs/install-design.md）：认出现状 → 生成/读取"怎么跑"的
# 配置（用户目录 %USERPROFILE%\sol\install.yaml，里面只有 run.args；同目录再生成一份 sol.yaml）→ 展示 → 问一句 → 才动手。
#
#   powershell -ExecutionPolicy Bypass -File install.ps1     或双击 install.cmd
#   文件必须是 **UTF-8 带 BOM**：powershell.exe 5.1 会把不带 BOM 的脚本按 ANSI(GBK) 读，中文全乱码。
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
	# 卸载时"配置文件也一起删掉吗"的答案：在父进程问，带进升权子进程（子进程有自己的窗口，
	# 在它里面提问人可能看不见，父进程却在等它——那就是"脚本没反应"）。环境变量走 UAC 不可靠（见文件开头）。
	[string]$SolDelConfigs = '',
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
# 子进程的窗口可能根本不在用户眼前（真机上就是"蓝窗口一片空白"）。所以子进程每说一句都顺手追加
# 进一个**行式**进度文件：父进程在等它的同时把新行念出来，用户在眼前这个窗口里就能看见它走到哪一步。
function Note-Progress($msg) {
	if (-not $script:ProgressFile) { return }
	try { Add-Content -Path $script:ProgressFile -Value ("[{0:HH:mm:ss}] {1}" -f (Get-Date), $msg) -Encoding UTF8 -ErrorAction Stop } catch { }
}
function Say($msg) { Note-Progress $msg; Write-Host $msg }
function Head2($msg) { Write-Host ""; Write-Host $msg -ForegroundColor White }
function Warn2($msg) { Note-Progress "警告: $msg"; Write-Warning $msg }
function Die($msg) { Write-Host $msg -ForegroundColor Red; exit 1 }
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
# $Root / $TaskName / $InstallDir / $DestBin / $Ledger / $History / $LogFile / $TaskCmd 都定义在下面的
# 参数解析之后——-SolRoot/--sol-root 要先读进来，才能决定它们落在哪儿。
# 两份配置都落在**当前用户的目录**里，三端一个约定（Linux/macOS 是 ~/.config/sol/install.yaml
# 与 ~/sol/sol.yaml，Windows 是 %USERPROFILE%\sol\ 下这两个文件）：不需要管理员权限就能生成、
# 查看、修改，也不随"你在哪个目录运行脚本"漂移（早先按执行目录走，在下载目录里跑一次就会多出一份）。
# 服务定义里用的是绝对路径，所以不踩"服务有自己的家目录"那个坑——计划任务以 SYSTEM 跑，家目录是 system32。
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
# 参数解析完才定根目录与任务名（-SolRoot/--sol-root 要能改掉它们）。
# 落点里那几个固定名字只在这里写一次：报告、报错文案、计划任务、打包自检都引用它们，
# 免得同一个 'sol.yaml' 在文件里散成十几处字面量、改一处漏一处。
$DirName = 'sol'                 # 用户目录下那个目录（%USERPROFILE%\sol；设了自选根时是 <根>\home\sol）
$InstallConfigName = 'install.yaml'   # 安装配置："sol 怎么跑"
$RunConfigName = 'sol.yaml'           # 运行配置：sol 自己读的那份
if ($env:SOL_INSTALL_ROOT) { $Root = $env:SOL_INSTALL_ROOT } else { $Root = 'C:\ProgramData' }
if ($env:SOL_UNIT_NAME) { $TaskName = $env:SOL_UNIT_NAME } else { $TaskName = 'sol' }
$InstallDir = Join-Path $Root $DirName
$DestBin = Join-Path $InstallDir 'sol.exe'
$Ledger = Join-Path $InstallDir 'install.json'
$History = Join-Path $InstallDir 'install.log'
$LogFile = Join-Path $InstallDir 'sol.log'
$TaskCmd = Join-Path $InstallDir 'run-sol.cmd'
# 用户名下那个目录：默认 %USERPROFILE%\sol；把落点挂到别的根（SOL_INSTALL_ROOT / --sol-root）时
# 挂在 <根>\home\sol，沙箱与多实例测试不会碰到真实的家目录。
if ($RunDirArg) {
	$RunDir = $RunDirArg
} elseif ($env:SOL_INSTALL_ROOT) {
	$RunDir = Join-Path (Join-Path $Root 'home') $DirName
} else {
	$RunDir = Join-Path $env:USERPROFILE $DirName
}
$UserConfig = Join-Path $RunDir $InstallConfigName
$RunConfig = Join-Path $RunDir $RunConfigName
# 早先的安装把这两份文件放在**执行目录**里；这份只用于把老内容搬过来。
$LegacyUserConfig = Join-Path (Get-Location).Path $InstallConfigName
$LegacyDesktopConfig = Join-Path (Get-Location).Path $RunConfigName
# 运行配置的模板：跟脚本放在一起的那份 sol.yaml（发布包里和 install.ps1 同目录，开发树里在 scripts/ 下）。
# 它是三端共用的一份，注释最全；生成时会就地换成随机令牌。找不到就退回脚本里的内置模板。
$ScriptDirHint = if ($PSScriptRoot) { $PSScriptRoot } else { Split-Path -Parent $PSCommandPath }
$TemplateConfig = ''
foreach ($cand in @((Join-Path $ScriptDirHint $RunConfigName), (Join-Path $ScriptDirHint "scripts\$RunConfigName"))) {
	if (Test-Path -LiteralPath $cand) { $TemplateConfig = $cand; break }
}
# 目录可能还不存在：能直接写就写，写不了再请管理员（计划任务以 SYSTEM 读这份配置，用户目录
# 默认就是"自己读写、其他账户只读"，SYSTEM 读得到）。
function Test-DirWritable([string]$Path) {
	$d = $Path
	while ($d -and -not (Test-Path $d)) { $d = Split-Path -Parent $d }
	if (-not $d) { return $false }
	try {
		$probe = Join-Path $d '.sol-write-probe'
		Set-Content -Path $probe -Value 'x' -ErrorAction Stop
		Remove-Item $probe -Force
		return $true
	} catch { return $false }
}
if (-not (Test-DirWritable $RunDir)) {
	Die "在这个目录里写不了文件：$UserConfig 与 $RunConfig 要生成在这儿。如果这个目录属于别的账户，用管理员身份再跑一次。"
}
$script:Child = $false
$IsAdmin = $false
try {
	$IsAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
} catch {
	# 极少数环境里这个探测本身会抛（受限容器、非 Windows 的 pwsh）。别让它把整个脚本带走。
	$IsAdmin = $false
}
# Windows 上正在运行的 exe 覆盖不了、也删不掉；进程刚被杀掉时文件锁还要一小会儿才松
# （报错是 "Text file busy" 或 access denied）。这类事就重试，而不是赌一次就成功。
function Invoke-WithRetry($what, $scriptBlock, $tries = 10, $delayMs = 500) {
	for ($i = 0; $i -lt $tries; $i++) {
		try {
			& $scriptBlock
			return $true
		} catch {
			if ($i -eq ($tries - 1)) { Warn2 "$what 失败：$_"; return $false }
			Start-Sleep -Milliseconds $delayMs
		}
	}
	return $false
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
# 原生命令（schtasks 之类）往 stderr 写东西时，$ErrorActionPreference='Stop' 会把它当成终止错误——
# 既看不到真实报错，还会把流程带走（真机上就是这样把 /Create 的失败变成了 TerminatingError）。
# 这里临时放宽，并把输出与退出码一起拿回来。
function Invoke-Native([string]$Exe, [string[]]$Argv) {
	$old = $ErrorActionPreference
	$ErrorActionPreference = 'Continue'
	try {
		# `$null |` 是关键，别再删：原生命令（schtasks 的确认提示就是典型）会在 stdin 上等人按键，
		# 真机上那就是"脚本卡死、按回车才继续"，而升权那一步现在是隐藏窗口——用户连那个窗口都看不到，
		# 更按不了。给它们一个空 stdin（立刻 EOF），它们只能取默认值然后返回。
		$out = $null | & $Exe @Argv 2>&1
		return @{ Code = $LASTEXITCODE; Out = (($out | Out-String).Trim()) }
	} catch {
		return @{ Code = -1; Out = "$_" }
	} finally {
		$ErrorActionPreference = $old
	}
}
# 我们自己那份 sol.exe 现在有几个进程在跑（返回 pid 列表）。只认可执行文件路径就是它的：
# 名字叫 sol.exe 的别人的进程与它无关（卸载时乱杀同名进程是另一种事故）。
#
# 为什么不用 Get-Process 的 .Path：计划任务以 SYSTEM 跑，而现状是**未升权**时打印的，那时读
# 更高权限进程的可执行文件路径会失败（异常被吞掉），看起来就是"没有在跑"。CIM 的 Win32_Process
# 任何人都能读，而且带命令行，正好用来认"是不是我们装的那份"。
function Get-RunningSolIds($path) {
	$ids = @()
	if (-not $path) { return $ids }
	# 这里原先是 Get-CimInstance（WMI）。真机上"要停掉正在跑的那份"这一步卡死过——WMI 查询可以无限期
	# 挂住，而 Get-Process 是本地调用，不会。路径读不到时（任务是以 SYSTEM 身份跑的）退化成按名字算，
	# 反正规矩已经定了：叫 sol 的就归这个脚本管。
	foreach ($p in @(Get-Process -Name 'sol' -ErrorAction SilentlyContinue)) {
		$exe = ''
		try { $exe = $p.Path } catch { }
		if ($exe -and ($exe -ieq $path)) { $ids += [int]$p.Id; continue }
		if (-not $exe) { $ids += [int]$p.Id }
	}
	@($ids)
}
# 为什么停：① 卸载要删这个 exe，进程活着文件删不掉（Windows 的文件锁）；② 更要紧的是它一直
# 占着 UDP 端口，下一次安装的预检绑不上，看起来就是"端口占用"。
function Stop-RunningInstances($path, $why) {
	Note-Progress "  查有没有在跑的 sol.exe（$path）"
	$ids = @(Get-RunningSolIds $path)
	if ($ids.Count -eq 0) { Note-Progress '  没有在跑的，跳过'; return }
	Say "  先停掉还在跑的那份（$why）：pid $($ids -join ',')"
	foreach ($procId in $ids) {
		try { Stop-Process -Id $procId -Force -ErrorAction Stop } catch { }
	}
	for ($i = 0; $i -lt 12; $i++) {
		Start-Sleep -Milliseconds 250
		if (@(Get-RunningSolIds $path).Count -eq 0) { break }
	}
	$left = @(Get-RunningSolIds $path)
	if ($left.Count -gt 0) {
		Warn2 "还有 $($left.Count) 个没停掉：pid $($left -join ',')（可能没权限；动手前请确认）"
	} else {
		Act 'admin' "停掉还在跑的 sol.exe（pid $($ids -join ',')）" "Stop-Process -Id $($ids -join ',') -Force"
	}
	Start-Sleep -Milliseconds 500
}
# 预检失败时把刚才停掉的任务按原样起回来（任务定义/配置/二进制都还没动过）。
# SOL_INSTALL_ROOT 下不碰本机计划任务（那是别人的地盘）。
function Restore-SolTask {
	if ($env:SOL_INSTALL_ROOT) { return }
	if ((Task-State) -ne 'installed') { return }
	$r = Invoke-Native 'schtasks' @('/Run', '/TN', $TaskName)
	if ($r.Code -eq 0) { Act 'admin' '把刚才停掉的任务起回来' "schtasks /Run /TN $TaskName" }
}
# v0.0.0-…（去掉 "sol version" 前缀与后面的 sha 括注）：给用户看的版本，不夹带内部串。
function Version-Tag($path) {
	if (-not $path) { return '' }
	((Binary-Version $path) -replace '^.*version ', '') -replace ' .*$', ''
}
# 我们那个计划任务对象（找到就返回它，顺便能读"操作"那一栏）。找不到返回 $null。
#
# 为什么不用 `schtasks /Query /TN sol` 判存在：真机上任务不在时它回的是"拒绝访问"（退出码 1），
# 于是"看不清"被错报成"不存在"（用户当场就说"明明有，提示无"）。列全部任务这一路未升权也能读：
# cmdlet 优先，退路是 `schtasks /Query`（不带 /TN）再按名字精确匹配。
function Get-OurTask {
	if (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue) {
		$t = @(Get-ScheduledTask -ErrorAction SilentlyContinue | Where-Object { $_.TaskName -eq $TaskName -and $_.TaskPath -eq '\' })
		if ($t.Count -gt 0) { return $t[0] }
		return $null
	}
	$r = Invoke-Native 'schtasks' @('/Query')
	if ($r.Code -ne 0) { return $null }
	foreach ($line in ($r.Out -split "`r?`n")) {
		$first = ($line -split '\s{2,}')[0]
		if ($first -and ($first.Trim() -eq $TaskName)) {
			return [pscustomobject]@{ TaskName = $TaskName; State = 'unknown'; Actions = @() }
		}
	}
	return $null
}
# 三态：'installed' / 'absent' / 'unknown'。连任务列表都读不到时宁可说"看不清"，
# 也别把"没权限看"说成"没有"——那会让人得出"任务被删了"的错误结论。
function Task-State {
	# 不是管理员就一律"看不清"：任务是以 SYSTEM 身份注册的，任务文件（C:\Windows\System32\Tasks\<名字>）
	# 的 ACL 只给 SYSTEM 和管理员读；非管理员的枚举会**悄悄跳过**它，于是"读不到"被当成"不存在"——
	# 用户上次就是这么被骗的（真机实测：同一个名字，非管理员查它回"拒绝访问"，查一个真不存在的名字回
	# "系统找不到指定的文件"；而 Get-ScheduledTask 对两者报一模一样的错）。看不清就直说看不清。
	if (-not $IsAdmin) { return 'unknown' }
	if (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue) {
		if (Get-OurTask) { return 'installed' }
		return 'absent'
	}
	$r = Invoke-Native 'schtasks' @('/Query')
	if ($r.Code -ne 0) { return 'unknown' }
	if (Get-OurTask) { return 'installed' }
	return 'absent'
}
# ───────────────────────── 安装配置（只有 run.args）─────────────────────────
$DefaultArgs = @('listen', '--config', $RunConfig)
# 原样拷贝（不重写）一份文件；CJK 路径走 -LiteralPath。
function Copy-ConfigFile([string]$From, [string]$To) {
	$dir = Split-Path -Parent $To
	if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
	Copy-Item -LiteralPath $From -Destination $To -Force -ErrorAction Stop
}
# 从一个 YAML 文件里读 run.args（只认本脚本这种写法：一行 args: [a, b, c]）。模板和用户配置
# 共用它——两边的写法是同一套，不存在"解析模板一套、解析用户另一套"的可能。
# 每个 token 去掉外面的单/双引号，再展开 %VAR%（模板里用 %USERPROFILE% 写路径，换台机器也能用）。
function Convert-ArgsOf([string]$Path) {
	$out = @()
	# 注意：调用处必须 @(...) 包一层——PowerShell 会把单元素数组拆成标量。
	foreach ($line in (Get-Content -LiteralPath $Path -Encoding UTF8)) {
		if ($line -match '^\s*args:\s*(.*)$') {
			$val = $Matches[1].Trim().Trim('[', ']')
			foreach ($tok in ($val -split ',')) {
				$t = $tok.Trim()
				$t = $t -replace '^["'']', '' -replace '["'']$', ''
				$t = [Environment]::ExpandEnvironmentVariables($t)
				if ($t) { $out += $t }
			}
		}
	}
	$out
}
# 本平台的安装配置模板：随包带的那一份（发布包里和 install.ps1 同目录，开发树里在 scripts/ 下）。
# 三个平台各一份、各自维护，脚本只读自己那平台的。
$InstallTemplate = Join-Path $PSScriptRoot 'install-windows.yaml'
if (-not (Test-Path -LiteralPath $InstallTemplate)) {
	$alt = Join-Path $PSScriptRoot 'scripts\install-windows.yaml'
	if (Test-Path -LiteralPath $alt) { $InstallTemplate = $alt }
}
if (-not (Test-Path -LiteralPath $InstallTemplate)) {
	Die "找不到 Windows 安装配置模板 install-windows.yaml（应该跟 install.ps1 放在一起）。重新下载一份发布包再装。"
}
# 模板里的 args 一行是 YAML 流式列表，带引号；解析它和解析用户那份必须是同一套规则，所以共用
# Convert-ArgsOf——模板和用户配置的写法是同一套，没有第二套。模板解析不出来，或 args 跟本脚本
# 算出来的默认值对不上，都算坏包，当场报出来而不是写一份错的配置。
#
# 比的是**标准落点**（%USERPROFILE%\sol\sol.yaml），不是 $DefaultArgs / $RunConfig：那两个会被
# -SolRunDir / -SolRoot 改掉（沙箱、多实例测试），而模板永远写着标准落点。拿被改过的值去比，
# 只要换个目录就会把好包说成"坏包"。标准落点由上面那两个全局名字拼出来，这里不再重写一遍字面量。
$CanonicalRunConfig = Join-Path (Join-Path $env:USERPROFILE $DirName) $RunConfigName
$TemplateArgs = @(Convert-ArgsOf $InstallTemplate)
if (($TemplateArgs -join ' ') -ne "listen --config $CanonicalRunConfig") {
	Die "安装配置模板 $InstallTemplate 里的 args 对不上（读到：$($TemplateArgs -join ' ')；应该是 listen --config $CanonicalRunConfig）。这是坏包，重新下载。"
}
function Write-UserConfig {
	Copy-ConfigFile $InstallTemplate $UserConfig
	# 落点不是标准位置（-SolRoot / -SolRunDir 沙箱、多实例）时，模板里那句 %USERPROFILE%\sol\sol.yaml
	# 展开出来是**真实用户目录**，跟这份配置实际所在的位置对不上——预检当场读不到文件（实测就是这个错：
	# read config C:\Users\jianghh\sol\sol.yaml: The system cannot find the file specified）。
	# 上面那行 Copy 是原样拷贝，所以这里把 args 改写成本机真实的绝对路径：以后无论谁去展开
	# （脚本、cmd、计划任务里的 SYSTEM），指的都是同一份文件，不依赖 %USERPROFILE% 在谁名下。
	# 标准安装时两个路径本来就相等，这句不产生任何改动（Set-Args-Line 里有 -ceq 短路）。
	if ((@(Convert-ArgsOf $UserConfig) -join ' ') -ne "listen --config $RunConfig") {
		Set-Args-Line @('listen', '--config', $RunConfig)
	}
}
# 内容跟"我们现在会生成的那份"是否一样（忽略 CRLF）。老脚本写的注释措辞不同，所以分两步：
# ① 逐字相同；② 去掉注释与空行后只剩 run: 和那一行 args，且 args 与默认值一致。
# 两条都不成立，说明它承载了你的改动（改过 --config、加过别的键），那是真老配置，照搬。
function Test-StockUserConfig([string]$Path) {
	if (-not (Test-Path -LiteralPath $Path)) { return $false }
	$mine = ([IO.File]::ReadAllText((Resolve-Path -LiteralPath $Path), [Text.UTF8Encoding]::new($false))) -replace "`r`n", "`n"
	$stock = ([IO.File]::ReadAllText((Resolve-Path -LiteralPath $InstallTemplate), [Text.UTF8Encoding]::new($false))) -replace "`r`n", "`n"
	if ($mine.TrimEnd() -eq $stock.TrimEnd()) { return $true }
	# ② 措辞可能不同（老脚本写的注释不一样），或者路径已被展开过：只要求去掉注释与空行后
	#    只剩 run: 和那一行 args，且那组 token 规整后与默认值一致。
	$rest = @()
	foreach ($l in ($mine -split "`n")) {
		$l = $l.TrimEnd()
		if (-not $l) { continue }
		if ($l -match '^\s*#') { continue }
		$rest += $l
	}
	if ($rest.Count -ne 2) { return $false }
	if ($rest[0] -notmatch '^\s*run:\s*$') { return $false }
	if ($rest[1] -notmatch '^\s*args:\s*\[.*\]\s*$') { return $false }
	# token 规整与模板共用 Convert-ArgsOf 的同一条规则（去引号 + 展开 %VAR%）。
	# 比的是标准落点 $CanonicalRunConfig：$DefaultArgs 会被 -SolRunDir / -SolRoot 改掉，
	# 拿它去比，沙箱里那份"一字不差的示例"就判不出来，会被当成用户配置搬走。
	return ((Convert-ArgsOf $Path) -join ' ') -eq "listen --config $CanonicalRunConfig"
}
# 只改 install.yaml 里那一行 args:，其余（你自己的注释、别的字段）原样留着。
function Set-Args-Line($argv) {
	# 这是一份"已经存在、而且被用户改过"的配置（没改过的那份上面 Straight 判断早就重写了），
	# 所以除了 args 那一行，其它字节一个都不能动。整份读进来再写回去有两个风险，都被这一段绕开了：
	#   ① 编码：PS 5.1 的 Get-Content 不给 -Encoding 就按系统 ANSI 代码页解码，中文注释会被读坏再写坏；
	#   ② 内容：注释措辞、行尾、结尾换行都会被 WriteAllLines 重新排版一遍，等于把用户的文件洗了一次。
	# 做法：只读那一行、只替换那一行的字节区间，失败就原样不动（宁可留着老取值，也不要写坏文件）。
	$enc = New-Object System.Text.UTF8Encoding($false)
	$old = [System.IO.File]::ReadAllText($UserConfig, $enc)
	$nl = "`n"
	if ($old.Contains("`r`n")) { $nl = "`r`n" }
	$eol = if ($nl -eq "`r`n") { "`r" } else { "" }
	$argsLine = '  args: [' + ($argv -join ', ') + ']'
	# (?m) 下 $ 只认 \n，所以把 CR 单独吃掉再放回去，行尾风格保持不变。
	$pattern = '(?m)^[ \t]*args:[ \t]*\[[^\]\r\n]*\][ \t]*' + $eol + '$'
	$rx = New-Object System.Text.RegularExpressions.Regex($pattern)
	$hit = $rx.Matches($old)
	if ($hit.Count -eq 1) {
		$new = $rx.Replace($old, [System.Text.RegularExpressions.MatchEvaluator] { param($m) $argsLine + $eol }, 1)
	} elseif ($hit.Count -eq 0) {
		# 没有 args 行（理论上进不来：Read-Args 已经拦过），只在末尾补一行。
		$sep = if ($old.EndsWith("`n") -or $old.Length -eq 0) { '' } else { $nl }
		$new = $old + $sep + $argsLine + $nl
	} else {
		Warn2 "$UserConfig 里有多行 args:，不敢猜是哪一行；这次不改它（你的文件保持原样）"
		return
	}
	if ($new -ceq $old) { return }
	Invoke-WithRetry '只改写安装配置里的 args 那一行' { [System.IO.File]::WriteAllText($UserConfig, $new, $enc) } | Out-Null
}
function Read-Args { Convert-ArgsOf $UserConfig }
# 两份配置在同一处（用户目录）；服务认的 sol.yaml：一份开箱即用的配置（纯包关机 / magic+"reboot" 重启 /
# magic+"sleep" 睡眠）。没有才写，绝不覆盖；执行目录里那份（你改过的）会**原样拷过来**，不丢改动。
$RuntimeConfigGenerated = $false
$RuntimeConfigCopied = $false
# 任务注册后复核失败（台账要据此记 incomplete，不能像上次那样报成功）
$script:ServiceVerifyFailed = $false
# 控制面令牌：32 字节随机数的十六进制（64 个字符），和 Linux 那份生成的是同一种东西。
function New-ControlToken {
	$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
	$bytes = New-Object byte[] 32
	$rng.GetBytes($bytes)
	(($bytes | ForEach-Object { '{0:x2}' -f $_ }) -join '')
}
# 那份配置里有一个明文令牌，所以权限只留 SYSTEM 与 Administrators：这两个文件现在在用户目录里，
# 那里的默认 ACL 是"你自己读写、同机其他账户只读"——别人读得到令牌就等于能调控制面关机。
# 用 SID 而不是名字：中文 Windows 上 "Administrators" 不叫这个名。
# 收紧之后 SYSTEM 与管理员照样读得到，所以以 SYSTEM 跑的计划任务不受影响。
function Set-RuntimeConfigAcl([string]$Path) {
	if (-not $IsAdmin) { return }
	# 运行配置现在落在**用户目录**里（%USERPROFILE%\sol），归当前用户所有：留他本人 + SYSTEM +
	# Administrators。原来只留后两个，文件一旦落在用户目录，你自己那份反而读不开。
	$sid = ''
	try { $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value } catch { }
	$out = if ($sid) {
		& icacls $Path /inheritance:r /grant:r "${sid}:(F)" '*S-1-5-18:(F)' '*S-1-5-32-544:(F)' 2>&1
	} else {
		& icacls $Path /inheritance:r /grant:r '*S-1-5-18:(F)' '*S-1-5-32-544:(F)' 2>&1
	}
	if ($LASTEXITCODE -ne 0) {
		Warn2 "没能收紧 $Path 的权限：$($out -join ' ')（里面有明文令牌，建议手动只留你本人、SYSTEM 与 Administrators）"
	}
}
function Ensure-RuntimeConfig {
	if (Test-Path $RunConfig) { return }
	try {
		$cfgDir = Split-Path -Parent $RunConfig
		if (-not (Test-Path $cfgDir)) { New-Item -ItemType Directory -Path $cfgDir -Force -ErrorAction Stop | Out-Null }
		if (Test-Path $LegacyDesktopConfig) {
			# ① 早先按"执行目录"生成、后来被改过的：原样搬过来，别把用户改过的令牌、规则冲掉。
			# 跟包里发的模板逐字相同的那份是示例（仓库里、anyboot 解包留下的），照抄等于把示例当配置用——
			# 而且令牌还是个占位符，服务起不来。那种情况往下走，用模板重新生成一份带随机令牌的。
			$adoptLegacy = $true
			if ($TemplateConfig -and (Test-Path $TemplateConfig)) {
				try {
					$mine = ([IO.File]::ReadAllText((Resolve-Path -LiteralPath $LegacyDesktopConfig), [Text.UTF8Encoding]::new($false))) -replace "`r`n", "`n"
					$theirs = ([IO.File]::ReadAllText((Resolve-Path -LiteralPath $script:TemplateConfig), [Text.UTF8Encoding]::new($false))) -replace "`r`n", "`n"
					if ($mine.TrimEnd() -eq $theirs.TrimEnd()) { $adoptLegacy = $false }
				} catch { $adoptLegacy = $true }
			}
			if ($adoptLegacy) {
				Copy-ConfigFile $LegacyDesktopConfig $RunConfig
				$script:RuntimeConfigCopied = $true
			}
		}
		if (-not (Test-Path $RunConfig) -and (Test-Path $TemplateConfig)) {
			# ② 包自带的模板（scripts/sol.yaml）：注释最全的一份，只把令牌占位符换成真的随机数。
			$token = New-ControlToken
			$body = [System.IO.File]::ReadAllText($TemplateConfig, [Text.UTF8Encoding]::new($false))
			# 占位符没换成真令牌的话，生成出来的就是一份起不来的配置（token 为空值）——宁可在这里报错。
			if ($body -notmatch 'token:\s*TOKEN\s*}') { throw "模板 $TemplateConfig 里找不到 auth.token: TOKEN 占位符，换不了令牌" }
			$body = [regex]::Replace(
				$body,
				'(?m)^(\s*auth:\s*\{\s*type:\s*bearer,\s*token:\s*).*?(\s*\})\s*$',
				{ param($m) $m.Groups[1].Value + $token + $m.Groups[2].Value })
			Write-Utf8NoBom $RunConfig ($body -split "`r?`n")
		} else {
			# ③ 内置模板（和 scripts/sol.yaml 一字不差的正文）。
			$token = New-ControlToken
			$lines = @(
				'# 纯包 -> 关机；magic+"reboot" -> 重启；magic+"sleep" -> 睡眠',
				'version: 1',
				'server:',
				'  interfaces: []',
				'  # 改这份文件后秒级自动生效（改坏了也不会顶掉正在跑的那份）。',
				'  watch: 5s',
				'  # HTTP 控制面（默认开启）：令牌三选一——token 直接写在下面（这里已经是随机生成的一串）、',
				'  # token_env 指环境变量、token_file 指一个只有自己能读的文件。',
				'  # 三端的设置办法与 curl 用法见 README 的 Control plane 一节；不要控制面就把 enabled 改成 false。',
				'  http:',
				'    enabled: true',
				'    listen: 127.0.0.1:8080',
				'    # 默认只监听本机；要从别的机器调接口就改成 0.0.0.0:8080 并自己管好令牌。',
				"    auth: { type: bearer, token: $token }",
				'rules:',
				'  - match: { ports: [11], content: { kind: none } }',
				'    action: power.shutdown',
				'  - match: { ports: [12], content: { kind: none } }',
				'    action: power.reboot',
				'  - match: { ports: [10], content: { kind: none } }',
				'    action: power.sleep',
				'# 重复包保护（默认就开着，可关）：电源动作各 5s 冷却；刚开机 / 刚唤醒 5s 内不执行电源动作。',
				'# 想关掉：settle 写 0、把对应动作写 0s。',
				'security:',
				'  settle: 5s',
				'  cooldowns: { power.sleep: 5s, power.shutdown: 5s, power.reboot: 5s }'
			)
			Write-Utf8NoBom $RunConfig $lines
		}
		if (Test-Path $RunConfig) { Set-RuntimeConfigAcl $RunConfig }
		$script:RuntimeConfigGenerated = $true
	} catch {
		Warn2 "没能把运行配置写到 $RunConfig：$_"
	}
}
$ConfigGenerated = $false
if (-not (Test-Path $UserConfig)) {
	# 执行目录里那份只有承载了你的改动（改过 --config、加过别的键）才搬过来；内容跟我们默认生成的
	# 那份一样就只是示例（仓库里、随包复制、anyboot 解包留下的），照着抄会把示例当成这台机器的实际跑法。
	if ((Test-Path $LegacyUserConfig) -and -not (Test-StockUserConfig $LegacyUserConfig)) {
		# 早先在执行目录里生成、后来被你改过的：原样搬过来（里面的 --config 老取值由下面的 Test-Args 挑出来）。
		Copy-ConfigFile $LegacyUserConfig $UserConfig
		Say "安装配置   沿用 $(Get-Location).Path\$InstallConfigName（原样搬到 $UserConfig）"
	} else {
		Write-UserConfig
	}
	$ConfigGenerated = $true
}
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
# 老版本把默认值写成了执行目录里的 .\sol.yaml（后来变成 ProgramData\sol\sol.yaml）。默认值跟着位置走，
# 所以只认这几样"我们以前替你写的"取值：认出来就改写成现在的位置，内容一起搬过去，改动不丢；
# 你自己另行指定的路径一律不动——那是有意为之的。
$LegacyRunConfig = @()
if (-not [IO.Path]::IsPathRooted($RunConfig)) { $LegacyRunConfig += (Join-Path (Get-Location).Path $RunConfig) }
if ($RunDirArg -and -not [IO.Path]::IsPathRooted($RunDirArg)) { $LegacyRunConfig += (Join-Path (Get-Location).Path $RunDirArg) }
$LegacyRunConfig += (Join-Path $InstallDir $RunConfigName)
$LegacyRunConfig += (Join-Path (Get-Location).Path $RunConfigName)
if ($LegacyRunConfig -contains $RunConfig) {
	$wasGenerated = $false
	if (-not (Test-Path $RunConfig)) {
		# 新位置还没有：老文件原样搬过去，连令牌一起。
		foreach ($cand in $LegacyRunConfig) {
			if ((Test-Path $cand) -and ($cand -ne $RunConfig)) {
				try { Copy-ConfigFile $cand $RunConfig; $RuntimeConfigCopied = $true; $wasGenerated = $true } catch { Warn2 "没能把 $cand 拷到 $RunConfig：$_" }
				break
			}
		}
	}
	$RunConfig = $DefaultArgs[2]
	# 只换 --config 的取值，你自己加的其它参数照旧。
	$migrated = @()
	for ($k = 0; $k -lt $ArgsList.Count; $k++) {
		if (($ArgsList[$k] -eq '--config') -and ($k + 1 -lt $ArgsList.Count)) {
			$migrated += '--config'; $migrated += $RunConfig; $k++
		} else { $migrated += $ArgsList[$k] }
	}
	$ArgsList = $migrated
	Set-Args-Line $ArgsList
	$copiedNote = if ($RuntimeConfigCopied) { '（老位置那份原样拷过来了）' } else { '' }
	Say "运行配置   改到用户目录：$RunConfig$copiedNote；你另外指定的路径不受影响"
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
	Head2 '[现状]'
	if ($LocalBin) {
		$shortSha = (Has-Sha256 $LocalBin).Substring(0, [Math]::Min(12, (Has-Sha256 $LocalBin).Length))
		Say "  二进制      $LocalBin"
		Say "  版本        $(Version-Tag $LocalBin)（sha256 $shortSha…）"
		$fits = Test-BinaryFitsHost $LocalBin
		if ($fits) { Warn2 "$fits——这份装不起来，去下本机架构的那个包。" }
	} else {
		Say '  二进制      没有找到可用的 sol.exe：脚本旁边、当前目录、PATH 里都没有'
	}
	if ($PathSols.Count -gt 0) {
		Say '  PATH 上的 sol.exe'
		foreach ($p in $PathSols) {
			if ($p -eq $LocalBin) { Say "    $p  ← 生效" } else { Say "    $p" }
		}
	}
	if ($LedgerExists) {
		$instTag = if ($LedgerJson.installed_version) { $LedgerJson.installed_version } else { '版本未知' }
		if ($LedgerJson.incomplete -eq $true) { Say "  已安装      $instTag（上次没装完——再跑一次会补上）" }
		else { Say "  已安装      $instTag（由安装脚本安装）" }
		Say "  台账        $Ledger"
	} else {
		Say '  已安装      无台账（从没被这个脚本装过；下面按你已有的二进制处理）'
		# 别用 Test-Path 去捅 C:\Windows\System32\Tasks\<名字>：那个目录的 ACL 会让**未升权**的
		# Test-Path 直接抛"拒绝访问"，在 $ErrorActionPreference='Stop' 下整只脚本当场死掉（真机实测）。
		# 问"任务在不在"走列表那条路（Get-ScheduledTask / schtasks /Query），未升权也能读。
		if ((Task-State) -eq 'installed') {
			Warn2 "有计划任务但没有台账（$TaskName）：不是我装的也归这个脚本管——选 1 会先删掉它、再按这份 install.yaml 重建；卸载也会删它。
      要手工清掉：schtasks /Delete /TN $TaskName /F"
		}
	}
	$st = Task-State
	if ($st -eq 'installed') {
		Say "  服务        计划任务 $TaskName 已注册"
		# 用户要的：在任务计划里"一眼看到启动的是什么"。脚本这边也照抄一行出来。
		$ourTask = Get-OurTask
		if ($ourTask -and $ourTask.Actions -and ($ourTask.Actions.Count -gt 0) -and $ourTask.Actions[0].Execute) {
			Say "  启动        $("$($ourTask.Actions[0].Execute) $($ourTask.Actions[0].Arguments)".Trim())"
		}
	} elseif ($st -eq 'unknown') {
		Say "  服务        读不到内容（计划任务 $TaskName）：当前不是管理员，要管理员才看得到；以管理员身份跑一次就能看全"
	} else {
		Say "  服务        没有（计划任务 $TaskName 不存在）"
		if ($LedgerExists -and ($LedgerJson.service.created_unit -eq $true)) {
			# 只陈述事实，不猜凶手：这台机器上它很可能是**我们自己卸载时**删的。
			Say "              台账说这个任务是脚本建的，现在确实不在；重跑一次（选 1）会再建。"
		}
	}
	Say "  日志        $LogFile"
	$instBin = if ($LedgerJson.binary) { $LedgerJson.binary } else { $DestBin }
	$instIds = @(Get-RunningSolIds $instBin)
	if ($instIds.Count -gt 0) {
		Say "  进程        有 $($instIds.Count) 个在跑：$instBin（要用到端口时会先停掉它）"
	} else {
		# 名字级回退：没升权时认不出"是不是安装目录那份"，但至少能看见"有没有在跑"，
		# 不至于说"没有在跑"而其实有（用户上次就是这么发现不对的）。
		$byName = @(Get-Process -Name 'sol' -ErrorAction SilentlyContinue)
		if ($byName.Count -gt 0) {
			Say "  进程        有 $($byName.Count) 个 sol.exe 在跑（pid $($byName.Id -join ',')）；路径要管理员才看得到，所以认不出是不是安装目录那份"
		} elseif ($st -eq 'installed') {
			Say '  进程        没有在跑（计划任务在，但它眼下没跑起来；重跑脚本选 1 会重新起一次）'
		} else {
			Say '  进程        没有在跑'
		}
	}
	if (Test-Path $RunConfig) {
		if ($script:RuntimeConfigGenerated) { Say "  运行配置    $RunConfig$(if ($script:RuntimeConfigCopied) { '（从早先的位置原样搬过来的那份）' } else { '（刚生成的示例配置：三条规则，按需改）' })" }
		else { Say "  运行配置    $RunConfig（sol 会读它）" }
	} else { Say "  运行配置    还不存在：$RunConfig" }
	if ($env:SOL_INSTALL_ROOT) { Say "  权限        普通用户 + SOL_INSTALL_ROOT=$env:SOL_INSTALL_ROOT（自己的地盘，不会升权）" }
	elseif (-not $IsAdmin) { Say '  权限        当前不是管理员：轮到需要权限的步骤时，会用 UAC 以管理员身份重跑一遍自己（不会再问一遍）' }
}
function Show-Config {
	Head2 '[安装配置]'
	Say "  $UserConfig"
	Say ''
	foreach ($l in (Get-Content -Encoding UTF8 $UserConfig)) { Say "    $l" }
	Say ''
	if ($ConfigGenerated) { Say '  （刚生成的。改它，然后重新运行脚本；脚本不会覆盖已存在的这份文件。）' }
}
# ───────────────────────── 问答 ─────────────────────────
$script:NoAnswer = $false
function Ask-Yes($prompt, $default) {
	# 升权子进程有自己的窗口，很可能不在用户眼前：在那里 Read-Host 等人按键，而父进程在 -Wait 里
	# 干等——用户看到的就是"脚本卡死没响应"（真机上就这么撞过两次）。子进程一律不问，取默认值；
	# 该问的在父进程问，答案经内部参数带进来（环境变量过 UAC 不保证继承，这是文件开头记下的坑）。
	if ($script:Child) {
		Warn2 "（升权子进程里不提问：「$prompt」取默认值 $default；要它生效请在父进程那一步回答）"
		return ($default -eq 'y')
	}
	$suffix = if ($default -eq 'y') { '[Y/n]' } else { '[y/N]' }
	$ans = ''
	try { $ans = (Read-Host "$prompt $suffix") } catch { $script:NoAnswer = $true; return $false }
	if ($null -eq $ans) { $script:NoAnswer = $true; return $false }
	$ans = $ans.Trim().ToLower()
	if (-not $ans) { $ans = $default }
	return ($ans -eq 'y' -or $ans -eq 'yes')
}
function Banner {
	$ver = ''
	if ($LocalBin) { $ver = Version-Tag $LocalBin }
	$where = if ($env:SOL_INSTALL_ROOT) { 'SOL_INSTALL_ROOT 沙箱' } else { 'Windows / 计划任务' }
	Head2 "sol 安装脚本  |  $(if ($ver) { $ver } else { '没有找到 sol.exe' })  |  $where"
	Say '改配置、重跑这个脚本，就是改 sol 的跑法。先报现状，再问你要做什么。'
}
# 选项写成编号列表，而不是一行用斜杠串起来的 回车/u/r/n——那行既难读也难记。
function Show-Menu {
	Head2 '[选项]'
	Say '  1  按配置应用（按 install.yaml 里的 run.args 装或升级）'
	Say '  2  卸载（摘掉计划任务，按台账删掉自己建的东西）'
	Say '  3  重新生成 install.yaml'
	Say '  4  退出，什么都不改'
	Say ''
}
function Ask-Choice($prompt) {
	if ($script:Child) { Warn2 '（升权子进程里不提问：直接退出）'; return 'quit' }
	# 只认序号（回车＝1），同时容忍单字母的旧习惯；看不懂就重问，绝不猜。
	for ($i = 0; $i -lt 5; $i++) {
		$ans = ''
		try { $ans = (Read-Host "$prompt [1]") } catch { $script:NoAnswer = $true; return 'quit' }
		if ($null -eq $ans) { $script:NoAnswer = $true; return 'quit' }
		$ans = $ans.Trim().ToLower()
		if (-not $ans) { return 'apply' }
		switch ($ans) {
			'1' { return 'apply' }
			'apply' { return 'apply' }
			'2' { return 'uninstall' }
			'u' { return 'uninstall' }
			'uninstall' { return 'uninstall' }
			'3' { return 'regenerate' }
			'r' { return 'regenerate' }
			'regenerate' { return 'regenerate' }
			'4' { return 'quit' }
			'n' { return 'quit' }
			'no' { return 'quit' }
			'q' { return 'quit' }
			'quit' { return 'quit' }
			default { Write-Host '  请输入 1-4 之间的序号（回车＝1）。' }
		}
	}
	return 'quit'
}
# ───────────────────────── 动作清单 ─────────────────────────
$script:Actions = @()
function Act($kind, $what, $cmd) {
	$line = "[$kind] $what"
	if ($cmd) { $line += "`n        `$ $cmd" }
	$script:Actions += $line
	Note-Progress $line
}
function Write-Ledger($incomplete, $prevVersion, $prevSha) {
	if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null }
	$sha = Has-Sha256 $DestBin
	$ports = Get-ThresholdPorts
	$obj = [ordered]@{
		schema            = 1
		method            = 'script'
		installed_version = (Version-Tag $DestBin)
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
		log_paths         = @($LogFile)
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
# ── 架构对不上：提前说人话，别让用户去看 .NET 的天书 ──
# 在 x64 机器上解了 windows-arm64 的包时，真跑起来的报错是
# "指定的可执行文件不是此操作系统平台的有效应用程序"。读 PE 头的 Machine 字段就能提前判断。
# 读 PE 头里的 Machine（0 表示读不出来／不是 PE）。
function Get-PEBinaryMachine($path) {
	try {
		$fs = [System.IO.File]::OpenRead($path)
		try {
			$br = New-Object System.IO.BinaryReader($fs)
			if ($br.ReadUInt16() -ne 0x5A4D) { return 0 }        # 'MZ'
			$fs.Position = 0x3C
			$off = $br.ReadUInt32()
			$fs.Position = $off
			if ($br.ReadUInt32() -ne 0x00004550) { return 0 }    # 'PE\0\0'
			return [int]$br.ReadUInt16()
		} finally { $fs.Close() }
	} catch { return 0 }
}
# 本机架构，统一成 x64 / arm64 / x86 这种写法。
function Get-HostArchName {
	$arch = ''
	try { $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() } catch { }
	if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
	switch ("$arch".ToUpper()) {
		'X64' { return 'x64' } 'AMD64' { return 'x64' }
		'ARM64' { return 'arm64' }
		'X86' { return 'x86' } 'ARM' { return 'arm' }
		default { return "$arch".ToLower() }
	}
}
# 二进制跟本机架构对不对得上：'' = 没问题；否则给一句能直接念给用户听的原因。
function Test-BinaryFitsHost($path) {
	if (-not $path -or -not (Test-Path $path)) { return '' }
	if (-not ($path -like '*.exe')) { return '' }   # 不是 PE（例如被拿去做跨平台测试）就不判
	$m = Get-PEBinaryMachine $path
	if ($m -eq 0) { return '' }
	$binArch = switch ($m) { 0x8664 { 'x64' } 0xAA64 { 'arm64' } 0x014C { 'x86' } 0x01C4 { 'arm' } default { '0x{0:X}' -f $m } }
	$hostArch = Get-HostArchName
	# x64 上跑 x86 是正常的（WOW64），其余不匹配都拦。
	if (($binArch -eq $hostArch) -or ($hostArch -eq 'x64' -and $binArch -eq 'x86')) { return '' }
	return "这份二进制是 $binArch 的，本机是 $hostArch（多半是下错架构的包了）"
}
function Invoke-Precheck {
	if (-not $LocalBin) { return $true }
	# 架构对不上就别装了：真跑起来只会给一句天书（"不是此操作系统平台的有效应用程序"）。
	$fits = Test-BinaryFitsHost $LocalBin
	if ($fits) {
		Die "$fits。`n  $LocalBin`n本机架构：$(Get-HostArchName)。Windows 发布包是 sol-<版本>-windows-amd64.zip 或 -windows-arm64.zip——别拿错；真要跨架构用，就把 install.yaml 的 run.args 指向本机架构那份二进制。"
	}
	if (-not (Test-Path $RunConfig)) {
		Warn2 "运行配置 $RunConfig 还不存在，没法预检；服务起来后会立刻退出（sol 拒绝在没有规则时启动）"
		return $false
	}
	# 直接用 .NET 起进程，不用 Start-Process -RedirectStandardError：后者在 Windows PowerShell 5.1 上
	# 会一直等到子进程退出，HasExited 于是永远是 true——"预检被拒绝"就再也说不清是真是假。
	# 这里明确等 3 秒：还在跑 = 通过；已退出 = 把它的输出（stdout + stderr 都要）原样给你看。
	$psi = New-Object System.Diagnostics.ProcessStartInfo
	$psi.FileName = $LocalBin
	$argLine = @()
	foreach ($a in ($ArgsList + '--dry-run')) { if ($a -match '\s') { $argLine += '"' + $a + '"' } else { $argLine += $a } }
	$psi.Arguments = ($argLine -join ' ')
	$psi.UseShellExecute = $false
	$psi.CreateNoWindow = $true
	$psi.RedirectStandardOutput = $true
	$psi.RedirectStandardError = $true
	$proc = New-Object System.Diagnostics.Process
	$proc.StartInfo = $psi
	try {
		if (-not $proc.Start()) { Warn2 '预检没能起起来 sol（进程没起来）'; return $false }
		$outTask = $proc.StandardOutput.ReadToEndAsync()
		$errTask = $proc.StandardError.ReadToEndAsync()
		if (-not $proc.WaitForExit(3000)) {
			try { $proc.Kill() } catch { }
			Say '预检        通过（sol 用这份配置起来了 3 秒，匹配了也不会真做事：listen 是 dry-run）'
			return $true
		}
		# 真正有用的那行在最后（比如 bind 失败），所以打印尾部而不是"前 3 行"。
		$all = @((($outTask.Result + "`n" + $errTask.Result) -split "`r?`n") | Where-Object { $_.Trim() })
		Say "预检        被拒绝（sol 起来又退出了，退出码 $($proc.ExitCode)）："
		$show = if ($all.Count -gt 15) { $all[($all.Count - 15)..($all.Count - 1)] } else { $all }
		foreach ($l in $show) { Say "            $l" }
		Say '            （要改端口/接口就编辑上面那份运行配置，改完重跑这个脚本）'
		return $false
	} catch {
		Warn2 "预检没能起起来 sol（$_）。没预检通过就不动手，所以此次什么都没改。"
		return $false
	} finally {
		try { $proc.Dispose() } catch { }
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
	if ($action -eq 'uninstall') { Say '这一步要摘掉计划任务、删掉安装目录里的二进制、撤掉防火墙规则和 PATH，需要管理员。' }
	else { Say '这一步要写安装目录、注册计划任务、改机器 PATH，需要管理员。' }
	Say '我用 UAC 以管理员身份重新执行一遍自己：同一个目录、你刚才的答案带过去，不会再问一遍。'
	Say '接下来会弹一个 UAC 授权框（“你要允许此应用对你的设备进行更改吗”）。'
	Say '授权之后不会再出现第二个窗口：需要管理员的那一步在后台跑，它的每一步都会显示在这个屏幕里。'
	# 这一条是真机上花了几轮才找出来的：那个窗口出来后**别用鼠标点它**——Windows 控制台的“快速编辑”
	# 默认开着，点一下会进入标记模式，conhost 停止读输出、进程的写也跟着阻塞，看起来就是“一片空白、
	# 怎么都不动”，而按一下回车或 Esc 就解冻。多发一句，省掉这一整轮猜谜。
	Say '那个窗口出来之后**不要用鼠标点它**：Windows 控制台的“快速编辑”会让它冻住（看起来一片空白）。真冻住了，在它里面按一下回车或 Esc 就能解冻。'
	Say '看不到它就看任务栏、或按 Win 键找一下——**不点它，这里会一直等，看起来就像卡住了**。'
	if ($Piped) {
		Die "这一步需要管理员，但脚本是管道执行（irm … | iex）的：没有文件可以重新以管理员身份运行。请先存成文件再跑：`n  irm <install.ps1 的地址> -OutFile install.ps1; powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1`n  或者直接右键 install.cmd 选“以管理员身份运行”。"
	}
	$hostExe = (Get-Process -Id $PID).Path
	if (-not $hostExe) { $hostExe = Join-Path $PSHOME 'powershell.exe' }
	$svc = if ($script:ServiceChosen) { 'yes' } else { 'no' }
	# 带空格的取值要自己加引号：PowerShell 拼命令行时不会替你加，路径里有空格就会把参数切断。
	$rawArgs = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $PSCommandPath,
		'-SolAction', $action, '-SolService', $svc, '-SolRunDir', $RunDir, '-SolUnitName', $TaskName)
	if ($script:DelConfigsGiven) { $rawArgs += @('-SolDelConfigs', $(if ($script:DelConfigs) { 'yes' } else { 'no' })) }
	if ($env:SOL_INSTALL_ROOT) { $rawArgs += @('-SolRoot', $env:SOL_INSTALL_ROOT) }
	$argList = @($rawArgs | ForEach-Object { if ($_ -match '\s') { '"' + $_ + '"' } else { $_ } })
	$env:SOL_INSTALL_ELEVATED = '1'
	try {
		# UAC 子进程有自己的窗口，它的输出在父进程里一个字都看不到——失败也是静悄悄的，
		# 而这正是"服务没注册"最难查的地方。子进程把全程写进 transcript，父进程随后打印出来。
		$elevLog = Join-Path $RunDir "elevated-$action.log"
		$progressFile = Join-Path $RunDir "elevated-$action.progress"
		if (Test-Path $progressFile) { Remove-Item $progressFile -Force -ErrorAction SilentlyContinue }
		# 不用 -Wait：UAC 对话框要是没被看到（藏在窗口后面、别的桌面、或只闪了下任务栏），控制台会
		# 一声不吭地干等——用户看到的就是"卡死没响应"。自己轮询，并且每 10 秒报一次在等什么、等了多久。
		# 不显示窗口：那个窗口在真机上"必须按一下回车才动"（用户反馈了多轮），而它要说的每句话
		# 都已经经进度文件转述到眼前这个窗口里了。-WindowStyle Hidden 让它完全不可见，也就无从被冻住。
		$p = Start-Process -FilePath $hostExe -Verb RunAs -PassThru -WindowStyle Hidden -WorkingDirectory $RunDir -ArgumentList $argList
		# 万一授权框真的没弹出来（系统策略/焦点问题），这里会一直等：把出路直接写出来。
		Say '      如果这里停住超过 10 秒、又没看到授权框：按 Ctrl+C，然后右键 install.cmd 选「以管理员身份运行」（那条路不需要中途授权）。'
		# 子进程每说一句都会追进这个文件：边等边把它念出来，用户就不用去猜那个窗口。
		$progressFile = Join-Path $RunDir "elevated-$action.progress"
		$shown = 0
		$waited = 0
		while (-not $p.WaitForExit(10000)) {
			$waited += 10
			Warn2 "还在等那一步结束（已等 ${waited} 秒）：如果屏幕上没有第二个窗口，或它是空白的，它就是卡住了——按 Ctrl+C，然后右键 install.cmd 选「以管理员身份运行」。"
			if ($waited -ge 60 -and $shown -eq 0) {
				Say '  已经 60 秒没有任何进度：多半就是那个窗口被点住、冻在“快速编辑”里了——在它里面按回车或 Esc 解冻；或者按 Ctrl+C，改走“以管理员身份运行”。'
			}
			if (Test-Path $progressFile) {
				$all = @(Get-Content -Path $progressFile -Encoding UTF8 -ErrorAction SilentlyContinue)
				if ($all.Count -gt $shown) {
					Say '  那一步目前做到：'
					foreach ($l in $all[$shown..($all.Count - 1)]) { Say "    $l" }
					$shown = $all.Count
				}
			}
		}
		Say ''
		if (Test-Path $progressFile) {
			Say '以管理员身份那一步的输出：'
			foreach ($l in (Get-Content -Encoding UTF8 $progressFile)) { Say "  $l" }
		} else {
			Warn2 "以管理员身份那一步没有留下任何输出（$progressFile 不存在）。退出码：$($p.ExitCode)"
		}
		exit $p.ExitCode
	} catch {
		Die "这一步需要管理员权限，但 UAC 提权没成功（被拒绝或不可用）。请右键 install.cmd 选“以管理员身份运行”，或在管理员 PowerShell 里再跑一次。原因：$_"
	}
}
# 让 sol 自己把日志写进文件——Windows 的计划任务收集不到 stdout，这是 Windows 侧唯一能落盘的方式。
# 只在运行配置里没有 logging: 这一节时追加；用户自己写过的 logging 一律不动（改它等于改人家的配置）。
function Add-FileLoggingToConfig {
	# 只"读来判、追加来写"，绝不把整份文件读进来再写回去。
	# 整文件读改写一旦解码方式错了（PS 5.1 的 Get-Content 不给 -Encoding 就按系统 ANSI 代码页，
	# 简中是 GBK），坏掉的是整份配置：全角标点的尾字节在 GBK 里映射不到字符，会被换成 '?'，
	# 连后面的换行一起吞掉——注释被粘成一行，再以 UTF-8 写回去文件就永久坏了。
	# 实测吃到过：生成的 sol.yaml 里 4 处注释被粘行，sol 报 yaml: line 7: did not find expected key。
	# 追加最多多出一行，坏不了已有的内容。
	if (-not (Test-Path -LiteralPath $RunConfig)) { return }
	$hasLogging = $false
	try {
		foreach ($l in (Get-Content -LiteralPath $RunConfig -Encoding UTF8 -ErrorAction Stop)) {
			if ($l -match '^\s*logging\s*:') { $hasLogging = $true; break }
		}
	} catch {
		Say "（读不了运行配置，跳过补 logging：$_）"
		return
	}
	if ($hasLogging) {
		Say "（运行配置里已经有 logging，照它来，脚本不动它）"
		return
	}
	# 末尾没有换行就先补一个，免得注释和最后一行粘在一起。
	$sep = ''
	try {
		$tail = [System.IO.File]::ReadAllText($RunConfig, [Text.UTF8Encoding]::new($false))
		if ($tail.Length -gt 0 -and -not $tail.EndsWith("`n")) { $sep = "`n" }
	} catch { $sep = "`n" }
	$block = $sep + "`n# 计划任务的输出没人接，所以让 sol 自己写文件（这一节由安装脚本补上；想改就改）。`nlogging: { output: file, file: '$LogFile' }`n"
	Invoke-WithRetry '在运行配置里补 logging（让 sol 写日志文件）' { Add-Utf8NoBom $RunConfig $block } | Out-Null
	Act 'user' "运行配置补上 logging（sol 的日志落到 $LogFile）" "（往 $RunConfig 追加 logging: { output: file, file: '$LogFile' }）"
}
function Open-ConfigDir($dir, $file) {
	# 呼出文件管理器跳到运行配置所在目录：先问、问过才开；沙箱、子进程、无人能答时只报路径。
	# 绝不 -Wait：一个等着人点一下的窗口就是"脚本卡死"的老剧本。
	Say ''
	Say "  配置文件    $file"
	Say '     刚生成的这份就是 sol 的跑法：端口、动作、允许的来源网段都在里面。默认是'
	Say '     纯包关机 / magic+reboot 重启 / magic+sleep 睡眠，按需改。'
	Say '     改完不用重装：配置里有 watch，秒级自动生效；改坏了也不会顶掉正在跑的那份。'
	if ($env:SOL_INSTALL_NO_OPEN -or $env:SOL_INSTALL_ROOT -or $script:Child) { Say "     目录：$dir"; return }
	if (-not (Test-Path $dir)) { return }
	if (Ask-Yes '要现在打开文件夹，去改这份配置吗？' 'y') {
		try { Start-Process -FilePath explorer.exe -ArgumentList $dir -ErrorAction Stop; Say "     已打开：$dir" }
		catch { Say "     打开失败，自己开这个目录：$dir" }
	} else { Say "     目录：$dir" }
}
function Invoke-Install {
	if (-not $LocalBin) { Die '没有可用的 sol.exe：把它放在脚本旁边或 PATH 里，再运行一次' }
	# 运行配置在进主流程时就已经落好了（Ensure-RuntimeConfig）；这里不再重复。
	$newSha = Has-Sha256 $LocalBin
	Write-Ledger $true $LedgerJson.installed_version $LedgerJson.sha256
	Act 'user' '写安装台账（先写 incomplete，中途失败也留痕）' ''
	# 你说"应用"但什么都没变 → 不折腾
	if ($LedgerExists -and $newSha -eq $LedgerJson.sha256) {
		$taskOk = ((Task-State) -eq 'installed') -or (-not $script:ServiceChosen)
		# 还要确认任务跑的是我们的包装脚本：老安装的任务直接跑 exe、没有日志，得走完整路径升一次。
		# 任务跑的不是我们的 exe（早先的安装跑的是 run-sol.cmd）也要走完整路径，好把它升过来。
		if ($taskOk -and $script:ServiceChosen -and (-not $env:SOL_INSTALL_ROOT)) {
			$t = Get-OurTask
			if (-not $t -or -not $t.Actions -or ($t.Actions.Count -eq 0) -or ($t.Actions[0].Execute -notlike "*$DestBin*")) { $taskOk = $false }
		}
		if ($taskOk) {
			Say ''
			Say "已是最新，无需操作：二进制没变（sha256 $newSha 与台账一致），计划任务与配置没变。"
			$script:Noop = $true
			return
		}
	}
	# 别人的任务默认不覆盖——但**要问一句**：用户明确说要接管，就该能接管。死路是最糟的：
	# 人说"我要装"，脚本却只回一句"不是你建的我不管"。
	# 叫 sol 的任务归这个脚本管，不看是谁建的：注册时先删掉再按这份 install.yaml 重建（见
	# Register-SolService），卸载时也会删掉。这里只是把话说清楚，不再问"要不要接管"。
	if ($script:ServiceChosen -and ((Task-State) -eq 'installed') -and ($LedgerJson.service.created_unit -ne $true)) {
		Say ''
		Warn2 "已经有一份计划任务 $TaskName，而且不是本脚本建的：按你的规矩，注册时先删掉它、再按这份 install.yaml 重建。"
	}
	# 例外：升级时几乎必然"绑不上端口"——因为正在跑的那份是我们自己的 sol.exe，占着同一个端口。
	# 那就先停它再试一次；还不过就把它按原样起回去，不让你白白少一个正在跑的服务。
	if (-not (Invoke-Precheck)) {
		$binNow = if ($LedgerJson.binary) { $LedgerJson.binary } else { $DestBin }
		if (@(Get-RunningSolIds $binNow).Count -gt 0) {
			Say ''
			Say '预检绑不上端口，而我们有份 sol.exe 正在跑——多半就是它占着。先停掉它再试一次：'
			Stop-RunningInstances $binNow '它占着端口，而预检要绑同一个端口'
			if (-not (Invoke-Precheck)) {
				Restore-SolTask
				Die '预检没通过（停掉旧的再试也一样）。刚才停掉的那份我已经按原样起回去了；改完配置再运行一次。'
			}
		} else {
			Die '预检没通过，什么都没动。改完配置再运行一次。'
		}
	}
	# Windows 陷阱：正在运行的 exe 覆盖不了 → 先结束任务
	if ((Task-State) -eq 'installed') {
		# 走 Invoke-Native 而不是裸调：原生命令往 stderr 写东西（这里可能是"拒绝访问"）
		# 在 $ErrorActionPreference='Stop' 下会被当成终止错误，把整个动作带走（真机就这么死的）；
		# 沙箱里更不该碰本机任务管理器。
		if (-not $env:SOL_INSTALL_ROOT) { Invoke-Native 'schtasks' @('/End', '/TN', $TaskName) | Out-Null }
		Act 'admin' '结束计划任务（运行中的 exe 有文件锁）' "schtasks /End /TN $TaskName"
		Start-Sleep -Seconds 1
	}
	# 任务停了不代表进程没了（手动起的那份、或没赶上的实例）；覆盖前必须把它清掉。
	Stop-RunningInstances $DestBin '正在运行的 exe 覆盖不了（Windows 文件锁）'
	if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null }
	$same = (Test-Path $DestBin) -and ((Has-Sha256 $DestBin) -eq $newSha)
	if ($same) {
		Say '二进制      已经就是这份（sha256 相同），不动'
	} else {
		if (Test-Path $DestBin) {
			Invoke-WithRetry '保留旧二进制' { Copy-Item -Path $DestBin -Destination "$DestBin.bak" -Force -ErrorAction Stop } | Out-Null
			Act 'admin' '保留旧二进制（回滚用）' "Copy-Item $DestBin $DestBin.bak"
			Say ("二进制      换版本：{0} → {1}" -f (Binary-Version $DestBin), (Binary-Version $LocalBin))
		}
		Invoke-WithRetry '放置二进制' { Copy-Item -Path $LocalBin -Destination $DestBin -Force -ErrorAction Stop } | Out-Null
		Act 'admin' '放置二进制' "Copy-Item $LocalBin $DestBin"
	}
	# 机器 PATH（只加一次）。沙箱里绝不碰（那是本机的地盘），真安装时也只提醒不致命：
	# 写 Machine 级环境变量需要管理员，非管理员会抛"不允许所请求的注册表访问权"——这类 .NET 异常
	# 在 $ErrorActionPreference='Stop' 下是**终止错误**，会把整个安装带走（真机实测就死在这儿）。
	if (-not $env:SOL_INSTALL_ROOT) {
		try {
			$machinePath = [Environment]::GetEnvironmentVariable('PATH', 'Machine')
			if ($machinePath -notlike "*$InstallDir*") {
				[Environment]::SetEnvironmentVariable('PATH', "$machinePath;$InstallDir", 'Machine')
				Act 'admin' '把落点目录加进机器 PATH' "[Environment]::SetEnvironmentVariable('PATH', '...;$InstallDir', 'Machine')"
			}
		} catch { Warn2 "没能把 $InstallDir 加进机器 PATH（不致命，命令行里用绝对路径即可）：$_" }
	}
	# SOL_INSTALL_ROOT 下不碰本机计划任务：那是别人的地盘（跟停进程、卸载那几处一样的规矩）。
	if ($script:ServiceChosen -and -not $env:SOL_INSTALL_ROOT) { Register-SolService }
	elseif ($script:ServiceChosen) { Say '  （沙箱：跳过计划任务注册——本机任务管理器是别人的地盘）' }
	# 计划任务是无窗口跑的，stdout 没人接：让 sol 自己写日志文件（配置里没有 logging: 就补一节）。
	if ($script:ServiceChosen -and (Test-Path $RunConfig)) {
		Add-FileLoggingToConfig
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
	Write-Ledger ([bool]$script:ServiceVerifyFailed) $LedgerJson.installed_version $LedgerJson.sha256
	Act 'user' "更新安装台账（incomplete=$([bool]$script:ServiceVerifyFailed)）" ''
	Append-History 'install'
}
function Register-SolService {
	if ($env:SOL_INSTALL_ROOT) { return }   # 沙箱里永不碰本机任务管理器
	# 任务直接跑 sol.exe：任务计划里"操作"那一栏就是可执行文件和它的参数，一眼看得懂。
	# （早先为了让 stdout 有地方去，包了一层 run-sol.cmd，结果任务里只看到一个 .cmd，用户不认。
	# 现在改成让 sol 自己写日志文件——见 Add-FileLoggingToConfig。）
	$tr = '"' + $DestBin + '" ' + (($ArgsList | ForEach-Object { if ($_ -match '\s') { '"' + $_ + '"' } else { $_ } }) -join ' ')
	# 规矩：已经装了就先删掉再重建，不做"就地覆盖"。覆盖会留下旧定义里的东西（旧主体、旧触发器、
	# 旧 ACL）；重建一次拿到的和这份 install.yaml 完全一致。也不看是谁建的：叫 sol 的任务归这个脚本管。
	if ((Task-State) -eq 'installed') {
		Invoke-Native 'schtasks' @('/End', '/TN', $TaskName) | Out-Null
		Invoke-Native 'schtasks' @('/Delete', '/TN', $TaskName, '/F') | Out-Null
		Act 'admin' '先删掉已有的计划任务（不看是谁建的）' "schtasks /Delete /TN $TaskName /F"
	}
	# schtasks 自己的报错是"为什么没注册上"的唯一线索：绝不能吞掉它。
	$r = Invoke-Native 'schtasks' @('/Create', '/TN', $TaskName, '/TR', $tr, '/SC', 'ONSTART', '/RU', 'SYSTEM', '/RL', 'HIGHEST', '/F')
	if ($r.Code -ne 0) {
		Warn2 "schtasks /Create（带 /RL HIGHEST）失败，退出码 $($r.Code)：$($r.Out)"
		# 部分 Windows 上 /RL HIGHEST 与 /RU SYSTEM 不能同时给（SYSTEM 本身就是最高权限）。
		$r = Invoke-Native 'schtasks' @('/Create', '/TN', $TaskName, '/TR', $tr, '/SC', 'ONSTART', '/RU', 'SYSTEM', '/F')
		if ($r.Code -ne 0) {
			Warn2 "schtasks /Create 仍然失败，退出码 $($r.Code)：$($r.Out)`n  手动来一遍：schtasks /Create /TN $TaskName /TR `"$tr`" /SC ONSTART /RU SYSTEM /F"
			return
		}
		Say '（去掉 /RL HIGHEST 后注册成功：/RU SYSTEM 的任务本身就是最高权限）'
	}
	# 建完必须复核：上次就是台账写了 created_unit=true、任务却查不到——不能只看 /Create 的退出码。
	if ((Task-State) -ne 'installed') {
		$script:ServiceVerifyFailed = $true
		Warn2 "任务注册后复核没看到它（$TaskName）：台账会记成没装完，重跑一次可以再建"
	}
	$run = Invoke-Native 'schtasks' @('/Run', '/TN', $TaskName)
	if ($run.Code -ne 0) { Warn2 "schtasks /Run 失败，退出码 $($run.Code)：$($run.Out)（任务已注册，只是这次没立刻起来；重启后会自动跑）" }
	Act 'admin' '注册并启动计划任务' "schtasks /Create /TN $TaskName /TR `"$tr`" /SC ONSTART /RU SYSTEM /F; schtasks /Run /TN $TaskName"
}
function Invoke-Uninstall {
	if (-not $LedgerExists) {
		# 没有台账也照样清：脚本只按**自己的名字**动手（计划任务 sol、默认落点的 sol.exe、防火墙规则
		# sol (WoL)、机器 PATH 里那一行）。用户点的是"卸载"，就该卸载干净——不再拿"是不是我装的"当门槛。
		Say ''
		Warn2 "没有台账（$Ledger）：这个脚本没在这台机器上装过 sol。仍按默认落点清理："
		Say '        · 计划任务 sol（有就删）/ 默认落点的 sol.exe / 防火墙规则 sol (WoL) / 机器 PATH 里那一行'
	}
	# 先停进程再删：正在跑的 exe 删不掉（Windows 文件锁），而且它会一直占着端口——
	# 下次安装的预检就会以"端口占用"失败，看起来像是装不上。
	$binNow = if ($LedgerJson.binary) { $LedgerJson.binary } else { $DestBin }
	# 走 Invoke-Native 而不是裸调：原生命令往 stderr 写东西（这里可能是"拒绝访问"）
	# 在 $ErrorActionPreference='Stop' 下会被当成终止错误，把整个动作带走（真机就这么死的）；
	# 沙箱里更不该碰本机任务管理器。
	if (-not $env:SOL_INSTALL_ROOT) { Invoke-Native 'schtasks' @('/End', '/TN', $TaskName) | Out-Null }
	Act 'admin' '结束计划任务实例（在跑的话）' "schtasks /End /TN $TaskName"
	Stop-RunningInstances $binNow '卸载要删掉它，也不能让它继续占着端口'
	# 走 Invoke-Native 而不是裸调：原生命令往 stderr 写东西（这里可能是"拒绝访问"）
	# 在 $ErrorActionPreference='Stop' 下会被当成终止错误，把整个动作带走（真机就这么死的）；
	# 沙箱里更不该碰本机任务管理器。
	if (-not $env:SOL_INSTALL_ROOT) { Invoke-Native 'schtasks' @('/Delete', '/TN', $TaskName) | Out-Null }
	Act 'admin' '删除计划任务' "schtasks /Delete /TN $TaskName /F"
	$rule = $null
	try { $rule = Get-NetFirewallRule -DisplayName 'sol (WoL)' -ErrorAction SilentlyContinue } catch { }
	if ($rule) {
		Remove-NetFirewallRule -DisplayName 'sol (WoL)' -ErrorAction SilentlyContinue
		Act 'admin' '删防火墙规则' "Remove-NetFirewallRule -DisplayName 'sol (WoL)'"
	}
	# 撤机器 PATH 同样：沙箱里不碰本机，真卸载时被拒（要管理员）也只提醒——别让异常带走整个卸载。
	if (-not $env:SOL_INSTALL_ROOT) {
		try {
			$machinePath = [Environment]::GetEnvironmentVariable('PATH', 'Machine')
			if ($machinePath -like "*$InstallDir*") {
				$newPath = (($machinePath -split ';') | Where-Object { $_ -and $_ -ne $InstallDir }) -join ';'
				[Environment]::SetEnvironmentVariable('PATH', $newPath, 'Machine')
				Act 'admin' '从机器 PATH 撤下落点目录' "[Environment]::SetEnvironmentVariable('PATH', '...', 'Machine')"
			}
		} catch { Warn2 "没能从机器 PATH 撤下 $InstallDir（不致命）：$_" }
	}
	$bin = if ($LedgerJson.binary) { $LedgerJson.binary } else { $DestBin }
	# sol.bak 只有升级过才有、包装脚本只有老安装才有：不存在的路径不该让重试器空转、更不该报假警。
	Invoke-WithRetry '删二进制、sol.bak 与包装脚本' {
		foreach ($p in @($bin, "$bin.bak", $TaskCmd)) { if (Test-Path $p) { Remove-Item -Path $p -Force -ErrorAction Stop } }
	} | Out-Null
	if (Test-Path $bin) { Warn2 "$bin 还在（可能有别的进程占着，或没权限）" }
	Act 'admin' '删二进制、sol.bak 与运行包装脚本' "Remove-Item $bin, $bin.bak, $TaskCmd"
	if (-not $script:DelConfigsGiven) {
		$script:DelConfigs = $false
		if (Ask-Yes '配置文件也一起删掉吗？' 'n') { $script:DelConfigs = $true }
	}
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
			Head2 '[完成]'
			Say "  版本        $(Version-Tag $DestBin)"
			Say "  二进制      $DestBin"
			if (Test-Path "$DestBin.bak") { Say "  （旧版本留在 $DestBin.bak，回滚就是把它换回去）" }
			Say ''
			Say "  安装配置    $UserConfig        改这里，然后重跑脚本"
			Say "  运行配置    $RunConfig$(if ($script:RuntimeConfigGenerated) { $(if ($script:RuntimeConfigCopied) { '（从早先的位置原样搬过来的那份）' } else { '（刚生成的示例配置：三条规则，按需改）' }) })"
			Say "  台账        $Ledger"
			Say "  服务日志    $LogFile"
			Say "  历史        $History（完整动作清单，带等价命令）"
			Say ''
			Say '  校验'
			Say "    $DestBin --version   -> $(Version-Tag $DestBin)"
			if ($script:ServiceChosen) { Say "    Get-ScheduledTask -TaskName $TaskName   -> $(Task-State)" }
			Say ''
			Say '  接下来'
			Say "    schtasks /Query /TN $TaskName /V /FO LIST"
			Say '    重跑安装脚本可以随时看现状（它会先报再问；选 4 退出，什么都不改）'
		}
		'uninstall' {
			Head2 '[已卸载]'
			Say "  服务        $(switch (Task-State) { 'installed' { '计划任务已注册' } 'absent' { '没有计划任务' } default { '读不到计划任务（要管理员）' } })（$TaskName）"
			Say "  二进制      $(if (Test-Path $DestBin) { "还在：$DestBin" } else { "已删除：$DestBin" })"
			if ($script:DelConfigs) { Say '  配置        已按你的选择删除' } else {
				Say "  配置        保留：$UserConfig、$RunConfig"
				Say "              要删：Remove-Item $UserConfig, $RunConfig"
			}
			Say "  服务日志    $LogFile（留着；要删：Remove-Item $LogFile）"
			Say "  历史        $History"
		}
		'none' {
			Head2 '[退出]'
			Say "  什么都没做，也没写历史。配置在那里：$UserConfig"
			Say '  改完重跑脚本即可。'
		}
	}
}
# ───────────────────────── 主流程 ─────────────────────────
# 已经被 UAC 升权重跑（带答案进来的）：不重复展示、也不再问，直接执行。
$ElevatedAction = $ActionArg
if (-not $ElevatedAction) { $ElevatedAction = $env:SOL_INSTALL_ACTION }
if ($ElevatedAction) {
	$script:Child = $true
	$env:SOL_INSTALL_ELEVATED = '1'
	$svc = $ServiceArg
	if (-not $svc) { $svc = $env:SOL_INSTALL_SERVICE }
	$script:ServiceChosen = ($svc -eq 'yes')
	# 卸载的"配置删不删"父进程已经问过了：直接用带过来的答案，别在这个窗口里再问一遍。
	$dcArg = $SolDelConfigs
	if (-not $dcArg) { $dcArg = $env:SOL_INSTALL_DELCONFIGS }
	if ($dcArg) { $script:DelConfigsGiven = $true; $script:DelConfigs = ($dcArg -eq 'yes') }
	# 行式进度：父进程边等边念（见 Escalate-IfNeeded）。放在最前面，连下面三行也进得了记录。
	$script:ProgressFile = Join-Path $RunDir "elevated-$ElevatedAction.progress"
	Remove-Item -Path $script:ProgressFile -Force -ErrorAction SilentlyContinue
	# 进度条在没人看的窗口里只会拖慢：关掉。
	$ProgressPreference = 'SilentlyContinue'
	# 真机现场（花了整整几轮才认出来）：用户点一下这个窗口，Windows 控制台的“快速编辑”就进入标记模式，
	# conhost 不再读取本进程的输出、本进程的写操作随之阻塞——看起来就是“三行之后一片空白、按回车才继续”。
	# 子进程一进来就把自己控制台设成“不许快速编辑”，从根上避免。失败不影响执行（老系统/受限环境）。
	try {
		if (-not ('Sol.ConsoleGuard' -as [type])) {
			Add-Type -Namespace Sol -Name ConsoleGuard -MemberDefinition @'
[DllImport("kernel32.dll", SetLastError=true)] public static extern IntPtr GetStdHandle(int nStdHandle);
[DllImport("kernel32.dll", SetLastError=true)] public static extern bool GetConsoleMode(IntPtr h, out uint mode);
[DllImport("kernel32.dll", SetLastError=true)] public static extern bool SetConsoleMode(IntPtr h, uint mode);
public static bool DisableQuickEdit() {
    const uint ENABLE_QUICK_EDIT_MODE = 0x0040;
    IntPtr h = GetStdHandle(-10);
    uint mode;
    if (!GetConsoleMode(h, out mode)) return false;
    return SetConsoleMode(h, mode & ~ENABLE_QUICK_EDIT_MODE);
}
'@
		}
		if ([Sol.ConsoleGuard]::DisableQuickEdit()) { Note-Progress '（已关掉这个窗口的“快速编辑”，点它不会再冻住）' }
	} catch { }
	# 这里原先还开 Start-Transcript，事后由父进程打印。真机上它在升权窗口里卡死过——用户看到的就是
	# "三行之后一片空白"，而子进程一直不结束、父进程一直等。行式进度文件既能实时读、也够留档，播录去掉。
	Say "（已用管理员权限重跑：$ElevatedAction）"
	Say "  工作目录    $RunDir"
	Say "  执行的动作  $ElevatedAction（服务 $($script:ServiceChosen)）"
	# 升权那条路上 Never 走到下面那句 Ensure-RuntimeConfig（它只在无人能答的正常主流程里调用），
	# 少了这一句，升权进程会对着一个不存在的运行配置做预检、报"没法预检"然后什么都不装。
	Ensure-RuntimeConfig
	try {
		switch ($ElevatedAction) {
			'install' { Invoke-Install; if ($script:Noop) { Say '（什么都没改。）' } else { Show-Report 'install' } }
			'uninstall' { Invoke-Uninstall; Show-Report 'uninstall' }
			default { Die "未知的 SOL_INSTALL_ACTION：$ElevatedAction" }
		}
	} catch {
		Say "出错了：$_"
		Say $_.ScriptStackTrace
	}
	exit 0
}
Ensure-RuntimeConfig
Banner
Show-State
Show-Config
$script:Noop = $false
if ($LedgerExists) {
	$stText = switch (Task-State) { 'installed' { '计划任务已注册' } 'absent' { '没有计划任务' } default { '读不到计划任务（要管理员）' } }
	$instTag0 = if ($LedgerJson.installed_version) { $LedgerJson.installed_version } else { '版本未知' }
	Say "检测到已安装 $instTag0（服务：$stText）。"
	Show-Menu
	$choice = Ask-Choice '请选择'
	switch ($choice) {
		'uninstall' {
			# 先问、再升权：答案随内部参数带过去，子进程不会再问（它的窗口不一定在你眼前）。
			$script:DelConfigs = Ask-Yes '配置文件也一起删掉吗？' 'n'
			$script:DelConfigsGiven = $true
			$env:SOL_INSTALL_DELCONFIGS = if ($script:DelConfigs) { 'yes' } else { 'no' }
			Escalate-IfNeeded 'uninstall'; Invoke-Uninstall; Show-Report 'uninstall'
		}
		'regenerate' {
			Remove-Item -Path $UserConfig -Force -ErrorAction SilentlyContinue
			Write-UserConfig
			Say "已重新生成：$UserConfig（重新运行脚本就会按它执行）"
		}
		'quit' { Show-Report 'none' }
		default {
			$script:ServiceChosen = ($LedgerJson.service.created_unit -eq $true)
			# 台账说没建过任务，而任务也确实不在（可能上次没跑完）：再问一次，
			# 否则"我想装服务"在这条路上永远没机会说出口。
			if (-not $script:ServiceChosen -and -not $env:SOL_INSTALL_ROOT -and (Task-State) -ne 'installed') {
				$script:ServiceChosen = Ask-Yes '把 sol 注册成开机启动的计划任务（后台常驻）？' 'y'
			}
			Open-ConfigDir (Split-Path -Parent $RunConfig) $RunConfig
			Escalate-IfNeeded 'install'
			Invoke-Install
			if ($script:Noop) { Say '（什么都没改。想强制重写计划任务就选 3 重新生成配置，或先卸载。）' }
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
Open-ConfigDir (Split-Path -Parent $RunConfig) $RunConfig
Escalate-IfNeeded 'install'
Invoke-Install
if ($script:Noop) { Say '（什么都没改。想强制重写计划任务就选 3 重新生成配置，或先卸载。）' }
else { Show-Report 'install' }