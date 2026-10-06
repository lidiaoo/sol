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
| 仓库真资产 | `scripts/install.sh`（Linux/macOS，POSIX）、`scripts/install.ps1`（Windows）、README 一行安装、包管理器清单 | 用户 |
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

- `incomplete: true`：安装/升级中途失败时置位，`--uninstall` 据此收尾，`sol status` 据此提示。
- `previous`：回滚依据（配合 `sol.bak`）。
- `config_paths` / `log_paths`：**报告用**，卸载默认不删（见 §8）。

## 4 安装报告

装完当场给三段式报告：

1. **摘要**（人话）：装了什么版本、二进制在哪、期望的配置文件路径、有没有装服务、接下来做什么。
2. **动作清单**：每行 = 做了什么 + 等价命令 + 是否 root + 结果。
3. **校验证据**：`sol --version` 的真实输出、下载后重算的 sha256、服务是否 active。

```
== 摘要 ==
已安装 sol v0.3.0 到 /usr/local/bin/sol（无服务，配置仍由你指定）

== 做了什么 ==
  (user) download  sol-v0.3.0-linux-amd64.tar.gz            -> /tmp/sol-XXXX
  (user) verify    sha256 与 checksums.txt 一致              -> ok
  (user) verify    /tmp/sol-XXXX/sol --version              -> sol version v0.3.0 (abc1234)
  (root) install -m 0755 /tmp/sol-XXXX/sol /usr/local/bin/sol
  (user) write     /usr/local/share/sol/install.json         -> 台账
  (user) append    /usr/local/share/sol/install.log          -> 安装历史

== 校验 ==
/usr/local/bin/sol --version  -> sol version v0.3.0 (abc1234)
/usr/local/bin/sol status     -> 未发现服务；配置路径尚未创建

== 接下来 ==
1) sol ifaces                     看哪张网卡会应答
2) sudo nano /etc/sol/sol.yaml    写配置（示例见 README Quick start）
3) sol listen --config /etc/sol/sol.yaml --dry-run   真发一个包试一次
```

`--dry-run` 用**完全相同的格式**输出，每行前面加 `would`：这就是"它到底要对我系统做什么"的可预览版本。

**落盘**：同一份报告追加写 `install.log`（带时间戳，`install`/`upgrade`/`uninstall` 每次一段），终端滚掉了还能 `--history` 回看。

**不说谎规则**（写进脚本实现与单测）：

1. 值必须来自现场：sha256 是下载后**重算**的；版本是**真执行** `sol --version` 拿到的输出，不是把请求的版本号照抄。
2. 只打印**路径**，绝不打印配置内容与环境变量值（密钥安全）。
3. 需要 root 的步骤显式标 `(root)`，并列出哪些文件的属主是 root；用户后续需要 root 做的事单独列。
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

### 5.3 `sol config check`

加载配置 + 跑**全部启动期校验**（保留端口、重复端口、歧义规则、secret 解析、日志目的地、allowlist 形式、动作参数、`sequence` 步数……），对每条问题给出**怎么修**。不建监听、不要特权。三处使用：用户改完配置自查、升级前的预检（§7）、覆盖安装失败后的诊断。

与 `--dry-run` 的分工：`check` 只验配置；`--dry-run` 会真的起服务收包但不执行动作。

### 5.4 连带补一个真缺陷

`sol listen` 启动时**没有**打出它读的是哪份配置。补一行启动日志：`msg="configuration" path=/etc/sol/sol.yaml source=--config`。这样 journald 里能直接看出"它读的是哪份"，也让 status 与安装报告的结论可被独立核对。

## 6 安装流程与探测决策

**第一步不是下载，是认出现状**：PATH 上所有命中的 `sol`（`command -v -a` / `where`，各自跑一遍 `--version`）+ 安装台账 + 服务单元/计划任务是否存在 + 包管理器记录（`brew list` / `winget list` / `scoop list`）。

| 现状 | 行为 |
| --- | --- |
| 同源（台账在）+ 同版本 | "已安装，无需操作"（退出码 0，不下载）；`--force` 才重装 |
| 同源 + 新版本 | 升级流水线（§7） |
| 同源 + 旧版本（降级） | 默认**拒绝**；`--allow-downgrade` 才做，并打印 breaking 提示 |
| 异源（brew / winget / scoop 装的） | **拒绝**，告诉用户用对应包管理器升级/卸载（否则两边互相覆盖） |
| 无台账但二进制存在 | 不猜：打印探测结果，要求 `--force` |
| PATH 上存在第二个 sol | 报告并指出**哪个生效**，不擅自改 PATH；装到新前缀需显式确认 |

需要 root 的步骤（写 `/usr/local/bin`、写 unit、enable 服务）在报告里单独列出并逐条 sudo，不做"整个脚本 sudo 跑"。

并发：加锁（`flock` / Windows 锁文件），避免 CI 与人同时装。装之前清理**自己**的残留（同名 unit/plist/task 且台账标了是它建的），否则 `enable` 会撞上旧单元。

## 7 覆盖安装与升级

```
停服务 → 配置预检（新版本 sol config check）→ 下载 + 校验 → 原子替换（旧的留 sol.bak）
      → 重启服务 → 回读运行中的 version/revision → 更新台账
```

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

