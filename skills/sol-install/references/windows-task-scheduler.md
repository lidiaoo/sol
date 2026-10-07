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
