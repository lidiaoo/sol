# Windows：计划任务这条路（实现与排错）

## 判"我们的任务在不在"

- 首选 `Get-ScheduledTask -TaskName <name>`：**未升权也能读**，还能顺带拿到 `State` / `Actions`（用来打那行"启动"）。找不到时它抛 `CimJobException`（"... objects found with property ..."），别的异常才是真出错。
- 退路：`schtasks /Query`（**不带** `/TN`）列出全部再按名字**精确**匹配——这一路未升权通常也是放行的。
- **不要**用 `schtasks /Query /TN <name>` 或 `/TN <name> /XML` 判存在：任务不在时它们回"拒绝访问"（退出码 1）。照着退出码非 0 就判 `absent`，会把权限问题说成"不存在"（用户会立刻反驳"明明有"）。
- `schtasks` 的文本是**本地化**的：不要按输出文字做判断，只认退出码，或干脆走 cmdlet。

## Test-Path 的雷

`Test-Path C:\Windows\System32\Tasks\<name>` 在未升权时会抛 `UnauthorizedAccessException`（拒绝访问），而脚本里 `$ErrorActionPreference='Stop'` 会把它变成**终止错误**，整只脚本当场退出（退出码 1）。任何对系统路径的 `Test-Path` / `Get-Item` 都要 `-ErrorAction SilentlyContinue` 或包在 try/catch 里；判任务存在请用上面的 cmdlet 路子。

## 注册

- `schtasks /Create /TN <name> /TR "<exe> <args>" /SC ONSTART /RU SYSTEM /F`；部分 Windows 上 `/RL HIGHEST` 与 `/RU SYSTEM` 不能同时给 → 失败就去掉 `/RL HIGHEST` 重试一次（保留这条降级）。
- `/TR` 里带空格的路径要用双引号包起来（`"C:\path with space\sol.exe" listen ...`）；直接跑 exe 的好处就是这里不需要包装脚本、也没有嵌套引号。
- 建完立刻复核（`Get-ScheduledTask`）；复核失败要显著报警 + 让台账记 `incomplete`。
- **任务会在我们背后消失**（被清理工具/优化软件/人手动删）。产品不能假设"我建过 ⇒ 它还在"，只能每次读现实、并在"台账说有、现实说无"时把这句话讲给用户听。
- 删任务只删我们自己台账里的那个名字；没有台账的同名任务＝别人的，报告出来让人决定。

## 日志落在哪

- 计划任务的 XML **没有**重定向能力；任务又常以 SYSTEM 在无窗口会话跑，stdout 无人接管。
- 别用 `cmd /c "... >> log 2>&1"` 塞进 `/TR`（嵌套引号地狱），也别包 `.cmd`（任务里看不见真正的 exe 和参数）。
- 正道：让 sol 自己写文件——运行配置里补一节 `logging: { output: file, file: <安装目录>\sol.log }`。sol 的文件日志是**追加、0600**，实测在 Windows 上正常落盘。
- 追加实现要点：配置里已有 `^\s*logging\s*:` 就**一个字都别动**；追加前确保原文件以换行结尾；用 `[System.IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($false)))` 写回（PS 5.1 的 `-Encoding utf8` 带 BOM）。

## 沙箱（SOL_INSTALL_ROOT）

- 沙箱里**绝不碰本机任务管理器**：连 `schtasks /Create`、`/Delete` 都不许——那会在宿主机建一个指向沙箱路径的任务。所有服务侧动作都要有 `if ($env:SOL_INSTALL_ROOT) { return }` 这类护栏，冒烟里再断言"沙箱里没有 /Create 或 /Delete"。
- 沙箱只验逻辑（配置生成、台账、文案、解析、卸载文件处理）；真任务的注册/权限/防火墙只能靠真机或 CI。

## UAC 那一跳：看起来卡死，其实是它在等对话框

- 安装/卸载需要管理员时，脚本用 `Start-Process -Verb RunAs -Wait` 重跑自己。**`-Wait` 会一直等
  UAC 对话框被回答**——对话框没被看到（藏在窗口后面、别的桌面、任务栏里）时，原控制台一动不动，
  用户报的就是"脚本没反应"。
- 判断"升权子进程到底起没起"的硬判据：安装目录里有没有 `elevated-<action>.log`。子进程一进去第一
  件事就是开 transcript；**这个文件不存在 = 子进程从没运行 = 卡在 UAC 授权那一步**。
- **要问的问题必须在父进程问**，答案经**内部参数**（如 `-SolDelConfigs`）带给子进程：子进程有自己的
  窗口，在它里面提问用户可能根本看不见（父进程却在等它回答）；而且环境变量过 UAC 不保证继承。
