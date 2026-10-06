# SoL 安装 · 覆盖升级 · 卸载 · 状态查看设计草案

状态：设计讨论定稿（待实现）
范围：安装脚本（Linux / macOS / Windows）、安装台账与安装报告、状态查看子命令、覆盖安装与升级、卸载、包管理器清单、Hermes skill
关联：路由与配置本身见 [routing-design.md](routing-design.md)；落地清单见 [TODO.md](../TODO.md)；使用者视角见 [README](../README.md)；升级注意事项见 [CHANGELOG](../CHANGELOG.md)

## 目录

- 1 目标与非目标 · 2 交付形态与输出约定 · 3 安装台账 · 4 安装报告
- 5 状态查看子命令（status / paths / config check）· 6 安装流程与探测决策
- 7 覆盖安装与升级 · 8 卸载 · 9 各平台差异与文件清单
- 10 验证与证据强度 · 11 包管理器清单 · 12 落地顺序 · 13 未决项

## 1 目标与非目标

**目标**：在任意一台机器上，一条命令装好 sol；装完能看懂它到底做了什么、文件落在哪；随时能查状态；能干净卸掉；能安全地覆盖升级。三个平台行为一致，且每一步都有可验证的证据。

**非目标**（明确不做，避免反复讨论）：

- **Docker 官方镜像**：sol 要动宿主电源、要绑宿主端口，容器里做"接收端"语义不对。
- **`sol upgrade` 自更新**：等于把一个下载器装成 root 服务，安全面太大。升级交给安装脚本重跑或包管理器。
- **静默 `curl | sudo bash`**：脚本支持管道执行，但默认不偷偷 sudo、不偷偷装服务。
- **实时内省**：进程内的包数 / 匹配数 / 最近事件归 `/v1/status`（控制面），本文的三个子命令不做这件事。

## 2 交付形态与输出约定

三层交付：

| 层 | 交付物 | 面向谁 |
| --- | --- | --- |
| 仓库真资产 | `scripts/install.sh`（Linux/macOS，POSIX）、`scripts/install.ps1` + `scripts/install.cmd`（Windows：双击入口，内部按对的执行策略调 ps1）、README 一行安装、包管理器清单 | 用户 |
| 查询子命令 | `sol status` / `sol paths` / `sol config check` | 用户与脚本/CI |
| Hermes skill | `sol-install`（决策树 + `references/{linux,macos,windows}.md`） | Agent |

skill 不重述命令，只做决策树与验证：调用安装脚本、判断"此前是怎么装的"、装完给出摘要与下一步。

### 输出约定（三个子命令通用，照顾"小白"）

1. **默认人话，`--json` 才是给脚本的**。默认输出面向人：分段、给结论、末尾**给一条可直接复制的命令**。
2. **术语白话**。台账里是 `method: script`，给人看时写"由安装脚本安装"；`未纳管` 要跟一句解释。
3. **退出码固定**：`0` = 一切正常 / `1` = 存在问题（配置无效、服务不在、二进制被替换）。CI 与脚本据此判断。
4. **不需要特权**。三个命令都不需要 root；若某条信息确实需要特权才能取，明确写出"这条要看需要 root，命令是……"，而不是静默省略。
5. **单一职责**：一个命令回答一个问题，不合并成 `sol info --all`。
6. **`sol status` 是默认入口**。小白只需要记这一条命令，其余命令由 status 在输出里列出来。

## 3 安装台账（install.json）

**路径**：Linux/macOS `/usr/local/share/sol/install.json`；Windows `C:\ProgramData\sol\install.json`。

```json
{
  "schema": 1,
  "method": "script",
  "installed_version": "v0.3.0",
  "installed_at": "2026-10-06T11:20:00+08:00",
  "prefix": "/usr/local/bin",
  "binary": "/usr/local/bin/sol",
  "sha256": "…",
  "service": {
    "kind": "systemd",
    "name": "sol.service",
    "unit_path": "/etc/systemd/system/sol.service",
    "created_unit": true,
    "created_user": false,
    "cap": "CAP_NET_BIND_SERVICE"
  },
  "firewall_rules": [{ "tool": "ufw", "spec": "10010/udp", "created_by": "installer" }],
  "config_paths": ["/etc/sol/sol.yaml"],
  "log_paths": ["/var/log/sol/sol.log"],
  "previous": { "installed_version": "v0.2.1", "sha256": "…" },
  "incomplete": false
}
```

