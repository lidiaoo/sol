# 验证通道：手上没有那个系统时怎么验

一个 bug 只在**真机**上现形的，就别在沙箱里找第二遍；按下面选通道，并给每类坑配一条回归断言。

## 1) 真 Windows（在 Windows 上工作时就是本机）

- shell 是 git-bash：**native 程序（powershell / python）拿不到 MSYS 的 `/tmp/...` 路径**——一律传 `C:/...` 正斜杠的 Windows 路径。
- 在 bash 里写 `\.\install.ps1` 会被反斜杠转义成 `.install.ps1`；用 `./install.ps1`。
- 喂答案：`printf 'y\ny\n' | powershell -NoProfile -ExecutionPolicy Bypass -File ./install.ps1`。
- **输出是 GBK**（偶尔混进无效字节）：`| iconv -c -f GBK -t UTF-8`（`-c` 不能省，否则整段转换失败、断言全红）；`>` 重定向出来的可能是 UTF-16LE，用 python `decode(errors='replace')` 最省事。
- 别把 PS 辅助函数取成单字母（`H` 撞 `h`→`Get-History`，满屏噪音）。
- 语法体检用**真解析器**：`[System.Management.Automation.Language.Parser]::ParseFile($p, [ref]$null, [ref]$e)`；0 个错误才叫过（尾逗号那类错只有它能抓）。

## 2) 逻辑级 PowerShell 冒烟（本机不是 Windows 时）

- 便携版 pwsh：下 `powershell-*-osx-x64.tar.gz` 解压即用，不用装包、不用管理员。
- Windows-only 的 cmdlet/命令用 **PowerShell 函数桩**（在 runner 里 `function schtasks {...}` 再 `& ./install.ps1`），不要放 PATH 上的可执行文件桩：ps1 按 Windows 规矩 `$env:PATH -split ';'`，而 pwsh 在 Unix 上按 `:` 找命令，两者会打架。
- 真二进制改名 `sol.exe` 就能在 Unix 上被执行（不看扩展名），预检能真跑。

## 3) 真 Linux（手上没有 Linux 机器时）

- `kern.hv_support=0` 的机器上 Lima / Docker Desktop 都起不来；**qemu 纯软件模拟（TCG）能起**：cloud-init 种子盘（`hdiutil makehybrid -iso -joliet -default-volume-name CIDATA`）+ qcow2 叠加盘 + `-netdev user,hostfwd=tcp::2222-:22`，SSH 进去就有**真 systemd + 免密 sudo**，能跑完装/升级/卸载全链路。
- 用 `-gcflags="-N -l"` 再编一个**字节不同**的二进制来触发真"升级"路径（同一个 tar 的 sha256 变了才会走覆盖而不是"已是最新"）。

## 4) macOS 真机

- 沙箱全生命周期随便跑；写 `/usr/local/bin` 与 LaunchDaemon 要**用户密码**——需要 sudo 的那条路别硬跑（会卡在提示上），交给用户或 CI。

## 5) CI 三平台 matrix

- `.github/workflows/install-smoke.yml`：一次性 runner 上真装（真 root / 真管理员）→ 断言服务 active、真在监听、台账 `incomplete: false` → 幂等重跑 → 换二进制升级（"先停掉自己那份"）→ 卸载没留我们建的东西。
- 每修一个只在真机现形的 bug，就往对应 job 加一条断言（例如"现状必须认出正在跑的那份"）。
- CI 才是"修好了"的证据；本机能跑的只是逻辑级。