- `install.sh` 直接重跑（同版本）→ "已安装 v0.3.0，无需操作。要强制重装用 --force"。
- `install.sh --version v0.3.1`（同源升级）→ 停服务 → 预检通过 → 替换 → 重启 → 回读确认 `v0.3.1` → 台账加 `previous`。
- `install.sh --version v0.2.1`（降级）→ 拒绝："当前 v0.3.0，降到 v0.2.1 需要 --allow-downgrade；注意端口 9 语义变更见 CHANGELOG"。
- 用户先前用 winget 装的，又跑 `install.sh` → 拒绝："检测到 winget 安装记录；请用 winget upgrade 升级，或用 winget uninstall 卸掉后再用脚本安装"。
- 用户手工 `cp sol /usr/local/bin/`（无台账）→ "未纳管：/usr/local/bin/sol 存在但无安装台账。要用脚本接管请加 --force（会先列出将要做的操作）"。

## 8 卸载

**四条原则**：

1. **先读台账再动手**；没有台账就拒绝瞎猜，打印如何手工确认，`--force` 需先列清单并确认。
2. **只删自己建的**：系统用户、CAP、防火墙规则、PATH 改动——只动台账里标了是安装器创建的那些；用户自己设的 `SOL_TOKEN` 等只报告不动。
3. **默认不碰配置与日志**：保留 `/etc/sol/sol.yaml`、`~/.config/sol/`、日志，并打印"留了什么、在哪、怎么删"；`--purge` 才删。
4. **幂等 + 能从半成品恢复**：装到一半失败也要能卸干净（台账在第一个副作用前写入，逐项打勾）。

**各平台拆解**：

- Linux：`systemctl disable --now sol.service` → 删 unit → `daemon-reload` → `reset-failed`；装过 CAP 就 `setcap -r`；台账标了安装器建的系统用户才 `userdel`。
- macOS：`launchctl bootout system /Library/LaunchDaemons/com.lidiaoo.sol.plist` → 删 plist（顺序反了 `KeepAlive` 会把它拉回来）。
- Windows：`schtasks /Delete /TN sol /F` → `Remove-NetFirewallRule -DisplayName "sol (WoL)"` → 撤 PATH 项 → 删 `%ProgramData%\sol` 下的二进制与台账（配置保留，`-Purge` 全删）。

**验收三条硬标准**：① 端口不再监听；② 服务单元/计划任务不存在；③ 配置文件仍在且脚本已打印其路径与删除方法。

## 9 各平台差异与文件清单

| | Linux | macOS | Windows |
| --- | --- | --- | --- |
| 二进制 | `/usr/local/bin/sol` | `/usr/local/bin/sol` | `C:\ProgramData\sol\sol.exe` |
| 台账 / 历史 | `/usr/local/share/sol/{install.json,install.log}` | 同 Linux | `C:\ProgramData\sol\{install.json,install.log}` |
| 服务管理器 | systemd | launchd | 计划任务（纯 exe 不能当服务） |
| 装服务的额外文件 | `/etc/systemd/system/sol.service` | `/Library/LaunchDaemons/com.lidiaoo.sol.plist` | 计划任务 `sol` + 防火墙规则 |
| 覆盖陷阱 | 旧 inode、`Restart=always` | `KeepAlive` 拉回 | 运行中 exe 文件锁 |
| 特权端口 | root 或 `CAP_NET_BIND_SERVICE` | root | 无特权端口概念 |

默认（不装服务）只产生：二进制 + 台账 + 历史；升级后多一个 `sol.bak`。**配置与日志不由安装脚本创建**，只在报告里打印期望路径。

## 10 验证与证据强度

| 交付物 | 证据强度 | 方式 |
| --- | --- | --- |
| `sol status` / `paths` / `config check` | **真机全量** | 单测 + Linux 真机冒烟（沿用现有 `sNN` 机制） |
| `scripts/install.sh` | **真机全量** | Linux 真机跑：装 / 重跑 / 升级 / 降级被拒 / 异源被拒 / 卸载 / `--purge`，并真发一个魔法包确认能起来 |
| `scripts/install.ps1`、macOS 路径 | **CI 证据**（否则只能标"仅语法级"） | 新增 `.github/workflows/install-smoke.yml`，matrix ubuntu/macos/windows 真跑脚本 + 校验 + `--version` + `ifaces` |
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
| 2 | `sol paths` + `sol config check` + `sol status`（含 `listen` 的配置来源启动日志行） |
| 3 | 台账 / 报告契约落地（`install.json` schema + `install.log` 格式 + 校验脚本 + 单测） |
| 4 | `scripts/install.sh`（探测 / 决策矩阵 / 预检 / 覆盖 / `--dry-run` / `--uninstall` / `--purge`）+ 真机冒烟 |
| 5 | `scripts/install.ps1` |
| 6 | `.github/workflows/install-smoke.yml`（三平台 matrix） |
| 7 | scoop + winget 清单 |
| 8 | Hermes skill `sol-install` + `references/{linux,macos,windows}.md` |
| 9 | README 一行安装 + 修正 Windows 那段 `move sol.exe C:\Windows\System32` + 收口 |

## 13 未决项

1. **服务生命周期是否下沉到 `sol service {install,uninstall,status}`（Go 侧，跨平台）**，脚本只负责取二进制 + 校验 + 放置。理由：三套服务管理器写死在 bash/pwsh 里难以测试，而安装、升级、卸载、状态四处都要用同一套逻辑。若采纳，用户入口仍是安装脚本与 `sol status`，`sol service` 只是更下层的一条命令。
2. `sol status` 是否需要从日志尾部推断"最近一次动作"——当前设计不做（避免为了好看去解析日志）。
3. `sol status` 里"监听端口是否真的在听"能否做成免特权探测——当前只列配置端口 + 提示用 `--dry-run` 验证。