为什么它在**设计期**就要定：卸载、升级、状态查询三件事全部依赖它；台账不在就只能靠猜，而猜着删别人的系统是最糟的失败模式。

- `incomplete: true`：安装/升级中途失败时置位，卸载时据此收尾，`sol status` 据此提示。
- `previous`：回滚依据（配合 `sol.bak`）。
- `config_paths` / `log_paths`：**报告用**，卸载默认不删（见 §8）。

## 4 执行报告

执行只给**简短**一段：装了什么、文件在哪、怎么验证、接下来做什么。完整动作清单不刷屏，而是写进 `install.log`（每行 = 做了什么 + 等价命令 + 是否 root），报告里给路径。

```
== 完成 ==
sol v0.3.0（用你提供的 ./sol）-> /usr/local/bin/sol（systemd 服务已启用）

安装配置   ~/.config/sol/install.yaml        改这里，然后重跑脚本
运行配置   /etc/sol/sol.yaml（还不存在；示例见 README Quick start）
台账       /usr/local/share/sol/install.json
历史       /usr/local/share/sol/install.log

校验
  /usr/local/bin/sol --version        -> sol version v0.3.0 (abc1234)
  systemctl is-active sol.service     -> active

接下来
  systemctl status sol.service
  sol status
```

报告的"校验"那两行按平台换：Linux `systemctl is-active sol.service` / macOS `launchctl print system/com.lidiaoo.sol` / Windows `schtasks /Query /TN sol`。

**落盘**：简短报告 + 完整动作清单追加写 `install.log`（带时间戳，install / upgrade / uninstall 每次一段）。

**不说谎规则**（写进脚本实现与单测）：

1. 值必须来自现场：sha256 是下载后**重算**的；版本是**真执行** `sol --version` 拿到的输出，不是把请求的版本号照抄。
2. 只打印**路径**，绝不打印配置内容与环境变量值（密钥安全）。
3. 需要 root 的步骤标 `(root)`；用户后续需要 root 做的事单独列。
4. 失败也要报告：已完成 / 未完成 / 如何回滚，并把台账置 `incomplete`。

## 5 状态查看子命令

| 命令 | 回答的问题 | 特权 | 退出码 | `--json` |
| --- | --- | --- | --- | --- |
| `sol status` | 装了没 / 哪个版本 / 健康吗（**默认入口**） | 不需要 | 0 正常，1 有问题 | ✅ |
| `sol paths` | 文件都在哪：二进制 / 配置（含来源）/ 候选路径 / 日志 / 台账 | 不需要 | 0 | ✅ |
| `sol config check` | 这份配置能被新版本接受吗，哪条规则有问题、怎么修 | 不需要 | 0 通过，1 不通过 | ✅ |

### 5.1 `sol status`

```
SoL 状态
  版本        sol version v0.3.0 (abc1234)
  二进制      /usr/local/bin/sol        sha256 与台账一致 ✓
  安装方式    由安装脚本安装（2026-10-06 11:20，前缀 /usr/local/bin）
  服务        systemd sol.service —— 本进程由 systemd 启动（INVOCATION_ID 存在）
              查看是否活着：systemctl status sol.service
  配置        /etc/sol/sol.yaml（来自 --config）—— 校验通过 ✓
  日志        stderr（systemd 下即 journald）
  监听端口    10010/udp、7/udp（保留端口，动作固定 noop）

下一步
  systemctl status sol.service ... 看服务
  sol config check ... 改完配置后自查
```

- **未安装 / 无台账时照样能跑**（从 PATH 定位自己），报"未纳管：这个二进制不是安装脚本装的"——这就是"装没装"的答案。
- **二进制 sha256 与台账比对**：不一致说明二进制被换过（被包管理器覆盖、或有人手动替换）。这是"未纳管 / 被顶掉"的可观测信号。
- **服务状态**：只报本地能证明的事实——台账怎么记的、当前进程是否由服务管理器启动（systemd 的 `INVOCATION_ID` / `JOURNAL_STREAM`，launchd 的 `XPC_SERVICE_NAME`），以及**用户自己可以跑的那条平台命令**。sol 不去调 `systemctl`/`launchctl`/`schtasks`，避免把自己焊死在某个 init 系统上。
- **权限告警**：配置里用了 <1024 / 保留端口但当前不是 root 也没有 CAP、`logging.output: file` 但目录不可写、`auth: hmac` 但密钥环境变量缺失——这些本来就是启动期会拒的，提前在这里说清并给修法。