- 弹 UAC 之前把话说清楚，并且**别用 `-Wait` 干等**：自己轮询、每 10 秒报一次"还在等授权框"，否则用户看到的是
  "卡死没响应"。真机上还出现过"UAC 框根本没弹、控制台无声卡住"：所以文案里要给出出路（按 Ctrl+C，然后右键
  `install.cmd` 选「以管理员身份运行」，那条路不需要中途授权）。
- **升权子进程一律不提问**：它有自己的窗口，可能不在用户眼前；在那里 `Read-Host` 等人按键，父进程会一直等。
  要问的在父进程问，答案经**内部参数**带进去（环境变量过 UAC 不保证继承）。真机上前两次"卡死"都出在这里。
- **`sol` 计划任务归安装脚本管，不看是谁建的**：卸载一定删；安装时已存在就**先删再重建**（不做就地覆盖，
  覆盖会留下旧主体/旧触发器/旧 ACL）。规矩来自用户，别再加"要不要接管"这类门槛问句。
- 用户侧的替代路径（最省事）：右键 `install.cmd` →「以管理员身份运行」，全程不需要 UAC 对话框。

## 下错架构的包：报错是"指定可执行文件不是此操作系统平台的有效应用程序"

- 在 x64 机器上解 `windows-arm64` 的包（或 Intel Mac 上跑 `darwin-arm64`）时，报错完全看不懂。
  安装脚本现在**先读二进制头**：PE 的 Machine（ps1）、ELF `e_machine`／Mach-O `cputype`（sh）。
  架构不符 → 现状块就提示"这份装不起来"，预检处直接停下并说清该下哪个包。
- 写这类判定时注意：**判定函数要能返回空**（不是认识的格式就不判），否则 MSYS/Cygwin 这类环境下
  会把"看不懂"误报成"架构不对"。

## SYSTEM 身份的任务：非管理员"看不见"它，而且会被静默跳过

- 我们的任务用 `/RU SYSTEM /SC ONSTART` 注册 → 任务定义 `C:\Windows\System32\Tasks\<名字>` 的 ACL
  只给 SYSTEM 与管理员。**非管理员的 `Get-ScheduledTask` 枚举会直接跳过读不到的任务**（不是报错），
  于是"不在列表里"被当成"不存在"——真机上任务在、sol 在跑，现状却报"没有"。
- 判别"到底有没有"的两个免权限办法（都不依赖中英文文案）：
  1. `schtasks /Query /TN <名字>` 与 `schtasks /Query /TN <随机乱名>` 的输出**不一样 = 存在**（一样 = 没有）；
  2. `[System.IO.File]::OpenRead("…\Tasks\<名字>")`：`UnauthorizedAccessException`（取 InnerException）
     = 有但读不到、`FileNotFoundException` = 真的没有。
- 但按项目要求，最简规矩是**非管理员不猜**：直接说"读不到内容，要管理员才看得到"。现状块是只读的，
  不该为了看一眼就弹 UAC；升权那一步（装/卸）之后什么都看得见。

## Windows 上"休眠期间时钟不停"会坑掉 resume 判定（运行期，不是安装期）

- 判定"机器刚从 suspend 回来"的常见手法是"wall clock 增量 − 单调钟增量"：Linux/macOS 的
  CLOCK_MONOTONIC 休眠期间不走，这个差就是睡眠时长。**Windows 上两者都不停**——Go 的单调钟是
  `QueryPerformanceCounter`，standby/hibernate 期间照样计数——差值恒为 0，判定永远不触发。
- Windows 的正解是系统自带的那对计数器：`GetTickCount64`（**含**休眠）− `QueryUnbiasedInterruptTime`
  （**只算工作态**）= 睡眠时长。两个调用都在 kernel32，不需要新依赖（`syscall.NewLazyDLL` + `unsafe`）。
- 写这类判定的测试时注意：**"醒着时不漂移"必须有一条真测试**（两计数同步走），否则会误报 resume 把
  正常功能全挡掉；而"真的休眠过一次"只能在愿意休眠的机器上验，本机不该拿真实服务去 suspend。

## 真机「卡住、按回车才继续」的最终答案（别再猜窗口/点击/快速编辑）

- 真凶是**原生命令在 stdin 上等人按键**：`schtasks /Delete` 的确认提示就是典型。封装 `& $Exe @Argv 2>&1`
  只重定向了 stdout/stderr，没管 stdin → 提示就在控制台输入队列上等着。
- 规矩：调原生命令一律 `$null | & $Exe @Argv 2>&1`（空 stdin → 立刻 EOF → 取默认值返回）。判据：
  `$null | cmd /c pause` 应 ~20ms 返回。
- 同源的两条：升权子进程**不许提问**（窗口可能不在眼前）；**别用 WMI**（`Get-CimInstance` 会无限挂）。
- 定位这类问题的唯一可靠办法：让看不见的进程写**行式进度文件**（带时间戳、可边跑边读），父进程边等边念；
  最后一行就是卡住的位置。完整清单见仓库 `docs/install-pitfalls.md`。
