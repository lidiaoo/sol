# SoL - Shutdown on LAN（局域网关机）

**简体中文** | [English](README.en.md)

SoL 是一个监听 Wake-on-LAN 魔法包的服务：当收到的包命中配置好的规则时，就触发一个动作——
关机、重启、睡眠、执行命令、调用 webhook，或这些动作的有序组合。它是 Wake-on-LAN 的反向用法：
包是触发信号，"收到包的那台机器"负责执行。

规则可以按端口、目标 MAC、网卡、来源网段和包内容匹配，所以一个实例就能同时服务多个发送方、
多张网卡和多种语义。

包可以用三种方式找到它，任选其一都能用：

- **UDP** —— 魔法包本身：按端口、按网卡、按来源网段、按包内容、按 HMAC 匹配（局域网里发过来的那种）。
- **HTTP** —— 控制面，默认关闭，只绑 `127.0.0.1` 并强制令牌（脚本或另一台机器发过来的那种）。
- **远程命令** —— 转发一个或多个 UDP 包，默认关闭（你的某个 action 往外发的那种）。

只想先看它动一下，两分钟：

1. 拿到可执行文件：[Install](#installation)（或者解开一个发布包，或[自己编译](#build-from-source)）。
2. 写一份最小配置——一个端口、一个动作；也可以跳过文件，直接用
   [Simple mode](#simple-mode-command-line)。
3. 给自己发一个魔法包：`curl`、`nc`，或者 [Testing your setup 里那条 PowerShell 命令](#testing-your-setup)，
   然后看着日志里跳出那一行。

## 多种使用方式

照你想做的事挑一行。每一行都指向讲它的那一节 —— 没有死路，每条都能走到一个能用的服务。

| 我想…… | 从哪开始 | 最后得到什么 |
| --- | --- | --- |
| 让这台机器能被局域网关机 / 重启 / 睡眠 | [Install](#installation) —— 跑安装脚本，它会把该问的都问全（零参数、编号菜单） | 开机自启的服务（`systemd` / `launchd` / 计划任务）和一份你能改的 `sol.yaml` |
| 先试一下，不动这台机器的服务 | [Quick start](#quick-start) 与 [Installation](#installation) | 手工跑着的 `sol listen`；`--dry-run` 只打印它打算做什么，不动手 |
| 只用命令行参数跑，不要配置文件 | [Simple mode](#simple-mode-command-line) —— `sol listen --port 10010 --iface eth0` | 同一个守护进程，全靠命令行配置 |
| 用 Home Assistant 驱动它——事件推出去、按钮自动化接进来 | [Home Assistant](#home-assistant) —— 两个方向各一个 webhook，需要特权的那半留在 sol 里 | 面板里能看到事件，一个按钮就能让某台机器睡眠或唤醒 |
| 用 HTTP 触发动作——脚本、面板、另一台机器 | [Control plane](#control-plane) —— 默认关着，改一个字段就能开；curl 调用在[那一节](#control-plane)里 | `POST /v1/actions/<name>` 等端点，只绑 `127.0.0.1`，带令牌 |
| 要精确匹配：多个端口、多张网卡、包内容、自定义动作 | [Configuration file mode](#configuration-file-mode) | 一份按端口、网卡、来源网段、包内容、HMAC 匹配的 `sol.yaml`，还能跑你自己的命令 |
| 自己编译，或自己出发布包 | [Build from source](#build-from-source) / [Installation](#installation) | 各平台的 `sol`，以及可以直接发布的 `dist/` 包 |

```bash
sh install.sh                                     # or unpack a release; the menu asks everything
sol listen --config sol.yaml --dry-run            # watch the matches without acting on them
curl -s http://127.0.0.1:8080/healthz             # control plane: on with server.http.enabled
curl -s -H "Authorization: Bearer $SOL_TOKEN" http://127.0.0.1:8080/v1/status
```

上面四条命令就是整个产品的第一屏：装上、看一眼、用 HTTP 调它。完整的 curl 集合——每个端点、每种回答
是什么意思、以及在 Linux / macOS / Windows 上怎么把令牌交给服务——都在下面的
[Control plane](#control-plane) 里。

## 目录

- [Description](#description) —— 它监听什么、能做什么、支持哪些平台
- [Quick start](#quick-start) —— 解开、跑起来、看它工作
- [Installation](#installation) —— 安装脚本、发布包、从源码编译、装完自检
- [Run the service](#run-the-service) —— 参数、配置文件、规则、动作、控制面
- [Running as a service](#running-as-a-service) —— `systemd`、`launchd`、计划任务
- [Home Assistant](#home-assistant) —— 两个方向，以及为什么这样分工值得
- [Ports and privileges](#ports-and-privileges) —— 端口号为什么决定要不要 root
- [Choosing interfaces](#choosing-interfaces) —— 监听哪几张网卡，以及怎么列出来
- [Testing your setup](#testing-your-setup) —— 发一个包、读它的回答
- [Troubleshooting](#troubleshooting) —— 用户真会撞上的那些消息
- [Security Notes](#security-notes) —— 对外暴露任何东西之前先读它
- [Migration notes (breaking changes)](#migration-notes-breaking-changes)
- [Inspiration](#inspiration)、[Attribution](#attribution)、[License](#license)

## Description

sol 用 [cobra](https://github.com/spf13/cobra) 做 CLI，其余是 Go 标准库。除标准库外只依赖
`yaml.v3`（配置）与 `testify`（测试）。

完整设计——路由模型、配置结构、安全模型，以及每个功能的"已实现/待实现"状态和真机冒烟证据——
都在 [docs/routing-design.md](docs/routing-design.md)；进度记录在 [TODO.md](TODO.md)。

### Listen

`sol listen` 在配置的网卡上（默认是自动选出的全部合格网卡）监听 Wake-on-LAN 魔法包，命中规则
就触发动作。规则来自配置文件；一旦给了 `--port`，就完全由命令行接管整套规则。

### Protocol

服务在 UDP 端口上监听 Wake-on-LAN 魔法包。魔法包 = 6 个 `0xFF` 后面跟着目标 MAC 重复 16 次
（102 字节）；配了 SecureOn 时，尾部再跟 6 字节口令（108 字节）。

- 端口 **7** 和 **9** 保留给纯 WOL：只接受不带负载的裸魔法包，且只能执行 `noop`——其它情况启动
  即报错。`--allow-reserved-actions` 或 `security.allow_reserved_port_actions: true` 可按需恢复
  旧行为。
- 其它端口上，规则还能进一步匹配包内容（`content`：suffix / prefix / any）、来源 CIDR 和来源 MAC。

### Supported actions

| 类型 | 作用 |
| --- | --- |
| `noop` | 只记录命中（保留端口的默认动作） |
| `power.sleep` | 挂起机器 |
| `power.shutdown` | 关机 |
| `power.reboot` | 重启 |
| `exec` | 执行配置好的命令（argv，默认不走 shell；可选 `shell: true`、命令白名单、超时、workdir、env，以及 `user`/`group` 降权） |
| `http` | 调用 webhook（method、url、headers、body、timeout、retries、proxy；目的地受 `security.url_allowlist` 限制） |
| `sequence` | 把上面这些按顺序串成一个动作（某一步失败不会跳过它后面的步骤） |
| `wol.send` | 唤醒另一台机器：向固定目标发魔法包（`mac`，可选 `broadcast`、`port`、`secure_on`、`repeat`、`interval`） |
| `remote:<id>` | 白名单里的远端命令，注册成普通动作 |

sol 也能唤醒**别的**机器。`wol.send` 向固定目标发魔法包（`mac` 是唯一必填项；`broadcast` 默认
`255.255.255.255`，`port` 默认 9，`repeat` 默认 1，`interval` 默认 100ms）。可以由规则触发、作为
`sequence` 的一步，或在控制面用 `POST /v1/actions/wake-nas` 触发。目标 MAC 只能来自配置——sol
绝不会从触发包里取——并且它和其它动作一样走 cooldown、限流、dry-run 与审计日志。

信任阶梯见设计文档 §20：只记日志 < 出站唤醒 < 睡眠/锁屏 < 关机/重启 < 自定义命令 < 出站 HTTP <
远端原始命令（默认关闭）。

### Platform support

监听这一侧各平台完全一样：每个端口一个 UDP socket、绑 `0.0.0.0`，后面跑同一套规则引擎。平台之间
真正的差别是电源动作和特权规则。

| | Linux | macOS | Windows |
| --- | --- | --- | --- |
| `power.shutdown` | `shutdown -h now` | `shutdown -h now` | `shutdown -s -t 0 -f` |
| `power.reboot` | `shutdown -r now` | `shutdown -r now` | `shutdown -r -t 0 -f` |
| `power.sleep` | `systemctl suspend` | `pmset sleepnow` | `rundll32 powrprof.dll,SetSuspendState 0,1,0` |
| 保留端口 7/9、以及任何 <1024 的端口 | 需要 root 或 `CAP_NET_BIND_SERVICE` | 需要 root | 没有特权端口的概念 |
| `exec` 带 `user:`/`group:` | 支持（需要 root） | 支持（需要 root） | 不支持：`exec user/group requires root` 只在 unix 上存在 |
| `exec`、`http`、`sequence`、`wol.send`、控制面、重载、各类护栏 | 支持 | 支持 | 支持 |

其它系统（比如 BSD）也能编译、也能监听；只是电源动作会报 `unsupported operating system`，那种平台上
用 `exec` 动作来做。

有件事值得直说：sol 作用在**它自己运行的那台机器**上，而且只在它运行时生效。它是"收到 WoL 就关机"的
接收端，不是叫醒一台睡着机器的东西。

## Quick start

1. **先看哪几张网卡会应答。** `sol ifaces` 里标 `AUTO yes` 的就是：这是这台机器的**身份**（它真实的
   网卡），不是"此刻恰好 up 的网卡"的快照。

2. **写一份配置。** sol 先看 `/etc/sol/sol.yaml`，再看 `~/.config/sol/sol.yaml`——三个平台都是这两个
   位置，`~` 就是你的主目录。要放别处用 `--config`（或 `$SOL_CONFIG`）指过去；显式给的路径不存在
   会**报错**，不会静默回退。一个都没有时它**拒绝启动**（`no rules configured`），而不是假装在监听。

   ```yaml
   version: 1
   rules:
     - match: { ports: [10010] }
       action: power.shutdown
   ```

3. **先无副作用地试一遍。** `--dry-run` 只把"本来会发生什么"写进日志、什么都不做；它同时也是确认
   "包到底有没有被匹配上"的最快办法。

   ```bash
   sol listen --config /etc/sol/sol.yaml --dry-run
   ```

4. **从另一台机器发一个魔法包**，然后看日志里有没有
   `magic packet matched ... action=power.shutdown`——见 [Testing your setup](#testing-your-setup)。

5. **装成服务**，这样重启后还在、没人登录时也跑——见 [Running as a service](#running-as-a-service)。

6. **确认跑起来的是什么**：`sol --version`、`journalctl -u sol.service -f`（Linux）、
   `tail -f /usr/local/var/log/sol.log`（macOS）、`Get-Content -Wait C:\ProgramData\sol\sol.log`
   （Windows；计划任务直接跑 `sol.exe`，日志由 sol 自己写进这个文件——运行配置里的 `logging` 那一节），
   或者控制面开着时的 `GET /v1/status`。安装脚本的"现状"和完成报告里也各有一行告诉你日志在哪。

密钥绝不进 YAML：放进环境变量（`SOL_TOKEN`、`SOL_CMD_KEY`、`SOL_PACKET_KEY`），或放进只有服务账号
能读的文件里，再由配置引用它。

## Installation

### Quick Install（零参数安装脚本）

一条命令，没有任何参数。它先认出现状、把它准备做什么打印出来，动手之前先问你。

**Linux / macOS：**
```console
curl -fsSL https://github.com/lidiaoo/sol/releases/latest/download/install.sh -o install.sh && sh install.sh
```

**Windows**（PowerShell；也可以直接双击随附的 `install.cmd`）：
```console
irm https://github.com/lidiaoo/sol/releases/latest/download/install.ps1 -OutFile install.ps1; powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1
```

它会用你**已有的** `sol`（脚本旁边、当前目录、`PATH` 里），不会拿下载的东西去覆盖它。它在**你执行
脚本的那个目录**里写 `install.yaml`，内容只有"sol 怎么跑"；同时保证有一份能跑的运行配置：纯包 → 关机
（端口 11）、magic+"reboot" → 重启（12）、magic+"sleep" → 睡眠（10）。服务没有规则会拒绝启动，所以
脚本直接写一份能跑的。它还把两个"重复包护栏"**写在文件里**——`security.settle` 与三个电源动作各 5s 的
冷却：默认值一眼看得见，也一眼知道怎么关掉。**这份配置放哪，平台不同**：Linux/macOS 就是执行目录里的 `sol.yaml`；Windows 放在
**安装目录**（`C:\ProgramData\sol\sol.yaml`，跟 `sol.exe` 做伴）——计划任务以 SYSTEM 开机就跑，配置不该
依赖一个可能被挪走的项目目录；你在执行目录里改过的那份会被**原样拷过去**，不会丢。改配置就编辑它、再
重跑一遍脚本。
**端口 <1024 在 Linux/macOS 上要 root**：脚本动手前会升权，正常安装不用管；但如果是不要 root 的
用户级安装，要把端口改成 ≥1024。
它把现状、配置在哪都打印出来，再问两个问题：要不要装成服务、是否继续。你说"是"之前什么都不改。

**权限**：它不会一开始就以 root 跑。轮到需要权限的步骤（写 `/usr/local/bin`、注册服务）时，Linux/macOS
上它用 `sudo` **重跑一遍自己**，Windows 上弹 **UAC** 以管理员身份重跑，并把你刚才的答案带过去——不会
再问第二遍。你要是拒绝了，它就停下来说明原因，而不是装到一半。

**重跑脚本**就是看现状、改 sol 的跑法（改文件再跑一遍）、或卸载（选 `2`；它会先把还在跑的 sol 停掉，删配置前还会再问一次）。
它做的每一件事都追加写进 `install.log`（带等价命令），和台账 `install.json` 放在一起。

东西落在哪：二进制 `/usr/local/bin/sol`（Windows `C:\ProgramData\sol\sol.exe`），台账与历史
`/usr/local/share/sol/`（Windows `C:\ProgramData\sol\`），运行配置 `sol.yaml` 在执行脚本的那个目录
（Windows `C:\ProgramData\sol\sol.yaml`——跟二进制做伴，计划任务读的就是它）；只有你选了装服务才会多一个
systemd 单元 / launchd plist / 计划任务。你自己已有的配置与日志一个都不碰（没有才写一份）。

这份脚本三平台通用，但目前只在 Linux 上真机跑过；macOS 与 Windows 分支的证据强度记在
[docs/install-design.md](docs/install-design.md)（等 CI 覆盖）。

### Download the release directly（自己下 release）

**Linux AMD64:**
```bash
curl -L https://github.com/lidiaoo/sol/releases/download/{newest}/sol-{newest}-linux-amd64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**Linux ARM64:**
```bash
curl -L https://github.com/lidiaoo/sol/releases/download/{newest}/sol-{newest}-linux-arm64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**macOS Intel:**
```bash
curl -L https://github.com/lidiaoo/sol/releases/download/{newest}/sol-{newest}-darwin-amd64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**macOS Apple Silicon:**
```bash
curl -L https://github.com/lidiaoo/sol/releases/download/{newest}/sol-{newest}-darwin-arm64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**Windows (PowerShell):**
```powershell
Invoke-WebRequest -Uri "https://github.com/lidiaoo/sol/releases/download/{newest}/sol-{newest}-windows-amd64.zip" -OutFile "sol.zip"
Expand-Archive -Path "sol.zip" -DestinationPath "." -Force
```

> 上游最后一个 tag 是 v0.0.2，配置文件、额外动作与端口 9 的变更都在它之后；升级既有安装前请先读
> [CHANGELOG](CHANGELOG.md)。

**自己出发布包**：一条命令，Linux / macOS / Windows（Git Bash）上完全一样，见下面的
[自己生成 dist 发布包](#自己生成-dist-发布包)。Go 交叉编译，一台机器就能出全六份；CI 用 goreleaser 出同一套（名字、内容一致）。

### 自己生成 dist 发布包

`scripts/release.sh` 把 CI 发的那套包写进 `dist/`。三个平台**任选一台**都能跑——Go 交叉编译，
不需要为每个目标平台各准备一台机器：

```bash
sh scripts/release.sh v0.1.0
```

```text
dist/sol-v0.1.0-linux-amd64.tar.gz      dist/sol-v0.1.0-windows-amd64.zip
dist/sol-v0.1.0-linux-arm64.tar.gz      dist/sol-v0.1.0-windows-arm64.zip
dist/sol-v0.1.0-darwin-amd64.tar.gz     dist/checksums.txt
dist/sol-v0.1.0-darwin-arm64.tar.gz     dist/install.sh  dist/install.ps1  dist/install.cmd
```

各平台怎么跑——命令完全一样，只是**在哪个 shell 里敲**不同：

| 平台 | 这样跑 | 说明 |
| --- | --- | --- |
| Linux | `sh scripts/release.sh v0.1.0` | 需要 `git` 和 `go`；只有要出 Windows 包时才需要 `zip` |
| macOS | `sh scripts/release.sh v0.1.0` | 同上；有 `file` 的话会多做一遍格式/架构核对（可选） |
| Windows | 开 **Git Bash**：`cd /e/Common/Project/GolandProjects/sol && sh scripts/release.sh v0.1.0` | 原装的 Git for Windows 就够，不用装 WSL |

Windows 上没有 `zip` 时，`.zip` 会走 PowerShell 的 `Compress-Archive` 生成；没有 `file` 时，格式/架构
核对会跳过并说明。两样都不影响出包。`make` 里**故意没有**这个目标——打包和构建是两件事。

每个包解出来就是一个目录，也就是"执行目录"：二进制、三个安装脚本、中英 README、CHANGELOG、
LICENSE、`sol-example.yaml`、`sol.schema.json`，以及 `skills/`（Hermes 的 `sol-install` 技能）。
解包进去跑安装脚本即可，没有别的要配。

选项：

- `sh scripts/release.sh` 不带版本号时取 `git describe --tags --always`，并且**拒绝脏树**——只算已跟踪
  文件的改动，未跟踪的 `dist/`、`.idea/` 不算；`--allow-dirty` 可以明确接受。
- `--platforms "windows/amd64 linux/arm64"` 只出其中几个平台。
- `-h` 看用法。

它拒绝发出坏东西：每条二进制都核对格式与架构，包里的安装脚本必须与仓库里**逐字节一致**，技能目录
必须完整，最后还会在宿主平台上真跑一遍刚编出来的二进制（`--version`）。

CI 那边：在 GitHub 上**创建 release** 时，`.github/workflows/release.yaml` 用 goreleaser 出同一套包：

```bash
git tag v0.1.0 && git push origin v0.1.0
gh release create v0.1.0 dist/sol-* dist/checksums.txt dist/install.* --title v0.1.0
# (not dist/*: that would include the directory dist/stage/ and gh would try to upload it,
#  failing with "read dist/stage: is a directory")
```

### Build from source

```bash
make build          # or: go build .
make test
make lint
```

`make build` 用 `git describe` 打上版本号，`make build-static` 产出发布流水线那种形态（静态、
strip）。两个目标还会盖上"树脏不脏"的章，口径只算**已跟踪**改动——工具链自带的那个标记连未跟踪
文件也算，而出包过程本身就会写 `dist/stage/...`，于是干净的树也会被说成 `-dirty`。裸 `go build` 不需要打标：工具链自己会嵌入一个点明源码树的伪版本
（`v0.0.0-<时间戳>-<提交>`）以及提交号，`go install ...@v1.2.3` 则嵌入那个 tag。这些信息在
`-s -w -trimpath` 之后依然在，所以发布的二进制仍然说得出自己是谁。

### 安装脚本怎么用（零参数 · 编号菜单）

这些行为背后的真机踩坑记录集中在 [docs/install-pitfalls.md](docs/install-pitfalls.md)。

装完/升级/看现状/卸载，都用**同一个脚本、同一个入口**，不给它传任何参数：

| 平台 | 怎么跑 |
| --- | --- |
| Linux / macOS | `./install.sh`（或 `sh install.sh`） |
| Windows | 双击随附的 `install.cmd`，或 `powershell -ExecutionPolicy Bypass -File .\install.ps1` |

它**先报现状、再问你要做什么**，在你回答之前不改动任何东西。

#### 现状块每行是什么意思

- `二进制` / `版本`：你执行目录里那份；`PATH 上的 sol` 是系统里已有的那份。
- `已安装` / `台账`：脚本自己装的记录（`/usr/local/share/sol/install.json`；Windows `C:\ProgramData\sol\install.json`）。说"上次没装完"就再跑一次补上。
- `服务`：systemd 单元 / launchd 任务 / 计划任务在不在。Windows 还会多一行 `启动`，写的就是任务真正跑的命令行（可执行文件 + 参数），不用去任务计划里猜。
- `日志`：服务日志在哪看——Linux `journalctl -u sol.service -f`，macOS `/usr/local/var/log/sol.log`，Windows `C:\ProgramData\sol\sol.log`。
- `进程`：有没有 sol 在跑（按**可执行文件路径**认，不是按进程名）。没升权时只能按名字识别，它会说清楚"认不出是不是安装目录那份"。
- `权限`：当前不是管理员时，说明什么时候会要权限（Unix 用 sudo 重跑一遍自己；Windows 一次 UAC）。
- `运行配置`：服务真正读的那份配置。改它才是改行为；执行目录里那份只是"输入"。

#### 选项（回车 = 1）

    1  按配置应用（按 install.yaml 里的 run.args 装或升级）
    2  卸载（摘掉服务，按台账删掉自己建的东西）
    3  重新生成 install.yaml
    4  退出，什么都不改

#### 装完之后

- **改行为**：编辑运行配置 `sol.yaml`（听哪些端口、匹配什么、干什么），或者编辑 `install.yaml` 的 `run.args`（怎么起）。改完重跑脚本选 `1`。
- **升级**：把新的 `sol` / `sol.exe` 放到执行目录（跟脚本同一个目录），重跑选 `1`。它会先停掉自己那份服务再换二进制，换完再起——不会撞"端口占用"。
- **卸载**：选 `2`。服务、二进制、台账都清掉；`sol.yaml` / `install.yaml` 留不留由你答。
- **每一步都有台账**：`install.log` 记着每个动作和它的等价命令（想手工复核就照抄那行）。

#### 读不懂的现象怎么查

- **`服务 没有（计划任务 X 不存在）`，可台账说装过**：任务被删过、或被"优化/清理"工具动过。重跑选 `1` 会再建（脚本现在会在建完任务后复核一次，建不上就记成"没装完"并在报告里说）。
- **Windows 上 `服务 … 看不清`**：没管理员权限读任务列表。以管理员身份重跑一次就能看清。
- **`预检 被拒绝`**：端口占不上。多半是上一份还在跑（脚本会先停掉它再试一次）；也可能是系统把这段端口**排除**了（Hyper-V / WSL / Docker / 虚拟机常见）→ 换高端口，或 `net stop winnat && net start winnat`（重启后失效）。
- **Windows 上没有日志文件**：检查运行配置里有没有 `logging: { output: file, file: ... }`——计划任务是"无窗口"跑的，stdout 没人接，所以日志必须由 sol 自己写文件（安装脚本在你没写这一节时会补上一条，然后由 sol 写进 `C:\ProgramData\sol\sol.log`）。

- Windows 上以**非管理员**跑时，"服务"行会说"读不到内容（计划任务 sol）：当前不是管理员，要管理员才看得到"。这不是故障：计划任务是以 SYSTEM 身份注册的，任务定义只有管理员读得到，非管理员连枚举都会跳过它。想看清就右键 `install.cmd` →「以管理员身份运行」。
#### 想装到别处试（不改本机）

设 `SOL_INSTALL_ROOT=<目录>` 再跑脚本：落点全在那个目录里，**永不升权**，也**绝不碰**本机的服务管理器/计划任务/防火墙——测试和 CI 都靠它。

> 装了 Hermes 的话，这些规矩（含三平台真机踩过的坑）也在 `sol-install` 技能里：直接打 `/sol-install` 就行。

### Verify Installation

```bash
sol --version
sol --help
sol listen --help
sol ifaces
```

## Run the service

### Simple mode (command line)

```bash
# Sleep on port 10010, reboot on port 10011, on every eligible interface
sol listen --port 10010:sleep --port 10011:reboot

# Only on eth0, with the default action (shutdown) for a bare port number
sol listen --iface eth0 --port 10020

# See what the match is instead of acting on it
sol listen --port 10010:sleep --dry-run
```

### Options

| Flag | 含义 |
| --- | --- |
| `--config` | 要加载的配置文件（默认按 `$SOL_CONFIG`、`/etc/sol/sol.yaml`、`~/.config/sol/sol.yaml` 顺序找） |
| `--iface` | 参与匹配的网卡，可重复。省略即自动选中所有合格网卡（up、非 loopback、有 MAC、非虚拟） |
| `--port` | 监听的 UDP 端口，可带动作（`10010` 或 `10010:sleep`），可重复。端口 7/9 是保留端口，恒为 `noop`。只要给了 `--port` 就完全接管规则集 |
| `--default-action` | 裸端口号使用的动作（`noop`\|`sleep`\|`shutdown`\|`reboot`，默认 `shutdown`） |
| `--allow-reserved-actions` | 允许端口 7/9 执行非 `noop` 动作（旧行为；代价是放弃这两个端口保留的意义——WOL 互操作） |
| `--dry-run` | 只记录命中的包，不执行动作 |
| `--watch` | 轮询配置文件并在变化时重载（如 `5s`）；`0` 表示关闭。覆盖 `server.watch` |

### Configuration file mode

```yaml
version: 1

server:
  # interfaces: [eth0]        # optional: restrict matching to these NICs (see `sol ifaces`)
  watch: 5s                   # reload automatically when this file changes
  http:                       # optional control plane (defaults to 127.0.0.1:8080)
    enabled: true
    listen: 127.0.0.1:8080
    auth: { type: bearer, token_env: SOL_TOKEN }

logging: { level: info, format: text }   # level: debug|info|warn|error; format: text|json
# logging: { level: info, format: json, output: file, file: /var/log/sol/audit.log }

security:
  dry_run: false
  reserved_ports: [7, 9]
  # secure_on: "${SOL_SECURE_ON}"   # default SecureOn password (6 bytes) every rule expects
  url_allowlist:              # outbound HTTP destinations; boundaries are enforced
    - "https://hooks.example.com/"
    - "=https://api.example.com/v1/notify"
  exec_allowlist: [/usr/bin]
  cooldown: 5s
  cooldowns: { power.shutdown: 30s }
  settle: 5s                  # after a boot or a resume: no power action for 5s (default; 0 = off)
  # settle_actions: [power.sleep, power.shutdown]   # default: the three power actions
  rate_limit: 10/s            # global token bucket across every action and trigger
  rate_burst: 20              # bucket size; 0 means one second of rate_limit

actions:
  - name: lock
    type: exec
    command: [/usr/bin/loginctl, lock-session]
    timeout: 10s

  - name: notify
    type: http
    method: POST
    url: "https://hooks.example.com/hook/{{.Action}}"
    body: '{"action":"{{.Action}}","src":"{{.SrcIP}}","port":"{{.DstPort}}"}'
    timeout: 5s
    retries: 2
    # proxy: http://proxy.internal:3128   # optional: http, https or socks5

  - name: shutdown-then-notify
    type: sequence
    steps: [notify, power.shutdown]

  - name: wake-nas
    type: wol.send
    mac: "58:11:22:BC:78:66"        # the target machine; the only required parameter
    broadcast: 192.168.0.255        # default 255.255.255.255; a unicast address works too
    repeat: 3                       # a broadcast can be lost, send it a few times
    # secure_on: "${NAS_WOL_PASSWORD}"   # only if the target requires SecureOn (6 bytes)

rules:
  - match: { ports: [10010], content: { kind: suffix, value: "lock" } }
    action: lock
  - match: { ports: [10010], content: { kind: suffix, value: "off" }, src_cidrs: [192.168.0.0/24] }
    action: shutdown-then-notify
```

配置发现顺序：`--config` → `$SOL_CONFIG` → `/etc/sol/sol.yaml` → `~/.config/sol/sol.yaml`。
逐字段速查：见 [docs/routing-design.md](docs/routing-design.md) 的"字段速查"一节。

编辑器（支持 YAML 的）可以按发布的 JSON Schema 做补全和校验：把编辑器指到本地的
`schema/sol.schema.json`，或在文件开头加一行：

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/lidiaoo/sol/master/schema/sol.schema.json
```

Schema 与加载器严格一致——未知字段、类型不对、枚举值超出范围都会被拒；只要配置字段和 schema
对不上，测试就会失败。

### 每种动作，配一个能跑的例子

[Supported actions](#supported-actions) 那张表讲每个动作做什么，这里讲它怎么用。下面每个例子都是一份
**完整的配置**——原样就能加载，规则里写的端口就是你要发包过去的口。把它存成服务读的那份文件（或用
`--config` 指过去），起 sol，然后往那个端口发一个魔法包：[Testing your setup](#testing-your-setup) 里有
Linux、macOS、Windows 和 PowerShell 现成的发送命令。

| 动作 | 往哪个端口发什么 | 结果 |
| --- | --- | --- |
| `noop` | 10140 | 日志里一行，别的什么都不做 |
| `power.sleep` / `power.reboot` / `power.shutdown` | 10141 / 10142 + `reboot` / 10143 + `off` | 本机睡眠 / 重启 / 关机 |
| `exec` | 10144 + `lock` | 跑你配的命令 |
| `http` | 10145 | 调用你的 webhook |
| `sequence` | 10146 + `wake-nas` | 每一步按顺序跑 |
| `wol.send` | 10147 + `wake-nas` | 给另一台机器发魔法包 |
| `remote:<id>` | 10149（magic + `lock` + HMAC 标签）或 `POST /v1/commands/lock` | 跑白名单里的那条命令 |

#### noop


```yaml
version: 1
rules:
  - match: { ports: [10140], content: { kind: none } }
    action: noop
```

最安全的动作，也是保留端口唯一允许的动作。用它先确认规则能匹配上，再挂真正会动手的动作；或者用
`--dry-run` 起整个守护进程，效果相同。

#### power.sleep、power.reboot、power.shutdown


```yaml
version: 1
rules:
  - match: { ports: [10141], content: { kind: none } }
    action: power.sleep                 # suspend
  - match: { ports: [10142], content: { kind: suffix, value: "reboot" } }
    action: power.reboot
  - match: { ports: [10143], content: { kind: suffix, value: "off" } }
    action: power.shutdown
security:
  cooldowns: { power.sleep: 5s, power.shutdown: 5s, power.reboot: 5s }
```

它们作用在**运行 sol 的这台机器**上——它就是被关机的那台。需要 root（或 `SYSTEM`），安装脚本已经安排
好了。那三个 5s 冷却是防止一串唤醒包把刚睡下去的机器又按醒的关键：见 [Guards](#guards)。

#### exec


```yaml
version: 1
actions:
  - name: lock-screen
    type: exec
    command: [/usr/bin/loginctl, lock-session]   # argv, no shell unless shell: true
    timeout: 10s
rules:
  - match: { ports: [10144], content: { kind: suffix, value: "lock" } }
    action: lock-screen
security:
  exec_allowlist: [/usr/bin]
```

`command` 是 argv 列表，不是 shell 字符串，所以没写过的内容永远不会被解释执行。`shell: true` 是逃生口，
默认被"raw shell"开关禁着：见 [Raw shell (off by default)](#raw-shell-off-by-default)。
`exec_allowlist` 钉住绝对路径只能落在哪些目录。Linux/macOS 上可用 `user:`/`group:` 给命令降权（需要
root，Windows 不支持）。

#### http


```yaml
version: 1
actions:
  - name: tell-home-assistant
    type: http
    method: POST
    url: "http://homeassistant.local:8123/api/webhook/{{.Action}}"
    headers: { Content-Type: application/json }
    body: '{"who":"{{.SrcIP}}","port":{{.DstPort}}}'
    timeout: 5s
    retries: 2
rules:
  - match: { ports: [10145], content: { kind: none } }
    action: tell-home-assistant
security:
  url_allowlist: ["http://homeassistant.local:8123"]
```

webhook 方向：sol 把发生了什么告诉别人。`url_allowlist` 里要写**完整 URL**——不在名单里的目标是被拒绝，
不只是警告。`{{.Action}}`、`{{.SrcIP}}`、`{{.DstPort}}`、`{{.Arg.x}}` 可以插进查询串和路径里；scheme 和
host 必须是字面量，所以一个包永远无法把这次调用重定向到别处。

#### sequence


```yaml
version: 1
actions:
  - { name: nas-wake-lan, type: wol.send, mac: "AA:BB:CC:00:00:01", broadcast: 192.168.0.255 }
  - { name: tell-home-assistant, type: http, url: "http://homeassistant.local:8123/api/webhook/woke" }
  - { name: wake-and-tell, type: sequence, steps: [nas-wake-lan, tell-home-assistant] }
rules:
  - match: { ports: [10146], content: { kind: suffix, value: "wake-nas" } }
    action: wake-and-tell
security:
  url_allowlist: ["http://homeassistant.local:8123"]
```

把其它动作按顺序串成一个动作。steps 里放的是动作名。某一步失败会被记下来，但**不会跳过它后面的步骤**，
所以一个挂掉的 webhook 拦不住唤醒。

#### wol.send


```yaml
version: 1
actions:
  - name: wake-nas
    type: wol.send
    mac: "AA:BB:CC:00:00:01"     # required; the target never comes from the triggering packet
    broadcast: 192.168.0.255     # default 255.255.255.255
    port: 9                      # default 9
    repeat: 3                    # default 1
    interval: 100ms              # default 100ms
rules:
  - match: { ports: [10147], mac: self, content: { kind: suffix, value: "wake-nas" } }
    action: wake-nas
```

把别人叫醒。目标 MAC **只从配置里来**，绝不从触发它的那个包里取——这正是 `wol.send` 能放心暴露的原因。
默认值：broadcast `255.255.255.255`、端口 9、每个包发 1 次、间隔 100ms。

#### remote:<id>


```yaml
version: 1
security:
  allow_remote_commands: true
  remote_command_auth: { type: hmac, key_env: SOL_CMD_KEY, window: 60s }
  remote_command_ports: [10149]
commands:
  - id: lock
    type: exec
    command: [/usr/bin/loginctl, lock-session]
rules:
  # sol needs at least one rule to start; the remote command channel has its own listener
  - match: { ports: [10150], content: { kind: none } }
    action: noop
```

远程命令通道：白名单里的命令，可以走 UDP（魔法包 + `<id>[:k=v,...]` + HMAC 标签）或走 HTTP
（`POST /v1/commands/lock`）。它需要 `allow_remote_commands: true` 和一个 HMAC 密钥，密钥来自环境变量
（`key_env`）或 0600 文件——绝不写进 YAML。每条命令同时会变成一个普通动作，这里是 `remote:lock`，所以它
会出现在 `/v1/actions`、冷却和审计日志里。但**规则不能指向它**：`match` 里写 `action: remote:<id>` 会在
启动时被拒（`unknown action reference`），因为这个通道是唯一的入口。

#### raw:shell

它没有可配置的动作：它是 `exec` 加 `shell: true` 背后那道护栏的名字，默认关着——见
[Raw shell (off by default)](#raw-shell-off-by-default)。
### Remote commands (off by default)

远端命令通道让经过认证的发送方通过 UDP（魔法包 + `<id>[:k=v,...]` + HMAC-SHA256 tag）或 HTTP
（`POST /v1/commands/{id}`）调用白名单里的命令。除非 `security.allow_remote_commands: true` 且
配了 HMAC 密钥，否则它是关闭的；密钥永远不写进 YAML：

```yaml
version: 1
security:
  allow_remote_commands: true
  remote_command_auth: { type: hmac, key_env: SOL_CMD_KEY, window: 60s }   # key + replay window
  remote_command_ports: [10014]                                            # reserved ports refused

commands:
  - id: lock
    type: exec
    command: [/usr/bin/loginctl, lock-session]
    user: nobody                                             # optional privilege drop
    args:
      session: { type: string, enum: [tty, x11], required: false }
```

每个命令都会注册成名为 `remote:<id>` 的普通动作，因此 cooldown、dry-run、审计日志和
`POST /v1/actions/remote:<id>` 对它一样生效。

### Authenticating packets (off by default)

内容 token 是明文的：能摸到端口的人就能触发规则。用 `security.packet_auth` 可以让规则要求整包
的 HMAC tag（截断的 SHA-256，8 字节），`wol.send` 也能生成它：

```yaml
version: 1

security:
  packet_auth: { type: hmac, key_env: SOL_PACKET_KEY, window: 60s }   # key + replay window; the key never lives in this file

actions:
  - name: wake-nas
    type: wol.send
    mac: "58:11:22:BC:78:66"
    sign: true                      # append the tag the target expects

rules:
  - match: { ports: [10012], auth: hmac }   # only a packet with a valid tag may fire here
    action: wake-nas
```

`window` 用来限制重放：包尾变成 8 字节大端 unix 秒的 stamp 加 tag，且 stamp 在 tag 覆盖范围内，
所以改不了。

接收方只接受窗口内（不早也不晚）的 stamp，并且同一个 tag 在窗口内只接受一次——被录下的包再发
一次会按重放拒绝，超过窗口才发来的包按过期拒绝。两种拒绝都会带原因记日志
（`authenticated packet refused reason=replay|stale`）、计入 `/v1/status`（`replayed` 以及
`replay_reasons` 明细）并导出为 `/metrics` 的 `sol_replayed_total`；已见 tag 的缓存是有上限的：
满了以后新包被拒，而不是淘汰一个仍可能被重放的条目。两端必须一致：开了窗口的接收方会拒收只带
tag 的旧布局，而 `wol.send` 只在自己这个实例也设了 `window` 时才发 stamp（它需要密钥和窗口，
不需要规则）。reload 会从空缓存开始，所以刚 reload 之前见过的包在窗口内可能被重放一次；其它
情况下窗口就是界。

`match.auth: hmac` 是按规则生效的，所以老规则照旧。tag 覆盖它之前的全部字节（包括 `secure_on`
口令），内容匹配看到的是去掉 tag 的负载，审计日志对每次命中都记 `authenticated=true|false`。
"规则要求认证但没配密钥"、"保留端口要求认证"、"`sign: true` 但没有密钥"都会在启动时报错而不是
静默失效。它能证明包来自持有密钥的人；单靠它并不能阻止把录下的包重放一次——要限制这一点就配
`packet_auth.window`（见下），或依赖 cooldown 与限流。

### SecureOn per interface or per rule

`security.secure_on` 是每条规则默认要的口令（6 字节）。某个网卡块或某条规则可以要求不同的口令，
这正是"同一端口上两个目标互不串台"的做法：

```yaml
version: 1

security:
  secure_on: "${SOL_SECURE_ON}"     # the default

actions:
  - name: wake-server
    type: wol.send
    mac: "58:11:22:BC:78:66"

server:
  interfaces:
    - name: enp9s0f3u1
      secure_on: "${NAS_WOL_PASSWORD}"   # this block's rules expect this one instead
      rules:
        - match: { ports: [10020] }
          action: wake-server

  rules:
    - match: { ports: [10020], secure_on: "${OTHER_WOL_PASSWORD}" }   # this rule only
      action: noop
```

两条规则都监听 10020，一条继承全局口令、另一条自己声明：它们永远不可能命中同一个包，所以这不算
冲突。

- 规则没写 `secure_on` 就继承它所在块的，块也没写就继承 `security.secure_on`；三级都没写则表示
  不要求口令。
- `secure_on: ""` 与"不写"不同：它是**显式**表示"这里不要口令"，用来豁免全局默认。
- 端口 7 和 9 只接受纯魔法包，所以在这两个端口上配口令会在启动时被拒。
- 只是口令不同的两条规则不算冲突：一个包只会带其中一个口令。口令对不上的包会被当成明文魔法包读
  （那 6 个字节落到 content 里），而默认的 `content: none` 会拒绝它——所以口令错了永远打不开一条
  要求口令的规则。

### Raw shell (off by default)

`security.allow_raw_shell: true` 允许远端发送方通过 `/bin/sh -c` 执行任意 shell 命令。这是 sol
唯一用 shell 的地方，默认关闭；而且不开它自己的 HMAC 密钥、不给它一个命令通道没在用的专用端口，
它就会被拒：

```yaml
version: 1

security:
  allow_remote_commands: true            # the raw shell rides on the command channel
  remote_command_auth: { type: hmac, key_env: SOL_CMD_KEY }
  remote_command_ports: [10012]

  allow_raw_shell: true
  raw_shell_auth: { type: hmac, key_env: SOL_RAW_SHELL_KEY }   # its own key, not the command key
  raw_shell_ports: [10013]                                     # non-reserved, not shared
  raw_shell_src_cidrs: ["192.168.0.0/24"]                      # who may use it at all
  raw_shell_allowlist: ["^echo .*$"]                           # entries are anchored at both ends
  raw_shell_timeout: 5s
```

```bash
# UDP: [magic packet][secure_on?][command][8-byte HMAC tag]
python3 -c 'import hashlib,hmac,socket; m=b"\xff"*6+bytes.fromhex("58:11:22:BC:78:66".replace(":",""))*16; c=b"echo hi"; k=b"..." ; t=hmac.new(k,m+c,hashlib.sha256).digest()[:8]; socket.socket(2,2).sendto(m+c+t,("192.168.0.10",10013))'

# HTTP control plane
curl -X POST -H "Authorization: Bearer $SOL_TOKEN" -H 'Content-Type: application/json' \
  -d '{"cmd":"echo hi"}' http://127.0.0.1:8080/v1/exec
```

两个通道也能防重放：`remote_command_auth.window` 与 `raw_shell_auth.window`（如 `60s`）让发送方
给每段命令加 stamp，接收方只接受窗口内的 stamp、且每个 tag 只接受一次——就是给认证包用的那套
代码（`security.packet_auth.window`）。不配就是关闭，两端必须一致；线格式见
`docs/routing-design.md` §21.3/§21.4，细节见 §19.17。

配了 `raw_shell_allowlist` 时每条命令都要过它（整条命令行必须匹配，所以 `^echo .*$` 挡得住
`id; echo hi`）；配了 `raw_shell_src_cidrs` 时发送方必须在里面；每条命令连同退出码都会完整记入
日志。dry-run、cooldown、限流、超时与降权全部生效，启动日志里还会有一条点名所开端口的告警。
`raw:shell` 只是护栏与审计日志用的内部标签——不是能写进规则的动作名。启用了通道但密钥不对、
命令为空、端口是保留端口、端口与命令通道共用、或白名单条目编译不过，都会在启动时报错。

### Control plane

`server.http.enabled: true` 会在默认 `127.0.0.1:8080` 上开启 HTTP 控制面，并强制认证
（`bearer` 配 `${SOL_TOKEN}`、`basic`，或 `mtls`）。除非你明确要求，它不会绑公网地址，也没有
无认证模式。

token 本身来自环境变量（`export SOL_TOKEN=...`）或 0600 权限的文件；它不写进 YAML，引用一个
未设置的变量会让启动失败，而不是拿着空密钥跑起来。注释里的引用会被忽略，所以把某个可选行注释掉
——比如上面的 `secure_on`——配置照样能加载。

端点：`GET /healthz`、`GET /v1/status`、`GET /v1/rules`、`GET /v1/interfaces`、`GET /metrics`、
`POST /v1/actions/{name}`、`POST /v1/commands/{id}`、`POST /v1/exec`、`POST /v1/reload`。

`GET /v1/status` 会报出**正在应答的是哪个构建**——`"version"`（打标值，或工具链的伪版本）与
`"revision"`（提交号，工作区有未提交改动时带 `-dirty` 后缀）；`/metrics` 把同一对信息暴露成
`sol_build_info{version="...",revision="..."} 1`。这样两份配置不同的进程，光凭一份 bug 报告就能
区分开。

#### 每个端点，以及它返回什么


下面每个返回都是从**跑起来的实例**上抓的（用 `--dry-run` 起，所以什么都没真的执行）。剩下两种情况一条
规矩：令牌错或没带上，所有需要认证的端点一律回 **`401`** + `{"error":"unauthorized"}`；路径不存在回
`404 page not found`（纯文本，不是 JSON）。除 `/healthz` 外都需要 `Authorization` 头。

##### `GET /healthz` —— 它活着吗？

唯一不需要认证的端点，而且**完全不看令牌**：令牌错了也回 `200`。所以它只能告诉你控制面活着，永远回答
不了"令牌对不对"。

```json
{"status":"ok"}
```

##### `GET /v1/status` —— 进程的一张快照

```json
{
  "version": "v0.1.0-21-g7c3dd97",
  "revision": "7c3dd97b2ad5",
  "uptime": "2s",
  "uptime_seconds": 2.9637342,
  "packets": 0,
  "matched": 0,
  "suppressed": 0,
  "rate_limited": 0,
  "inflight": 0,
  "settle_skipped": 0,
  "replayed": 0,
  "actions": {},
  "rules": 1,
  "interfaces": ["eth0", "wlan0"],
  "dry_run": true,
  "auth_type": "bearer",
  "http_listen": "127.0.0.1:18080"
}
```

| 字段 | 含义 |
| --- | --- |
| `version`、`revision` | 应答的这个构建：版本戳（或工具链的伪版本）与提交号；工作区有未提交改动时带 `-dirty` |
| `uptime`、`uptime_seconds` | 同一个时长的两种写法：给人看的、和给脚本用的浮点数 |
| `packets` | 启动以来收到的魔法包数 |
| `matched` | 其中命中规则的个数 |
| `suppressed` | 命中了但没执行：冷却、限流或 settle 窗口 |
| `rate_limited`、`settle_skipped`、`replayed` | `suppressed` 拆开后的各项计数 |
| `inflight` | 此刻正在执行的动作数 |
| `actions` | 每个动作的执行次数（按名字）；`--dry-run` 下一直是 `{}`，因为什么都没真跑 |
| `rules`、`interfaces` | 装了几条规则，以及正在监听的网卡名 |
| `dry_run`、`auth_type`、`http_listen` | 生效中的护栏与地址——拿到一份 bug 报告就能直接读，不用来回问 |

##### `GET /v1/rules` —— 编译后的规则，一条一条

```json
{"rules":[{"ports":[10160],"mac":"self","content":"none","action":"noop","dry_run":false}]}
```

每条规则一项，顺序就是求值顺序：端口、MAC 选择器（`self`、`any` 或具体地址）、载荷怎么比、解析到的动作
名，以及这条规则自己是否处于 dry-run。

##### `GET /v1/interfaces` —— 它在听哪些网卡

```json
{"interfaces":[{"name":"eth0","mac":"58:11:22:bc:78:66","ipv4":"192.168.0.120"},{"name":"wlan0","mac":"f0:d4:15:57:9c:c5","ipv4":"192.168.0.121"}]}
```

和 `sol ifaces --json` 打印的是同一份，也是启动日志里报的那份。没有地址的网卡也会出现在这里——它们照样
能收到魔法包。

##### `GET /metrics` —— 同一批计数，Prometheus 形式

```text
# TYPE sol_packets_total counter
sol_packets_total 0
# TYPE sol_matched_total counter
sol_matched_total 0
# TYPE sol_suppressed_total counter
sol_suppressed_total 0
# TYPE sol_settle_skipped_total counter
sol_settle_skipped_total 0
# TYPE sol_replayed_total counter
sol_replayed_total 0
# TYPE sol_rules gauge
sol_rules 1
# TYPE sol_uptime_seconds gauge
sol_uptime_seconds 3.225
# TYPE sol_build_info gauge
sol_build_info{version="v0.1.0-21-g7c3dd97",revision="7c3dd97b2ad5"} 1
```

计数器：`sol_packets_total`、`sol_matched_total`、`sol_suppressed_total`、`sol_settle_skipped_total`、
`sol_replayed_total`、`sol_rate_limited_total`、`sol_inflight_total`，以及每个动作一个的
`sol_actions_total`。仪表：`sol_rules`、`sol_uptime_seconds`、`sol_build_info{version,revision}`。

##### `POST /v1/actions/{name}` —— 执行一个配置好的动作

```json
{"action":"noop","status":"triggered"}
```

`202` 的意思是**已受理**，不是"命中"：名字**不做校验**，所以打错了和真名字的返回一模一样
（`{"action":"nope","status":"triggered"}`）。到底发生了什么，看日志，或者看 `/v1/status` 里的
`actions`。可选的 JSON body 用来带参数（`{"args":{"k":"v"}}`），供 `{{.Arg.k}}` 插值。

##### `POST /v1/commands/{id}` —— 远程命令通道

```json
{"command":"hi","status":"triggered"}
```

通道开着时回 `202` 和命令 id。它默认关着——需要 `security.allow_remote_commands: true` 加上一个 HMAC
密钥，关着的时候回 **`403 {"error":"remote command forbidden: remote commands are disabled"}`**；id 不在
`commands` 里则回 **`404 {"error":"unknown remote command: nope"}`**。

##### `POST /v1/exec` —— 一条 shell 行，走 raw shell 那条路

body 是 `{"cmd":"<一条 shell 行>"}`——**字符串**，不是 argv 列表，所以它属于受管的那条路（见
[Raw shell (off by default)](#raw-shell-off-by-default)）。缺字段回 `400 {"error":"cmd is required"}`；
body 格式不对回 `400 {"error":"invalid request body: ..."}`。

##### `POST /v1/reload` —— 重新读配置文件

```json
{"reloaded":true}
```

新文件应用成功回 `200 {"reloaded":true}`。加载失败则把加载器的错误回给你，而**正在跑的配置保持不变**——
重载不会留下半应用的状态。

#### 打开它并用 curl 调用

把下面这段加进**服务实际读的那份配置**（`/etc/sol/sol.yaml`、`~/.config/sol/sol.yaml`，或 `--config`
指定的路径）。安装脚本生成的默认配置里**已经有这一节，只是 `enabled: false`**，所以只需要打开开关——
但得先给令牌找一个来源：`token_env` 填的是**环境变量的名字**，把令牌本身写在那儿就是那个会让启动失败的
错误。

```yaml
server:
  http:
    enabled: true
    listen: 127.0.0.1:8080
    auth: { type: bearer, token_env: SOL_TOKEN }   # the variable's NAME, never the token itself
```

把这个变量交给**服务**，然后重启它。服务看不到你当前 shell 的环境变量，所以三端各有各的落点：

```bash
# Linux (systemd): put it in the unit, or in /etc/sol/http.env and reference that file
systemctl edit sol                  # Environment=SOL_TOKEN=<long random string>
systemctl restart sol
# macOS (launchd): add it to the daemon's EnvironmentVariables, then reload the daemon
#   /Library/LaunchDaemons/com.lidiaoo.sol.plist -> <key>EnvironmentVariables</key>
sudo launchctl kickstart -k system/com.lidiaoo.sol
# Windows (Task Scheduler runs sol as SYSTEM, so the variable must be machine-wide)
setx SOL_TOKEN "<long random string>" /M
schtasks /End /TN sol && schtasks /Run /TN sol
```

`SOL_TOKEN` 是"一段长随机串"的占位符。Windows 上请用 `token_env`：`token_file` 的 0600 检查读的是
Windows 没有的 POSIX 权限位，所以那条路在那里会被拒绝。

然后调用它。Windows 10 自带 `curl.exe`——在 PowerShell 里要写全 `curl.exe`，否则 `curl` 是
`Invoke-WebRequest` 的别名。cmd 里变量写 `%SOL_TOKEN%`、PowerShell 写 `$env:SOL_TOKEN`、
Linux/macOS 写 `$SOL_TOKEN`：

```bash
curl -s http://127.0.0.1:8080/healthz                   # the only endpoint that needs no token
curl -s -H "Authorization: Bearer $SOL_TOKEN" http://127.0.0.1:8080/v1/status
curl -s -H "Authorization: Bearer $SOL_TOKEN" http://127.0.0.1:8080/v1/rules
curl -s -X POST -H "Authorization: Bearer $SOL_TOKEN" http://127.0.0.1:8080/v1/actions/power.sleep
curl -s -X POST -H "Authorization: Bearer $SOL_TOKEN" http://127.0.0.1:8080/v1/reload
```

`/healthz` 回 `{"status":"ok"}` 说明控制面起来了；但它**不校验令牌**，回答不了"令牌对不对"。任何
`/v1/...` 回 `401` 且正文 `{"error":"unauthorized"}`，就是令牌错或没带上，同一次拒绝也会写进审计日志。
连不上（`curl: (7)`）说明根本没人在听：sol 没在跑、`enabled` 不是 true、或者它启动被拒（缺令牌变量就会
这样）。控制面是**启动时**拉起的，热重载不管它，所以改完要重启。

#### 用别的工具驱动它（Postman、ApiPost、UpSnap、手机 App）

```bash
# any HTTP client works: curl, Postman, ApiPost, a button in a dashboard
curl -X POST -H "Authorization: Bearer $SOL_TOKEN" http://127.0.0.1:8080/v1/actions/nas-wake
# any Wake-on-LAN sender works too: UpSnap, PowerControl, a phone Wake-on-LAN app.
# Point it at this host, choose a non-reserved port (9 and 7 take bare magic packets only)
# and put the target device's own MAC in the packet - the port decides what happens.
```

curl 只是最顺手的客户端：控制面说的是普通 HTTP，所以 Postman、ApiPost 对着同一个端点就能用（方法
`POST`、`Authorization` 头里放令牌、路径里放动作名）。反过来，任何能发 Wake-on-LAN 魔法包的东西都能
当触发器，根本不需要 HTTP——UpSnap、PowerControl、手机上的 Wake-on-LAN App：指向这台主机，选一个
**非保留端口**，把目标设备自己的 MAC 放进包里。9 和 7 是保留端口：只接受裸魔法包，且不允许挂任何动作，
所以这类分组请用 1024 以上的端口。

两个方向最后都落到一个**组**上：它才是面板、App、脚本真正调用的东西。给每台设备、每个动作各自一个名字
和一个端口，并把这台设备的**所有网卡**——有线的和无线的——放进同一个组。

```yaml
version: 1
actions:
  # one action per NIC: wol.send takes a single MAC, so a group is a sequence
  - { name: nas-wake-lan,  type: wol.send, mac: "AA:BB:CC:00:00:01", broadcast: 192.168.0.255 }
  - { name: nas-wake-wifi, type: wol.send, mac: "AA:BB:CC:00:00:02", broadcast: 192.168.0.255 }
  - { name: nas-wake, type: sequence, steps: [nas-wake-lan, nas-wake-wifi] }
rules:
  # group "nas-sleep": this host sleeps when one of its own NICs is addressed (wired and wireless)
  - match: { ports: [10130], mac: self, content: { kind: none } }
    action: power.sleep
  # group "nas-wake": the same idea for waking another machine
  - match: { ports: [10131], content: { kind: suffix, value: "nas-wake" } }
    action: nas-wake
```

`mac: self` 覆盖运行 sol 这台主机的全部网卡，正是"让这台机器睡"那个组需要的写法；而 `wol.send` 一个
动作只能带一个 MAC，所以"把这台机器唤醒"的组是一个 `sequence`，每张网卡一个动作——同一台机器的有线地址
和无线地址都在里面。因为"组"落在具名动作上，任何 HTTP 客户端里它就是一个按钮，而往这个组的端口发一个包
触发的是同一件事。

#### 按设备分组：每台设备、每个动作一组

| 组 | 怎么触发 | 效果 |
| --- | --- | --- |
| `nas-sleep`（端口 10130） | 目标 MAC 是本机任一网卡的魔法包 | 让本机 `power.sleep` |
| `nas-wake`（端口 10131，或 `POST /v1/actions/nas-wake`） | 载荷以 `nas-wake` 结尾，或走 HTTP | 把 `nas` 的所有网卡都唤醒 |
| `desk-reboot`（端口 10132） | 目标 MAC 是本机任一网卡的魔法包 | 让本机 `power.reboot` |

命名是约定，不是语法：sol 匹配的是端口、MAC 和载荷，`nas-wake` 只是你给那个端口和那个动作起的名字。
让规则名和动作名保持一致，这个组就会在日志、`/v1/rules` 和你接的任何一个客户端里自解释。

### Audit log destination

审计日志默认进 stderr，交给服务管理器管（`journalctl -u sol`）。`logging.output` 可以改：`stdout`，
或 `file` 加 `logging.file`（写了 `output: file` 却没有路径、或写了路径却没有 `output: file`，
都是启动期错误）。日志文件以追加方式打开、权限 0600——里面会写来源地址、执行了哪个动作，`exec`
与裸 shell 还会写命令行——而且**不做轮转**：请配 `logrotate`，或者保留默认让 journald 去管。目的地
只在启动时读取；reload 会热换的是 `logging.level`。`SOL_LOG_OUTPUT` 与 `SOL_LOG_FILE` 可覆盖这两个
字段。
### Reloading

三条路径、一套实现：`SIGHUP`、`POST /v1/reload`、`server.watch` / `--watch`（轮询配置文件）。
规则、动作、cooldown、限流、远端命令通道、`dry_run` 和日志级别按包原子换入，**监听端口集合也跟着
一起换**：新配置新增的端口会先全部绑定成功，才开始关闭离开集合的端口，所以绑不上（HTTP 409）的
重载什么都不改，而成功的重载会立刻开始读新端口并关掉离开的端口。加载失败的配置不会碰正在跑的
那一份；cooldown 与限流没变就保留运行状态，所以 reload 不能用来偷偷把护栏刷新掉。网卡列表也会
重新发现，因此 `/v1/interfaces` 和审计日志描述的是机器当前的样子。

### Guards

三道限制保护机器不被广播风暴打穿，前两道**默认就开着**：

- `security.cooldown` / `security.cooldowns.<动作名>` —— 按动作：**同一个**动作两次执行之间的最小
  间隔。三个会改变状态的电源动作（`power.sleep`、`power.shutdown`、`power.reboot`）默认 **5 秒**：
  Wake-on-LAN 的发送方为了可靠会重复发包（3 个以上），而**每个包都是独立的一次触发**，认了第二个
  就等于把第一个撤销了。可用 `security.cooldowns.<动作名>` 改窗口，或用 `0s` 关掉。
- `security.settle`（默认 **5 秒**）—— 两个时刻之后的窗口：sol **刚启动**（开机、重启、服务重启）
  和机器**刚从睡眠里回来**。这段时间里 `security.settle_actions` 列出的动作（默认三个电源动作）
  会被拒绝。因为魔法包突发剩下的那几个包**正落在这里**——网卡会把机器再唤醒一次——而只有这道护栏
  能真正断掉这种乒乓：机器一旦关机，进程和它内存里的窗口也就都没了。`security.settle: 0` 关闭。
- `security.rate_limit` / `security.rate_burst` —— 全局：跨**所有**动作与所有触发源
  （包、`POST /v1/actions/{name}`、远端命令）的令牌桶。
- 以及无需任何配置的"执行中重入护栏"：同一个"要跑的活"还在执行时，再次触发会被拒绝而不是起第二个
  实例。身份 = 动作名 + 它实际要做什么（已校验的远端参数，或裸 shell 的命令行），所以
  `remote:backup target=home` 不会抑制 `target=work`。两个并发请求打同一个长动作，靠的就是它；
  而纯粹的包突发归 `security.cooldown` 管，因为包路径是一个一个处理的。

两个默认值都是配置，不是写死的策略。想彻底关掉这层保护：

```yaml
security:
  settle: 0                     # no window after a boot or a resume
  cooldowns:
    power.sleep: 0s             # no built-in window for the power actions
    power.shutdown: 0s
    power.reboot: 0s
```

被抑制的动作会记日志（`action suppressed by cooldown` / `by rate limit` / `the machine just
started or woke up` / `already running`，有重试时间就带上），计入 `/v1/status`（`suppressed`、
`rate_limited`、`settle_skipped`、`inflight`）与 `/metrics`（`sol_suppressed_total`、
`sol_rate_limited_total`、`sol_settle_skipped_total`、`sol_inflight_total`），在控制面回 **429**。
启动日志会打印生效的窗口（`action cooldown`、`settle window`）；配了限流时 `GET /v1/status`
也会回显当前的 `rate_limit`。

## Home Assistant


这个搭配两个方向都能走，而且分工正是它舒服的原因：Home Assistant 管自动化、面板和通知，sol 管需要
特权的那半边——睡眠、重启、关机、局域网唤醒——连同它的冷却、settle 窗口和审计日志。Home Assistant
不需要 root、不需要 shell 插件、也不需要你机器的口令。

##### sol 主动告诉 Home Assistant

这个方向什么都不用对外暴露：由 sol 去拨号。

```yaml
version: 1
actions:
  - name: tell-home-assistant
    type: http
    method: POST
    url: "http://homeassistant.local:8123/api/webhook/sol_event"
    headers: { Content-Type: application/json }
    body: '{"event":"{{.Action}}","from":"{{.SrcIP}}","port":{{.DstPort}}}'
    timeout: 5s
    retries: 2
rules:
  - match: { ports: [10141], content: { kind: none } }
    action: tell-home-assistant      # a bare packet tells Home Assistant; it decides what to do
  - match: { ports: [10143], content: { kind: none } }
    action: power.shutdown           # this is the port Home Assistant's wake_on_lan action sends to
security:
  url_allowlist: ["http://homeassistant.local:8123/api/webhook/sol_event"]
  cooldowns: { power.shutdown: 5s }
```
```yaml
# Home Assistant: configuration.yaml
automation:
  - alias: "sol: report every trigger"
    triggers:
      - trigger: webhook
        webhook_id: sol_event
        allowed_methods: [POST]
        local_only: true
    actions:
      - action: notify.mobile_app_phone
        data:
          message: "{{ trigger.json.event }} from {{ trigger.json.from }}:{{ trigger.json.port }}"
```

`sol_event` 是你自己起的 webhook id；Home Assistant 监听
`http://<home-assistant>:8123/api/webhook/<webhook_id>`。sol 插进 body 的那些内容都会以
`trigger.json` 送到，所以通知里能说清"哪台机器、从哪儿被点名"——这是裸 Wake-on-LAN 包永远带不了的上下文。

##### Home Assistant 指挥 sol

两种做法，通常第二种就够。

走局域网包——Home Assistant 自带的 `wake_on_lan` 动作可以直接打到 sol 的端口，于是一个按钮、一条自动化
或一句语音就成了触发器，不需要新的暴露面、也没有令牌要管：

```yaml
# Home Assistant: no HTTP, no exposed control plane - just a bare magic packet on the group's port
- action: wake_on_lan.send_magic_packet
  data:
    mac: "AA:BB:CC:00:00:01"
    broadcast_address: "192.168.0.120"    # the host that runs sol
    broadcast_port: 10143                 # the port that group listens on
```

走 HTTP——想调用**具名动作**（包括上面那些"组"）就用 `rest_command`。这是唯一需要 Home Assistant
能访问到控制面的情形，所以给 `server.http.listen` 一个局域网地址、配一个长令牌，并把防火墙收紧：

```yaml
# Home Assistant: configuration.yaml - press a button, sol does the privileged part
rest_command:
  sol_sleep_desk:
    url: "http://sol-host.lan:8080/v1/actions/desk-sleep"
    method: post
    headers:
      Authorization: !secret sol_token      # the secret holds "Bearer <long random string>"
      Content-Type: application/json
  sol_wake_nas:
    url: "http://sol-host.lan:8080/v1/actions/nas-wake"
    method: post
    headers:
      Authorization: !secret sol_token
```

##### 把 sol 做成面板里的传感器

```yaml
# Home Assistant: sol as a sensor, so the dashboard shows what it is really doing
rest:
  - resource: "http://sol-host.lan:8080/v1/status"
    headers:
      Authorization: !secret sol_token
    scan_interval: 60
    sensor:
      - name: "sol matched"
        value_template: "{{ value_json.matched }}"
      - name: "sol suppressed"
        value_template: "{{ value_json.suppressed }}"
      - name: "sol version"
        value_template: "{{ value_json.version }}"
binary_sensor:
  - platform: rest
    resource: "http://sol-host.lan:8080/healthz"
    value_template: "{{ value_json.status == 'ok' }}"
    device_class: connectivity
```

##### 这个组合好在哪

- **没有轮询。** 包一命中 sol 就推过去，Home Assistant 不用去问。
- **特权只留在一处。** 以 root 运行的是一个带审计日志的小二进制，而不是你的自动化平台；Home Assistant
  只负责发包或调用一个具名动作。
- **两个方向都有凭据。** 出站被 `url_allowlist` 钉住（写完整 URL，不是主机名）；入站要么带令牌，要么
  干脆什么凭据都不带——一个裸魔法包打到一个"只做那个端口被配置去做的事"的端口上。
- **唤醒风暴已经解决了。** 冷却与 settle 窗口是 sol 的，所以一串唤醒包、或者一次从睡眠里醒过来，都无法
  把 Home Assistant 刚下达的那次睡眠按回去。
- **上下文能带过去。** `{{.SrcIP}}`、`{{.DstPort}}`、`{{.Action}}`、`{{.Arg.x}}` 都会进 webhook body，
  所以 Home Assistant 能按"**谁**发的"分支，而不是只知道"有东西发了"。
- **两头都有记录。** sol 把命中、动作和结果写进自己的日志，Home Assistant 留下自动化 trace；排查一次没
  生效的唤醒，从两份日志开始，而不是靠猜。

两件要避免的事：Home Assistant 的 `wake_on_lan` 发的是**裸包、不带载荷**，所以要打到一个规则写
`content: { kind: none }` 的端口上（上面"每台设备、每个动作一组"的约定正是为此）；另外别让一条
Home Assistant 自动化反过来触发**同一个组**——冷却会吸收掉它，但回环不该是承重设计。

## Running as a service

### Linux (systemd)

1. **创建 unit 文件**

   ```bash
   sudo nano /etc/systemd/system/sol.service
   ```

   ```ini
   [Unit]
   Description=SOL listener
   After=network-online.target
   Wants=network-online.target

   [Service]
   ExecStart=/usr/local/bin/sol listen --config /etc/sol/sol.yaml
   Restart=always
   RestartSec=5

   # High ports need no privilege. For the reserved ports 7/9 (or anything below 1024)
   # either run as root or grant the capability:
   # AmbientCapabilities=CAP_NET_BIND_SERVICE
   # CapabilityBoundingSet=CAP_NET_BIND_SERVICE
   # User=sol

   [Install]
   WantedBy=multi-user.target
   ```

   配置里写了 `server.watch: 5s` 时，改动 `/etc/sol/sol.yaml` 就会生效；另一种做法是声明
   `ExecReload=/bin/kill -HUP $MAINPID`，然后用 `systemctl reload sol.service`。

2. **reload、开机自启并启动**

   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable --now sol.service
   sudo systemctl status sol.service
   ```

3. **看日志**

   ```bash
   journalctl -u sol.service -f
   ```

4. **如果开了防火墙，把用到的端口放行**——监听的是 UDP：

   ```bash
   sudo ufw allow 10010/udp comment 'sol'
   ```

### macOS (launchd)

1. **装二进制与配置**

   ```bash
   sudo cp sol /usr/local/bin/sol && sudo mkdir -p /usr/local/etc/sol
   sudo cp sol.yaml /usr/local/etc/sol/sol.yaml
   ```

2. **创建 launchd daemon**

   ```bash
   sudo nano /Library/LaunchDaemons/com.lidiaoo.sol.plist
   ```

   ```xml
   <?xml version="1.0" encoding="UTF-8"?>
   <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
   <plist version="1.0">
   <dict>
     <key>Label</key>
     <string>com.lidiaoo.sol</string>
     <key>ProgramArguments</key>
     <array>
       <string>/usr/local/bin/sol</string>
       <string>listen</string>
       <string>--config</string>
       <string>/usr/local/etc/sol/sol.yaml</string>
     </array>
     <key>RunAtLoad</key>
     <true/>
     <key>KeepAlive</key>
     <true/>
     <key>StandardErrorPath</key>
     <string>/usr/local/var/log/sol.log</string>
   </dict>
   </plist>
   ```

3. **加载并看日志**

   ```bash
   sudo launchctl load -w /Library/LaunchDaemons/com.lidiaoo.sol.plist
   sudo launchctl list | grep sol
   tail -f /usr/local/var/log/sol.log
   ```

   `launchctl unload -w` 停掉它，`RunAtLoad` 会在重启后拉回来。LaunchDaemon 以 root 运行，这正是
   <1024 端口需要的；想改用专用用户就在 plist 里加 `UserName`，同时把端口留在高位。

### Windows (Task Scheduler)

纯 `.exe` 不能直接当 Windows 服务，所以省事又可靠的做法是用计划任务，令牌放机器级环境变量：

1. **装到固定路径，并以 SYSTEM 在开机时启动**

   ```powershell
   New-Item -ItemType Directory -Force -Path C:\ProgramData\sol | Out-Null
   Move-Item .\sol.exe C:\ProgramData\sol\sol.exe -Force
   # 计划任务直接跑 sol.exe：任务计划里"操作"一栏就是 exe + 参数，一眼看得懂。
   # 任务没有 stdout 可接，所以日志由 sol 自己写文件——运行配置里补一节：
   #   logging: { output: file, file: 'C:\ProgramData\sol\sol.log' }
   schtasks /Create /TN sol /TR "\"C:\ProgramData\sol\sol.exe\" listen --config \"C:\ProgramData\sol\sol.yaml\"" /SC ONSTART /RU SYSTEM /RL HIGHEST /F
   schtasks /Run /TN sol
   ```

2. **让包进得来**（监听是 UDP；Windows 没有特权端口，所以 7、9 也能用）

   ```powershell
   New-NetFirewallRule -DisplayName "sol (WoL)" -Direction Inbound -Protocol UDP -LocalPort 10010,7,9 -Action Allow
   ```

3. **检查**——看计划任务的历史，或配置里指定的日志文件：

   ```powershell
   schtasks /Query /TN sol /V /FO LIST
   Get-Content C:\ProgramData\sol\audit.log -Wait
   ```

   `SYSTEM` 账号读得到机器级环境变量，所以用
   `[Environment]::SetEnvironmentVariable('SOL_TOKEN','...','Machine')` 设一次密钥，它就可用。注意
   机器级变量管理员可读；更稳的做法是把密钥放一个只有 `SYSTEM` 有权限的文件里，再在配置里按
   0600 的语义引用它。

### Service configuration notes

- `After=network-online.target` 保证服务在网络就绪之后才启动。
- `Restart=always` 在崩溃后自动拉起。
- 配置放 `/etc/sol/sol.yaml`，密钥放环境变量或配置引用的 0600 文件——绝不写进 YAML 本身。
- 服务有自己的家目录（systemd/launchd 是 root 的，Windows 是 `SYSTEM` 的），所以那里的
  `~/.config/sol/sol.yaml` 不是你的那份：单元里要用 `--config` 给绝对路径（上面的例子就是这么写的），
  或者把文件放在 `/etc/sol/sol.yaml`。
- unit 只有在用特权端口或需要 `exec` 降权时才需要 root；其它情况下"专用用户 +
  `CAP_NET_BIND_SERVICE`"是更好的默认。macOS 上对应的做法是往 plist 里加 `UserName`；Windows 上
  `exec` 根本没有 user/group 降权。
- 用多端口时重复写 `--port`（或在配置文件里列 `rules`）。
- 控制台输出就是审计日志：配 `logging.output: file` 时改写到 `logging.file`，而且**不做轮转**——
  Linux 上配 `logrotate`、macOS 上配 `newsyslog`、Windows 上配一个限大小的任务。

## Ports and privileges

- 端口 **7、9 以及所有 1024 以下**的端口都是特权端口：非 root 时 bind 会 `permission denied`。
  要么以 root 跑，要么给能力——下面的 systemd 单元演示了专用用户 +
  `AmbientCapabilities=CAP_NET_BIND_SERVICE`。
- 只用高位端口（≥1024）则不需要任何特权。**推荐用高位端口**：它让保留的 WOL 端口保持有意义，
  也不需要额外能力。
- 带 `user`/`group` 的 `exec` 要求 sol 以 root 运行，否则启动即 `ErrNotRoot`——降权绝不会被静默
  跳过。**只接受 root**：判定就是 `geteuid() == 0`，所以给非 root 进程只授
  `CAP_SETUID`/`CAP_SETGID` 会被拒绝，而不是做一半。命令随后以目标账号的组运行，绝不继承 sol
  自己的附加组。
- 除非配了 TLS 与 mTLS，控制面应保持只听 `127.0.0.1`。

## Choosing interfaces

`sol ifaces` 列出每张网卡的 MAC/IPv4 以及自动模式是否会选它——在信任一个不带 `--iface` 的启动前
很有用：

```
NAME             TYPE      STATUS  MAC                IPV4           AUTO
lo               loopback  up                         127.0.0.1      no
enp6s0           physical  up      00:11:22:33:44:55  192.168.0.120  yes
wlp5s0           physical  down    0a:e8:9e:0f:d3:8d  -              yes
docker0          virtual   up      02:42:4e:d9:8c:14  172.17.0.1     no
```

`sol ifaces --json` 输出同样的列表，供脚本使用。

AUTO 列说的是**身份，不是当下可用性**：现在 down 的网卡仍然是这台机器的一部分（上表的 `wlp5s0`），
因为它随时可能起来——而且起来时 MAC 还可能变（无线网卡在 down 时内核可能报一个随机占位地址，
起来后才换成真地址）。所以监听进程会在运行期重读网卡列表：一个没命中任何规则的包会触发一次重读
（最多每秒一次），外加一个 30 秒的轮询，覆盖"一直没收到包"的机器。变化会以 `interface set changed`
记进审计日志，点名新增/消失的网卡。**新增** 会在下一个包到达后一秒内生效；**消失** 的网卡会一直
匹配到"下一个未命中的包"或"下一次轮询"为止，也就是最多 30 秒——想立刻生效就 `SIGHUP`（或
`--watch`）。显式写出的 `interfaces: [x]` 仍在启动期校验：名字不存在就是拼错了，直接报错。

## Testing your setup

在"应该做出反应"的那台机器上，前台带 `--dry-run` 起 sol：

```bash
sol listen --config /etc/sol/sol.yaml --dry-run
```

再从同一网络里的另一台机器发**一个**魔法包：6 个 `0xff` 字节后面跟目标 MAC 重复 16 次，一共 102
字节，而且必须从偏移 0 开始。MAC 用 `sol ifaces` 打出来的那个，把下面的 `00:11:22:33:44:55`、
`192.168.0.120` 和端口 `10010` 换成你自己的。

Linux 与 macOS，用 `wakeonlan`：

```bash
wakeonlan -i 192.168.0.120 -p 10010 00:11:22:33:44:55
```

任何有 Python 3 的地方——不用装工具：

```bash
python3 -c "import socket; mac=bytes.fromhex('001122334455'); socket.socket(socket.AF_INET, socket.SOCK_DGRAM).sendto(b'\xff'*6+mac*16, ('192.168.0.120', 10010))"
```

Windows，PowerShell：

```powershell
$mac = 0x00,0x11,0x22,0x33,0x44,0x55
$packet = [byte[]]((1..6 | ForEach-Object { 0xff }) + (1..16 | ForEach-Object { $mac }))
$udp = New-Object Net.Sockets.UdpClient
$null = $udp.Send($packet, $packet.Length, "192.168.0.120", 10010)
$udp.Close()
```

该看什么：

- 日志里：`magic packet matched`，带端口、网卡和动作；或者 `non-matching packet` 带长度——长度不是
  102（配了 `secure_on` 是 108，再加 `auth: hmac` 再多 8 字节）就说明这不是一个纯魔法包；
- 控制面开着时看 `/v1/status`：`packets`、`matched` 以及各动作的计数；
- 带 `--dry-run` 时动作行会记日志但不会执行——测试就安全地停在这里。确认规则可信之后，去掉
  `--dry-run` 再跑一次。

## Troubleshooting

| 现象 | 该查什么 |
| --- | --- |
| `bind: permission denied` | 端口 7/9 与任何 <1024：要么 root，要么给 `CAP_NET_BIND_SERVICE`（高位端口两者都不需要） |
| 包到了但什么都没发生 | `sol ifaces`（那张网卡是不是 `AUTO yes`？）、规则的 `ports` 与 `mac`，以及日志：`non-matching packet` 会写出端口和载荷长度 |
| 日志说 `length=...` 然后跳过 | 魔法包必须从偏移 0 开始、正好 102 字节（配 `secure_on` 是 108，配 `auth: hmac` 再多 8）；保留端口上带载荷的包按设计会被拒 |
| `--port 9` 不再关机了 | 7 与 9 保留给纯 WOL、恒为 `noop`；把动作挪到高位端口，或加 `--allow-reserved-actions` |
| `exec user/group requires root` | `user:`/`group:` 降权需要 root，Windows 上则完全不存在 |
| 无线网卡或热插拔的网卡匹配不上 | 身份集合在 sol 运行期会重读（日志里的 `interface set changed`）；`SIGHUP` 可以立刻应用当前列表 |
| 控制面回 401 | 它只听 `127.0.0.1` 且强制认证：导出 `SOL_TOKEN`，并带 `Authorization: Bearer ...` |
| reload 好像没生效 | 被拒的 reload 会**故意**保留旧配置；原因在日志里（`POST /v1/reload` 也会回给你） |

## Security Notes

Wake-on-LAN 是无认证广播：任何能摸到监听端口的人都能发出一个合法魔法包。因此 SoL

- 在保留端口上绝不越过 `noop`，除非你明确要求，
- 默认严格匹配（规则只在它点名的内容上触发），
- 提供 `src_cidrs` 限制哪些网段可以触发某条规则，
- 可以要求整包的 HMAC tag（`security.packet_auth` + `match.auth: hmac`），证明发送方持有密钥，
  并用它的 `window` 限制重放（远端命令与裸 shell 通道同理，各自有独立的 window），
- 远端命令要求 HMAC 认证、严格的命令白名单与逐参数校验，并且默认关闭该通道，
- 控制面只监听本机且强制认证，
- 命令默认以 argv 执行、不走 shell，除非显式写 `shell: true`，
- 并且记录每一个决策，所以规则集可以拿着日志对着现实核对。

最强的边界仍然是网络：把监听端口放在防火墙规则或独立 VLAN 后面；发送方已知时，给每条规则都配上
`src_cidrs`。

## Migration notes (breaking changes)

完整列表见 [CHANGELOG.md](CHANGELOG.md)。其中两条会影响既有安装：

- **`--port 9` 不再关机。** 端口 7 和 9 保留给纯 WOL，现在恒为 `noop`。把动作挪到高位端口
  （推荐），或加 `--allow-reserved-actions` 保留旧行为。
- **`--iface` 不再必填。** 不写它时，每张合格网卡都参与匹配；用 `sol ifaces` 确认具体是哪几张。

## Inspiration

项目的灵感来自 Habr 上的文章 ["Выключаем компьютер через Wake-on-Lan"](https://habr.com/ru/articles/816765/)：
它演示了如何把 Wake-on-LAN 包改造成"关机"而不是"唤醒"。

## Attribution

SoL 由 [bavix](https://github.com/bavix) 编写，MIT 许可。本仓库是它的 fork：
[lidiaoo/sol](https://github.com/lidiaoo/sol)，**完整保留**原始的版权声明与许可证正文——两份声明都在
[LICENSE](LICENSE) 里。

在上游之上，这个 fork 承载了 [docs/routing-design.md](docs/routing-design.md) 与
[CHANGELOG.md](CHANGELOG.md) 里记录的工作：带配置文件的规则/动作模型、多端口与多网卡路由、远端命令
通道与裸 shell 通道、HTTP 控制面、出站 HTTP 动作、热重载、各类护栏（冷却、限流、执行中去重、重放
窗口），以及它自己的 module 路径（`github.com/lidiaoo/sol`）与由此构建的发布产物。上游最后一个
tag 是 v0.0.2，上述全部都在它之后——升级既有安装前请先读 CHANGELOG。

## License

MIT。两份版权声明都在 [LICENSE](LICENSE) 里：原始的那份来自
[bavix](https://github.com/bavix)，另一份属于本 fork。