### 5.2 `sol paths`

二进制真实路径（`os.Executable()`，解 symlink）、**实际生效**的配置文件路径 + 来源（`--config` / `$SOL_CONFIG` / 系统路径 / 用户路径 / 都没有）、按优先级排列的候选路径与命中项、日志目的地、台账路径。回答"东西在哪"，不依赖安装脚本还在不在。

**已实现**（`cmd/paths.go` + `internal/config/discover.go` + `internal/install/paths.go`；冒烟 s32 真机 25/25）：

- **来源判定只有一份实现**：`config.Discover` 同时服务 `Load`、这条命令和启动日志行，三者不可能给出不同答案。
- 候选就是 `config.DefaultPaths()`，命中项打 `*`；日志目的地取自配置，**配置坏了也照常给路径**（只多一行 `configuration not usable: <err>`），退出码恒为 0。
- 除设计里的五项，还打印 `install config`（用户改的那份 `install.yaml`）与 `history`——否则"装完文件在哪"要跨两条命令看；末行给一条 `next`（当前是 `sol config check`）。
- 平台路径集中在 `internal/install`：macOS 与 Linux 同为 `/usr/local/share/sol` + `~/.config/sol/install.yaml`，Windows 为 `%ProgramData%\sol` + `%APPDATA%\sol\install.yaml`。将来 `sol status` 与安装脚本共用这一处。
- 台账目前只报**路径与是否存在**；"是否纳管 / 版本比对"要等第 3 步的写入方（`install.json`）落地。

### 5.3 `sol config check`

加载配置 + 跑**全部启动期校验**（保留端口、重复端口、歧义规则、secret 解析、日志目的地、allowlist 形式、动作参数、`sequence` 步数……），对每条问题给出**怎么修**。不建监听、不要特权。三处使用：用户改完配置自查、升级前的预检（§7）、覆盖安装失败后的诊断。

**已实现**（`cmd/check.go` + `deps.Builder.Validate()`；冒烟 s33 真机 40/40）：

- **校验只有一份实现**：`Builder.Validate()` 就是 `buildRuntime()`——监听进程启动时走的同一条路；`check` 不复制任何规则、动作或 allowlist 的判断，因此不可能"check 说没事、启动却被拒"。控制面另跑 `BuildHTTPServer()` 的认证/TLS 校验（它只组装，不监听）。
- **不做监听的证据**：冒烟里让 python 先占住配置里那个端口，`check` 仍然通过且 3ms 返回，之后没有残留进程——这是"只读文件"的可复现证据。
- **每条问题配一条修法**：提示表以各包**已有的错误哨兵**为 key（`wol.ErrDuplicatePort`、`config.ErrMissingEnvVar`、`outbound.ErrURLNotAllowed`……），所以文案不会和产生它的校验脱节；没有映射的错误就照原样打印（原文会点名出错的字段）。
- 顺带发现并修掉一个真跨平台缺陷：`exec.ErrNotRoot` / `exec.ErrUserUnsupported` 原本定义在 build-tag 文件里，CLI 一引用就会让另一个平台的构建失败 → 两个哨兵移到中性的 `executor.go`，并由 `GOOS=windows` / `darwin` 构建验证。
- 退出码 0/1；`--json` 给 `ok` / `loaded` / `problems[]` / `summary`，供升级预检（§7）与 CI 使用。`loaded=false` 表示**配置根本没读出来**（此时不打印摘要，避免"没有网卡、没有日志"这种误导）。

与 `--dry-run` 的分工：`check` 只验配置；`--dry-run` 会真的起服务收包但不执行动作。

### 5.4 配置发现顺序（以及安装 / 服务场景的坑）

实测顺序（`internal/config/load.go` 的 `resolvePath`）：

| 优先级 | 来源 | 显式路径不存在时 |
| --- | --- | --- |
| 1 | `--config <路径>` | **直接报错** `read config …: no such file or directory`（不回退） |
| 2 | `$SOL_CONFIG` | 同上（不回退） |
| 3 | `/etc/sol/sol.yaml` | 不存在则看下一个 |
| 4 | `~/.config/sol/sol.yaml` | 不存在则用内置默认 |
| 5 | 内置默认（只有动作注册表、零规则） | `sol listen` 报 `no rules configured: pass --port or set rules in the config file`，**拒绝启动** |

- **全平台一致**：自动发现只看上面两个路径。macOS 不是 `/usr/local/etc/sol/…`，Windows 也不是 `C:\ProgramData\sol\…`；要放那里必须显式 `--config`（Windows 的 `~` = `C:\Users\<你>`）。
- 之后叠加环境变量，最后叠加 CLI 参数（文件 → 环境变量 → CLI，CLI 胜）。
- **服务场景的坑**：服务有自己的家目录（systemd / launchd 是 root 的，Windows 是 `SYSTEM` 的），`~/.config/sol/sol.yaml` 会变成 `/root/.config/sol/sol.yaml`。所以服务单元必须用 `--config` 给**绝对路径**，或把文件放在 `/etc/sol/sol.yaml`——这是"装完服务配置莫名不生效"最常见的原因。
- 安装脚本默认**不创建**配置，只在报告里打印"生效配置：(无) → 将按此顺序查找：…"，并附一段可复制的最小配置；`--write-config` 才落盘，且只在目标不存在时写，绝不覆盖已有配置。

### 5.5 连带补一个真缺陷（已实现）

`sol listen` 启动时**没有**打出它读的是哪份配置。现在它是审计日志的第一行，字段名与 `sol paths` 一致：

```
time=... level=INFO msg="configuration file" path=/etc/sol/sol.yaml source=system
```

实测三种来源：无配置时 `path=` 为空且 `source=none`，`--config` 与 `$SOL_CONFIG` 分别报 `source=--config` / `source=$SOL_CONFIG`（冒烟 s32）。journald 里能直接看出"它读的是哪份"，`status` 与安装报告的结论因此可被独立核对。

## 6 安装流程与探测决策

**安装只做三件事：认出现状 → 生成一份"怎么跑"的配置 → 你确认之后才动手。**

1. **认出现状**：找 sol 二进制（脚本旁边、当前目录、`$PATH` 里就地用；都没有才去下载对应平台的最新版）+ PATH 上所有命中的 `sol`（`command -v -a` / `where`，各自跑一遍 `--version`）+ 安装台账 + 服务单元/计划任务是否存在 + 包管理器记录（`brew list` / `winget list` / `scoop list`）。
2. **生成配置**：只写"怎么跑"——`run.args`（§6.1）。服务类型、单元路径、CAP、防火墙工具都由脚本按平台自己推导，不进文件（用户不需要记平台差异）。
3. **确认**：把配置展示出来，问一句"执行吗？"。答 n 就什么都不做，文件留着，改完重跑即可。
4. **执行**：用现成的二进制 + 下面这张表；**要不要装成服务是现场问的**，默认沿用台账里上次的选择。
5. **报告**：见 §4。

| 探测结论 | 执行时的行为 |
| --- | --- |
| 台账在 + 二进制版本/哈希与台账一致 + `run.args` 与服务定义一致 | "已是最新，无需操作"（退出码 0） |
| 台账在 + 你换过二进制（哈希不同） | 提示"检测到二进制换了：v0.2.1 → v0.3.0"，更新服务定义并重启（§7） |
| 台账在 + 只改了 `run.args` | 重新生成服务定义并重启 |
| 异源（brew / winget / scoop 装的二进制） | 提醒"包管理器升级会覆盖它"，问是否继续 |
| 无台账但二进制存在 | 正常开始——它就是"你已有的二进制"，执行后写台账 |
| PATH 上存在第二个 sol | 报告并指出**哪个生效**，不擅自改 PATH |

**安装脚本没有任何命令行参数。** 一个都不加：用户不需要记任何东西，所有选择都由"生成的文件 + 交互问答"完成。

未安装时：

```
== 已生成安装配置 ==
/home/you/.config/sol/install.yaml

  # sol 怎么跑（改这里，然后重新跑一遍脚本）
  run: { args: [listen, --config, /etc/sol/sol.yaml] }

二进制：用你这里的 ./sol（v0.3.0，sha256 1a2b…）；想换版本就把它换掉
服务：装成 systemd 服务？[Y/n]

可以现在编辑配置（另开一个窗口也行），改完回车继续。

执行吗？[Y/n]
```

已安装时（同一份文件就是"当前配置"）：

```
检测到已安装 sol v0.3.0（systemd 服务在运行）

请选择：[回车 = 按配置应用 / u = 卸载 / r = 重新生成配置 / n = 退出]
```

卸载时会再问一句，危险动作默认否：`配置文件也一起删掉吗？[y/N]`。

- **想看上次做了什么**：报告里印出 `install.log` 的路径，完整动作清单都在里面（不需要 `--verbose` 这种开关）。
- **CI / 无人值守**：不给参数，用管道喂答案即可（`printf 'y\n' | bash install.sh`）。**检测到没有终端时只生成配置、不执行任何操作**——绝不猜"用户大概是想装"。
- 需要 root 的步骤（写 `/usr/local/bin`、写 unit、enable 服务）在执行阶段逐条 sudo，不做"整个脚本 sudo 跑"。
- 并发用锁（`flock` / Windows 锁文件）。执行前清理**自己**的残留（同名 unit/plist/task 且台账标了是它建的），否则 `enable` 会撞上旧单元。

### 6.1 本机安装配置（安装脚本生成，用户可改）

**位置固定**（没有"换个位置"的开关）：Linux / macOS `~/.config/sol/install.yaml`；Windows `%APPDATA%\sol\install.yaml`。用 sudo 跑时写到 `$SUDO_USER` 的家目录，而不是 `/root`——否则用户在自己的家目录里根本找不到这份文件。

**内容只有一个 `run`**，它回答"sol 用什么参数跑"。其余的东西都不进文件：二进制从哪来由脚本自己找（§6.2），装不装服务现场问，服务类型 / 单元路径 / CAP / 防火墙工具按平台推导。

```yaml
# 由 install.sh 生成：sol 怎么跑。改这里，然后重新跑一遍脚本。
run:
  args: [listen, --config, /etc/sol/sol.yaml]
```

- `run.args` 是**列表**而不是一行字符串——字符串没法校验（危险开关能混进来）、没法映射到 launchd 的 `ProgramArguments` 数组、没法 diff。每个 token 按 `sol listen` 的真实 flag 集合校验，未知 flag 报错。
- **未知键即报错**（与 sol 配置解析同一风格）。
- **不覆盖**已有文件：配置在就按它执行；要重新生成就在问答里选 `r`。"改配置 → 重跑脚本"是唯一的修改回路。
- **三层权威互不重叠**：这份配置是**输入**（怎么跑）；服务定义是**产物**（脚本生成，别手改）；`/etc/sol/sol.yaml` 是**权威运行配置**（sol 只认这个 + CLI/env，见 §5.4）。
- **漂移可见**：手改过 unit 之后，`sol status` 发现"unit 里的参数 ≠ 台账记录"就提示重跑脚本。

### 6.2 二进制从哪来

**用户已经有 sol 时脚本就地使用，什么都不下载**：依次找 ① 脚本所在目录的 `./sol`（Windows 是 `sol.exe`）② 当前目录 ③ `$PATH`。都没有才回退到下载对应平台的最新版 release（自动判 OS/arch + 校验，见 §10 的证据要求）。

想钉版本就自己放一个二进制——脚本不替用户决定版本，也不会因为"网上有新版"就去动你放的那个。

**落点由脚本按平台决定（不可配）**：Linux / macOS → `/usr/local/bin/sol`（当前用户没有写权限就用 `~/.local/bin/sol` 并提示怎么加 PATH）；Windows → `C:\ProgramData\sol\sol.exe` 并加进机器 PATH。macOS 上脚本还会处理下载文件的 quarantine 标记（`xattr -d com.apple.quarantine`），否则首次运行会被 Gatekeeper 拦住——这一步用户不需要知道。

## 7 覆盖安装与升级

```
认二进制（版本 + sha256）→ 配置预检（sol config check）→ 停服务 → 原子替换（旧的留 sol.bak）
      → 重新生成服务定义（run.args 变了才需要）→ 重启 → 回读运行中的 version/revision → 更新台账
```

没有"下载新版本"这一步：换版本就是**你换掉那个二进制**，脚本负责把它放到最终位置、更新服务定义、重启，并把变化说清楚。

**四个硬校验**：

1. **运行中的确实是新版**。Linux 上进程跑的是旧 inode：替换了文件但服务没重启，`sol --version` 看到的是新版、实际跑的是旧代码。所以重启后必须回读**运行中**的 version/revision（`/v1/status` 或启动日志行）。
2. **旧配置在新版本下能加载**。这是本仓库的真实痛点（从上游 v0.0.2 升上来：端口 9 的动作恒为 noop、`auth`/`secure_on` 语义收紧）。预检不通过就**中止覆盖**，旧版本原样继续跑——这是覆盖安装最重要的安全阀，也是"可回滚"的真正实现。
3. **服务状态与台账一致**（台账说装了服务、实际没有 → 报告点出）。
4. **旧版本留着能回滚**：`sol.bak` + 台账 `previous`。

**三平台陷阱**：

- Linux：旧 inode（上文）；`Restart=always` 没停就替换，会在替换瞬间重启一次。
- Windows：**正在运行的 exe 覆盖不了**（文件锁）→ 必须先 `schtasks /End` / 停进程再 `Move-Item`，失败要给明确指引并可重试。
- macOS：launchd `KeepAlive` 会在替换二进制时把它拉回来 → 先 `launchctl bootout`，替换后再 load。

**场景示例**：

- 重跑脚本、什么都没有变 → "已是最新，无需操作"。
- 你把 `./sol` 换成新版本 → "检测到二进制换了：v0.2.1 → v0.3.0"，预检 → 替换（旧的留 `sol.bak`）→ 重启 → 回读确认运行的是 `v0.3.0` → 台账加 `previous`。
- 你把二进制换成旧版本（降级）→ 不阻止但要说清："版本从 v0.3.0 降到 v0.2.1；端口 9 的语义已变更，见 CHANGELOG"。
- 只改了 `run.args`（比如加了 `--port`）→ 只重新生成服务定义并重启，二进制不动。
- 二进制是 winget 装的 → 提醒"下次 winget upgrade 会覆盖它"，问是否继续（继续就用它，不继续就退出）。
- 没有台账（你手工拷进来的）→ 正常开始，执行完写台账——不需要"接管"这种开关。

## 8 卸载

**四条原则**：

1. **先读台账再动手**；没有台账就**拒绝**，打印"能看到什么 + 手工删除的确切命令"，不提供一键强删——安装出错是没装上，卸载出错是删了别人的东西。
2. **只删自己建的**：系统用户、CAP、防火墙规则、PATH 改动——只动台账里标了是安装器创建的那些；用户自己设的 `SOL_TOKEN` 等只报告不动。
3. **默认不碰配置与日志**：保留 `/etc/sol/sol.yaml`、`~/.config/sol/`、日志，并打印"留了什么、在哪、怎么删"；卸载时会问一句"配置文件也一起删掉吗？[y/N]"，默认否。
4. **幂等 + 能从半成品恢复**：装到一半失败也要能卸干净（台账在第一个副作用前写入，逐项打勾）。

**各平台拆解**：

- Linux：`systemctl disable --now sol.service` → 删 unit → `daemon-reload` → `reset-failed`；装过 CAP 就 `setcap -r`；台账标了安装器建的系统用户才 `userdel`。
- macOS：`launchctl bootout system /Library/LaunchDaemons/com.lidiaoo.sol.plist` → 删 plist（顺序反了 `KeepAlive` 会把它拉回来）。
- Windows：`schtasks /Delete /TN sol /F` → `Remove-NetFirewallRule -DisplayName "sol (WoL)"` → 撤 PATH 项 → 删 `%ProgramData%\sol` 下的二进制与台账（配置保留，问过之后才删）。

**验收三条硬标准**：① 端口不再监听；② 服务单元/计划任务不存在；③ 配置文件仍在且脚本已打印其路径与删除方法。

## 9 各平台差异与文件清单

| | Linux | macOS | Windows |
| --- | --- | --- | --- |
| 二进制 | `/usr/local/bin/sol` | `/usr/local/bin/sol` | `C:\ProgramData\sol\sol.exe` |
| 二进制落点（脚本决定，不可配） | `/usr/local/bin`；当前用户没写权限时 `~/.local/bin` + 提示 PATH | 同 Linux | `C:\ProgramData\sol` + 机器 PATH |
| 台账 / 历史 | `/usr/local/share/sol/{install.json,install.log}` | 同 Linux | `C:\ProgramData\sol\{install.json,install.log}` |
| 安装配置（只有 `run.args`，用户可改） | `~/.config/sol/install.yaml` | 同 Linux | `%APPDATA%\sol\install.yaml` |
| 服务管理器 | systemd | launchd | 计划任务（纯 exe 不能当服务） |
| 装服务的额外文件 | `/etc/systemd/system/sol.service` | `/Library/LaunchDaemons/com.lidiaoo.sol.plist` | 计划任务 `sol` + 防火墙规则 |
| 日志去向 | systemd 收 stdout → journald | launchd 要 `StandardErrorPath` → `/usr/local/var/log/sol.log` | **计划任务不收集 stdout** → 必须 `logging.output: file` |
| 防火墙 | 检测到 `ufw` / `firewalld` 才加规则 | 默认不动（macOS 应用防火墙不拦 UDP 入站；开了就提示手动放行） | `netsh advfirewall` / `New-NetFirewallRule` |
| 首次进入方式 | `curl … install.sh \| sh` | 同 Linux，另加 quarantine 处理 | `irm … install.ps1 \| iex`，或双击 `install.cmd` |
| 覆盖陷阱 | 旧 inode、`Restart=always` | `KeepAlive` 拉回 | 运行中 exe 文件锁 |
| 特权端口 | root 或 `CAP_NET_BIND_SERVICE` | root | 无特权端口概念 |

二进制来源：你放的（脚本就地使用）或脚本回退下载（§6.2）。默认（不装服务）产生：二进制 + 台账 + 历史 + 安装配置；换过二进制后多一个 `sol.bak`。**运行配置与日志不由安装脚本创建**，只在报告里打印期望路径。

### 9.1 脚本生成的服务定义（三平台模板）

**Linux**（systemd，`/etc/systemd/system/sol.service`）：

```ini
[Unit]
Description=SoL listener
After=network-online.target

[Service]
ExecStart=/usr/local/bin/sol listen --config /etc/sol/sol.yaml
Restart=always
# 要用保留端口 7/9 或 <1024 端口时（否则注释掉）：
# AmbientCapabilities=CAP_NET_BIND_SERVICE
# CapabilityBoundingSet=CAP_NET_BIND_SERVICE
# User=sol

[Install]
WantedBy=multi-user.target
```

注册：`systemctl daemon-reload && systemctl enable --now sol.service`

**macOS**（launchd，`/Library/LaunchDaemons/com.lidiaoo.sol.plist`）：

```xml
<plist version="1.0">
<dict>
  <key>Label</key><string>com.lidiaoo.sol</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/sol</string>
    <string>listen</string>
    <string>--config</string>
    <string>/etc/sol/sol.yaml</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/usr/local/var/log/sol.log</string>
</dict>
</plist>
```

注册：`launchctl bootstrap system /Library/LaunchDaemons/com.lidiaoo.sol.plist`（旧写法 `load -w`）；看状态 `launchctl print system/com.lidiaoo.sol`。

**Windows**（计划任务，纯 exe 不能注册成服务）：

```powershell
schtasks /Create /TN sol /TR "\"C:\ProgramData\sol\sol.exe\" listen --config C:\ProgramData\sol\sol.yaml" /SC ONSTART /RU SYSTEM /RL HIGHEST /F
schtasks /Run /TN sol
schtasks /Query /TN sol /V /FO LIST
```

计划任务**不收集 stdout**，所以配置里要让 sol 自己写日志：`logging.output: file`（否则审计记录无处可去）。

三份模板都从**同一份配置的 `run.args`** 生成——这正是把参数放进文件的价值：改一次，三个平台一致。

### 9.2 三平台的首次进入方式（都零参数）

- **Linux / macOS**：`curl -fsSL https://github.com/lidiaoo/sol/releases/latest/download/install.sh | sh`（或下载后 `sh install.sh`）。
- **macOS 额外一步（脚本自己处理）**：下载来的二进制带 quarantine 标记，脚本执行 `xattr -d com.apple.quarantine`，否则首次运行会被 Gatekeeper 弹"无法验证开发者"。
- **Windows**：`irm https://github.com/lidiaoo/sol/releases/latest/download/install.ps1 | iex`；或双击随附的 `install.cmd`（它内部按正确的执行策略调用 ps1，用户不需要记 `-ExecutionPolicy`）。
- 三平台一致的部分：**零参数**、生成的配置形态相同（只有 `run.args`）、同样两次问答、默认不需要特权（除非要装服务，或用 <1024 / 保留端口）。

## 10 验证与证据强度

| 交付物 | 证据强度 | 方式 |
| --- | --- | --- |
| `sol status` / `paths` / `config check` | **真机全量** | 单测 + Linux 真机冒烟（沿用现有 `sNN` 机制） |
| `scripts/install.sh` | **真机全量** | Linux 真机跑：无二进制时下载回退 / 有二进制时就地使用 / 重跑无变化 / 换二进制（升与降）/ 只改 `run.args` / 异源提醒 / 无台账 / 卸载，并真发一个魔法包确认能起来 |
| `scripts/install.ps1`、macOS 路径、三平台产物模板（unit / plist / 计划任务） | **CI 证据**（否则只能标"仅语法级"） | 新增 `.github/workflows/install-smoke.yml`，matrix ubuntu/macos/windows 真跑脚本 + 校验 + `--version` + `ifaces` + 注册后确认服务真的起来了 |
| 包管理器清单 | **schema 级** | scoop/winget 的 JSON schema 校验；winget 在 CI 里装不了，只能标 |
| README 里的安装命令 | **真跑** | 沿用现有 readme 断言脚本（抽 README 片段真执行） |

报告与台账字段由单测断言，防止实现漂移（与 `schema/sol.schema.json` 的防漂移测试同一思路）。

## 11 包管理器清单

顺序按"能不能真验证"排：

1. **scoop**（Windows，纯 JSON，可 schema 校验，维护成本最低）
2. **winget**（要外部仓库/提交，依赖产物 URL 稳定）
3. **brew formula / AUR**：本机没有 macOS / Arch 实机，只能标"未验证"——要么先不做，要么交付时明确标注。

每一层在 README 里标注证据强度，不把"未验证"写成"支持"。

## 12 落地顺序

每步 = 一次可提交改动，阶段完成标准仍是 `make test` + `make lint`。

| # | 内容 |
| --- | --- |
| 1 | 本文档 + TODO 挂条目（本次） |
| 2 | `sol paths` ✅ + `sol config check` ✅（含 `listen` 的配置来源启动日志行 ✅） + `sol status` |
| 3 | 台账 / 报告契约落地（`install.json` schema + `install.log` 格式 + 校验脚本 + 单测） |
| 4 | `scripts/install.sh`：探测 → 生成配置（内容只有 `run.args`，不覆盖已有）→ 展示并确认 → 执行 / 卸载（预检 / 原子替换留 `sol.bak` / 回读运行版本）；**零命令行参数**，交互问答完成全部选择 + 真机冒烟 |
| 5 | `scripts/install.ps1`（同上一行：零参数、交互问答） |
| 6 | `.github/workflows/install-smoke.yml`（三平台 matrix） |
| 7 | scoop + winget 清单 |
| 8 | Hermes skill `sol-install` + `references/{linux,macos,windows}.md` |
| 9 | README 一行安装 + 修正 Windows 那段 `move sol.exe C:\Windows\System32` + 收口 |

## 13 未决项

1. **服务生命周期是否下沉到 `sol service {install,uninstall,status}`（Go 侧，跨平台）**，脚本只负责取二进制 + 校验 + 放置。理由：三套服务管理器写死在 bash/pwsh 里难以测试，而安装、升级、卸载、状态四处都要用同一套逻辑。若采纳，用户入口仍是安装脚本与 `sol status`，`sol service` 只是更下层的一条命令。
2. `sol status` 是否需要从日志尾部推断"最近一次动作"——当前设计不做（避免为了好看去解析日志）。
3. `sol status` 里"监听端口是否真的在听"能否做成免特权探测——当前只列配置端口 + 提示用 `--dry-run` 验证。