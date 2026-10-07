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
| 发布产物 | `.goreleaser.yml` + `.github/workflows/release.yaml`（linux/darwin/windows × amd64/arm64，版本注入 `internal/buildinfo.version`；并把 `install.sh`/`install.ps1`/`install.cmd` 作为 release 资产上传——README 的一行安装指向 `releases/latest/download/install.sh`，此前从没上传过，一直是 404）。**未跑过**：fork 不再用上游 `bavix/.github` 的复用工作流 | 用户 |
| 仓库真资产 | `scripts/install.sh`（Linux/macOS，POSIX）、`scripts/install.ps1` + `scripts/install.cmd`（Windows：双击入口，内部按对的执行策略调 ps1）、README 一行安装、包管理器清单 | 用户 |
| 装完要能回答的问题（现状 / 文件清单 / 预检） | 由安装脚本给，零参数 | 用户与脚本/CI |
| Hermes skill | `sol-install`（决策树 + `references/{linux,macos,windows}.md`） | Agent |

skill 不重述命令，只做决策树与验证：调用安装脚本、判断"此前是怎么装的"、装完给出摘要与下一步。

### 输出约定（三个子命令通用，照顾"小白"）

1. **默认人话，`--json` 才是给脚本的**。默认输出面向人：分段、给结论、末尾**给一条可直接复制的命令**。
2. **术语白话**。台账里是 `method: script`，给人看时写"由安装脚本安装"；`未纳管` 要跟一句解释。
3. **退出码固定**：`0` = 一切正常 / `1` = 存在问题（配置无效、服务不在、二进制被替换）。CI 与脚本据此判断。
4. **不需要特权**。三个命令都不需要 root；若某条信息确实需要特权才能取，明确写出"这条要看需要 root，命令是……"，而不是静默省略。
5. **单一职责**：一个命令回答一个问题，不合并成 `sol info --all`。
6. **重跑安装脚本就是默认入口**。小白只记这一条命令：它先报现状（装了什么、文件在哪、服务形态、台账），再问 应用/卸载/重新生成/退出。

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
  "firewall_rules": [
    { "tool": "ufw", "spec": "10/udp", "created_by": "installer" },
    { "tool": "ufw", "spec": "11/udp", "created_by": "installer" },
    { "tool": "ufw", "spec": "12/udp", "created_by": "installer" }
  ],
  "config_paths": ["/home/you/sol.yaml"],
  "log_paths": ["/var/log/sol/sol.log"],
  "previous": { "installed_version": "v0.2.1", "sha256": "…" },
  "incomplete": false
}
```

为什么它在**设计期**就要定：卸载、升级、状态查询三件事全部依赖它；台账不在就只能靠猜，而猜着删别人的系统是最糟的失败模式。

- `incomplete: true`：安装/升级中途失败时置位，卸载时据此收尾，重跑脚本时据此提示。
- `previous`：回滚依据（配合 `sol.bak`）。
- `config_paths` / `log_paths`：**报告用**，卸载默认不删（见 §8）。

## 4 执行报告

执行只给**简短**一段：装了什么、文件在哪、怎么验证、接下来做什么。完整动作清单不刷屏，而是写进 `install.log`（每行 = 做了什么 + 等价命令 + 是否 root），报告里给路径。

```
== 完成 ==
sol v0.3.0（用你提供的 ./sol）-> /usr/local/bin/sol（systemd 服务已启用）

安装配置   /home/you/install.yaml        改这里，然后重跑脚本
运行配置   /home/you/sol.yaml（刚生成的示例配置：三条规则，按需改）
台账       /usr/local/share/sol/install.json
历史       /usr/local/share/sol/install.log

校验
  /usr/local/bin/sol --version        -> sol version v0.3.0 (abc1234)
  systemctl is-active sol.service     -> active

接下来
  systemctl status sol.service
  重跑安装脚本可以随时看现状（它会先报再问；答 n 退出，什么都不改）
```

报告的"校验"那两行按平台换：Linux `systemctl is-active sol.service` / macOS `launchctl print system/com.lidiaoo.sol` / Windows `schtasks /Query /TN sol`。

**落盘**：简短报告 + 完整动作清单追加写 `install.log`（带时间戳，install / upgrade / uninstall 每次一段）。

**不说谎规则**（写进脚本实现与单测）：

1. 值必须来自现场：sha256 是下载后**重算**的；版本是**真执行** `sol --version` 拿到的输出，不是把请求的版本号照抄。
2. 只打印**路径**，绝不打印配置内容与环境变量值（密钥安全）。
3. 需要 root 的步骤标 `(root)`；用户后续需要 root 做的事单独列。
4. 失败也要报告：已完成 / 未完成 / 如何回滚，并把台账置 `incomplete`。

## 5 装完要能回答的问题（由安装脚本回答，不做成 sol 子命令）

安装脚本必须把这三件事说清楚，而**不在 sol 本体里新增子命令**：用户已经有可执行文件，安装体验不该让二进制为它变复杂。

| 问题 | 谁来回答 | 怎么回答（都零参数） |
| --- | --- | --- |
| 装了没 / 哪个版本 / 文件都在哪 | 安装脚本自己 | 每次跑脚本先"认出现状"，第一屏就打印：二进制真实路径与版本、生效配置与来源、服务形态、台账、日志去处；然后才问 应用/卸载/重新生成/退出 |
| 这份配置会不会被拒 | 安装脚本 + 真二进制 | 预检 = 用 `sol listen --dry-run` 起一次（几秒内非零退出并把拒绝原因写到 stderr 即不合格），随后杀掉；通过才动服务 |
| 覆盖安装 / 升级有没有问题 | 安装脚本 | 版本与 sha256 比对（台账）、`run.args` 与服务定义比对、异源提醒、`sol.bak` 回滚点 |

- **重跑安装脚本就是"看状态"**：零参数、先报告后询问，答 `n` 什么都不改——所以不需要 `sol status`。
- **`--dry-run` 预检是唯一可信的配置校验**：它和真正启动走同一条路（同一个二进制、同一份配置），不需要 root（高位端口），被拒时把 stderr 原文给用户看，并附"怎么修"。
- **权限与可写性告警也在脚本里判**：端口 <1024 而 `id -u` 非 0、`logging.output: file` 的目录不存在或不可写——平台差异天然由脚本分支处理，不用进 sol。
- **台账 `install.json` 由脚本写、由脚本读**：`schema` 写在文件里，读到未来的版本就拒绝并提示升级脚本。台账不含密钥，因此 0644 可读（600 会让普通用户的后续查询读不了它）。
- **不碰 sol 的启动日志**：脚本自己知道它生成了哪份配置，报告里直接写；给 `sol listen` 加"生效配置 + 来源"那行留作未决项（§13）。


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
- **服务场景的坑（靠绝对路径避开，Windows 还靠"配置跟二进制放一起"）**：服务有自己的家目录（systemd / launchd 是 root 的，Windows 是 `SYSTEM` 的），相对路径与 `~` 进到服务里全变样。脚本生成 `install.yaml` 时把 `--config` 写成**绝对路径**：Linux/macOS 指向执行目录那份 `sol.yaml`，Windows 指向**安装目录**那份（`C:\ProgramData\sol\sol.yaml`）——后者跟 `sol.exe` 做伴，不依赖某个可能被挪走、删掉的项目目录。这也是 README 里手工装 Windows 服务的命令一直以来的写法，脚本现在与它一致。这是"装完服务配置莫名不生效"最常见的原因。
- 运行配置由脚本**只在它不存在时**生成一份开箱即用的示例（三条规则：纯包关机 / magic+"reboot" 重启 / magic+"sleep" 睡眠；另把两个默认护栏**显式写在文件里**——`security.settle: 5s` 与三个电源动作各 5s 的 `security.cooldowns`，用户一眼看见默认值、也一眼知道怎么关），**绝不覆盖**已有文件；路径与查找顺序见 §6。这一节早先写的 `--write-config` 开关属于被撤掉的 CLI 方案，脚本现在是零参数的。

### 5.5 连带补一个真缺陷

`sol listen` 启动时**没有**打出它读的是哪份配置。补一行启动日志：`msg="configuration" path=/etc/sol/sol.yaml source=--config`。这样 journald 里能直接看出"它读的是哪份"，也让 status 与安装报告的结论可被独立核对。

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

在你执行脚本的那个目录里（下面假设是 /home/you；Windows 上这里只有 `install.yaml`，运行配置在安装目录，见 §6.1）：

  install.yaml            # sol 怎么跑（改这里，然后重新跑一遍脚本）
  sol.yaml                # 刚生成的示例配置：三条规则，按需改

```
== 已生成配置 ==
/home/you/install.yaml
/home/you/sol.yaml        # 示例配置，服务因此不会拒绝启动

  # install.yaml 的内容
  run: { args: [listen, --config, /home/you/sol.yaml] }

二进制：用你这里的 ./sol（v0.3.0，sha256 1a2b…）；想换版本就把它换掉
服务：装成 systemd 服务？[Y/n]

可以现在编辑配置（另开一个窗口也行），改完回车继续。

执行吗？[Y/n]
```

已安装时（同一份文件就是"当前配置"）：

```
检测到已安装 sol v0.3.0（systemd 服务在运行）

选项（编号列表，回车＝1）：

  1  按配置应用（按 install.yaml 里的 run.args 装或升级）
  2  卸载（摘掉服务，按台账删掉自己建的东西）
  3  重新生成 install.yaml
  4  退出，什么都不改

请选择 [1]:
```

卸载时会再问一句，危险动作默认否：`配置文件也一起删掉吗？[y/N]`。

- **想看上次做了什么**：报告里印出 `install.log` 的路径，完整动作清单都在里面（不需要 `--verbose` 这种开关）。
- **CI / 无人值守**：不给参数，用管道喂答案即可（`printf 'y\n' | bash install.sh`）。**检测到没有终端时只生成配置、不执行任何操作**——绝不猜"用户大概是想装"。
- **"已安装但服务不在"这条路上必须留个出口**：台账里 `created_unit: false` 可能是"当时选了不装服务"，也可能是"上次没跑完"。只按台账走，用户就再也没机会说"我要服务"——所以单元不在时把那个问题**再问一次**（有 `SOL_INSTALL_ROOT` 或平台没有服务管理器时除外）。
- **三平台各自的编码陷阱（都表现为"跑不通"，且都不报错）**：`install.ps1` 必须带 UTF-8 BOM——`install.cmd` 用的是系统自带 `powershell.exe`(5.1)，读无 BOM 的 `.ps1` 会按 ANSI/GBK 解释，满篇中文变乱码甚至解析失败；`install.cmd` 要**纯 ASCII**（cmd.exe 按控制台代码页读文件）；`install.sh` **绝不能**有 BOM（会砸掉 `#!`）。生成的 YAML/JSON 一律用"UTF-8 且不带 BOM"写（5.1 的 `-Encoding UTF8` 带 BOM）。
- **shell 变量紧挨中文必须写 `${VAR}`**：macOS 的 `/bin/sh` 是 bash 3.2，`"$VAR（中文）"` 会把变量名解析成 `VAR` 加 `（` 的首字节，`set -u` 下直接 `unbound variable`，整只脚本死掉；Linux 的 bash 5 不会。这类写法在装脚本里一律加花括号，并由 install-smoke 的静态步骤 + macos job 兜住。
- **脚本必须能读懂自己写出来的配置**：`install.ps1` 曾把 `args: [...]` 一行写成五行（数组字面量里的字符串拼接被 PowerShell 当成多个元素），于是它自己 `Die`"没有 run.args"——这就是"Windows 跑不通"的直接原因。凡是"写出去再读回来"的路径都要有断言钉住。
- **端口解析要认两种写法**：`- match: { ports: [10010] }`（行内，示例与 README 都用它）与块写法。只认行首 `ports:` 时行内写法静默失效——低端口警告和防火墙规则全没了，表现就是"装完了收不到魔法包"。
- **防火墙是帮手，不是目的**：没有 NetSecurity 模块的 Windows（Server Core）上 `Get-NetFirewallRule` 是"命令不存在"，`-ErrorAction SilentlyContinue` 挡不住，会把整个安装带走。一律 try/catch + 提醒。
- **launchd 的日志目录必须先存在**：plist 里 `StandardOutPath`/`StandardErrorPath` 指向的目录不存在时任务直接起不来，所以写 plist 之前 `mkdir -p $PREFIX/var/log`。
- **要 root 就先升权，再动手**（不是半路一条条 sudo：密码问到一半、失败还被 `|| true` 吞掉）。规则：
  - Linux / macOS：动手之前用 `sudo` **重跑一遍自己**，把"你已经答过的答案"带过去（`--sol-action` / `--sol-service` / `--sol-run-dir`，内部参数，用户不需要知道），所以**不会再问一遍**；同一个执行目录，`install.yaml` 照旧落在那里（运行配置：Linux/macOS 也在那儿，Windows 在安装目录）。管道执行（`curl | sh`）没有可重跑的文件 → 退化为逐条 sudo 并说明。
  - Windows：不是管理员就 `Start-Process -Verb RunAs`（**触发 UAC**）以管理员身份重跑自己，`-WorkingDirectory` 保持同一个执行目录，答案同样带过去。往安装目录（`C:\ProgramData\sol`）写运行配置也留到这一步：没升权的那一次不抢着建那个目录，也不谎报"已生成"。
  - 沙箱/自选的根（`SOL_INSTALL_ROOT`）**永不升权**：那是你自己的地盘。
  - 升权失败（UAC 被拒 / 没有 sudo）→ 明确报错并给替代路径（换落点或换角色），而不是继续往下撞。
- 并发用锁（`flock` / Windows 锁文件）。执行前清理**自己**的残留（同名 unit/plist/task 且台账标了是它建的），否则 `enable` 会撞上旧单元。

### 6.1 本机安装配置（安装脚本生成，用户可改）

**位置固定**（没有"换个位置"的开关）：`install.yaml` 在**你执行脚本的那个目录**，三平台一致；运行配置 `sol.yaml` 在 Linux/macOS 也在那儿，**Windows 上在安装目录**——

- `./install.yaml`：脚本的**输入**（"sol 怎么跑"）。改完重跑脚本即可；已有就一个字都不覆盖。
- 运行配置 `sol.yaml`：脚本只在它不存在时生成一份**开箱即用**的——三条规则：纯包 → 关机（端口 11）、magic+"reboot" → 重启（12）、magic+"sleep" → 睡眠（10），并附一段显式的 `security:`（`settle: 5s` + 三个 5s 冷却）；服务没有配置文件会直接拒绝启动（crash-loop），而小白最可能的顺序就是先跑起来再改。
  - **Windows：放在安装目录**（`C:\ProgramData\sol\sol.yaml`），跟 `sol.exe` 做伴，计划任务跑的是安装目录里的 `run-sol.cmd`，`--config` 由它指着安装目录这份配置。执行目录里那份**不是**服务认的配置；你要是以前在执行目录改过 `sol.yaml`，重跑脚本会把它**原样拷进**安装目录，并把 `install.yaml` 里 `--config` 的取值换成安装目录的路径（内容不丢，原文件也不动）。
  - **为什么 Windows 不一样**：计划任务以 `SYSTEM` 开机就跑，配置放在某个用户的项目目录里容易被挪走、删掉，或者 `SYSTEM` 根本读不到。Linux/macOS 保持执行目录——配置跟你执行脚本的地方在一起，改完重跑即可。
- **端口 <1024 在 Linux/macOS 上要 root**，所以默认配置意味着：正常安装（动手前会升权）没问题；但"不要 root 的用户级安装"在这种配置下预检会失败（sol 报 `bind: permission denied`），那种场景得把端口改成 ≥1024。Windows 上低端口不需要特权，不受这一条影响。

用 sudo 跑也一样：这两件产物跟的是**当前目录**，不是任何人的家目录（放 `~/.config` 服务反而读不到——服务有自己的家目录）。

**内容只有一个 `run`**，它回答"sol 用什么参数跑"。其余的东西都不进文件：二进制从哪来由脚本自己找（§6.2），装不装服务现场问，服务类型 / 单元路径 / CAP / 防火墙工具按平台推导。

```yaml
# 由 install.sh 生成：sol 怎么跑。改这里，然后重新跑一遍脚本。
run:
  args: [listen, --config, /home/you/sol.yaml]
```

**为什么还要生成 `sol.yaml`**：没有配置文件的 sol 会**拒绝启动**（`no rules configured`），装成服务就会 crash-loop。所以脚本顺手写一份能跑的示例（三条规则：纯包关机 / magic+"reboot" 重启 / magic+"sleep" 睡眠，并在末尾显式写出 `security.settle` 与三个 5s 冷却——默认护栏要看得见，才谈得上"能关掉"），你在上面改端口和动作即可。`run.args` 里的 `--config` 是**绝对路径**（Linux/macOS：执行目录那份；Windows：安装目录那份），服务不会读错文件。

- `run.args` 是**列表**而不是一行字符串——字符串没法校验（危险开关能混进来）、没法映射到 launchd 的 `ProgramArguments` 数组、没法 diff。每个 token 按 `sol listen` 的真实 flag 集合校验，未知 flag 报错。
- **未知键即报错**（与 sol 配置解析同一风格）。
- **不覆盖**已有文件：配置在就按它执行；要重新生成就在问答里选 `r`。"改配置 → 重跑脚本"是唯一的修改回路。
- **三层权威互不重叠**：`install.yaml` 是**输入**（怎么跑）；服务定义是**产物**（脚本生成，别手改）；`sol.yaml` 是**权威运行配置**（sol 只认它 + CLI/env，见 §5.4）——放在哪由你的 `run.args` 决定，脚本只是给个默认（Linux/macOS：执行目录；Windows：安装目录）。
- **漂移可见**：手改过 unit 之后，重跑脚本会发现"unit 里的参数 ≠ 台账记录"并提示重新生成。

### 6.2 二进制从哪来

**用户已经有 sol 时脚本就地使用，什么都不下载**：依次找 ① 脚本所在目录的 `./sol`（Windows 是 `sol.exe`）② 当前目录 ③ `$PATH`。都没有才回退到下载对应平台的最新版 release（自动判 OS/arch + 校验，见 §10 的证据要求）。

想钉版本就自己放一个二进制——脚本不替用户决定版本，也不会因为"网上有新版"就去动你放的那个。

**落点由脚本按平台决定（不可配）**：Linux / macOS → `/usr/local/bin/sol`（当前用户没有写权限就用 `~/.local/bin/sol` 并提示怎么加 PATH）；Windows → `C:\ProgramData\sol\sol.exe` 并加进机器 PATH。macOS 上脚本还会处理下载文件的 quarantine 标记（`xattr -d com.apple.quarantine`），否则首次运行会被 Gatekeeper 拦住——这一步用户不需要知道。

## 7 覆盖安装与升级

```
认二进制（版本 + sha256）→ 停掉"正在跑的我们自己的那份"（预检绑不上端口时才需要：它占着同一个端口）→ 配置预检（`sol listen --dry-run` 起一次看是否被拒）→ 停服务 → 原子替换（旧的留 sol.bak）
      → 重新生成服务定义（run.args 变了才需要）→ 重启 → 回读运行中的 version/revision → 更新台账
```

没有"下载新版本"这一步：换版本就是**你换掉那个二进制**，脚本负责把它放到最终位置、更新服务定义、重启，并把变化说清楚。

**四个硬校验**：

1. **运行中的确实是新版**。Linux 上进程跑的是旧 inode：替换了文件但服务没重启，`sol --version` 看到的是新版、实际跑的是旧代码。所以重启后必须回读**运行中**的 version/revision（`/v1/status` 或启动日志行）。
2. **旧配置在新版本下能加载**。这是本仓库的真实痛点（从上游 v0.0.2 升上来：端口 9 的动作恒为 noop、`auth`/`secure_on` 语义收紧）。预检不通过就**中止覆盖**，旧版本原样继续跑——这是覆盖安装最重要的安全阀，也是"可回滚"的真正实现。（为了预检能绑上端口而临时停掉旧实例的情况：第二次预检仍不过就把它**按原样起回去**，不让用户白白少一个正在跑的服务。）
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
3. **默认不碰配置与日志**：保留 `install.yaml`（执行目录）、运行配置（Linux/macOS 执行目录，Windows 安装目录）与日志，并打印"留了什么、在哪、怎么删"；卸载时会问一句"配置文件也一起删掉吗？[y/N]"，默认否。
4. **幂等 + 能从半成品恢复**：装到一半失败也要能卸干净（台账在第一个副作用前写入，逐项打勾）。

**各平台拆解**：

- Linux：`systemctl disable --now sol.service` → 删 unit → `daemon-reload` → `reset-failed`；装过 CAP 就 `setcap -r`；台账标了安装器建的系统用户才 `userdel`。
- macOS：`launchctl bootout system /Library/LaunchDaemons/com.lidiaoo.sol.plist` → 删 plist（顺序反了 `KeepAlive` 会把它拉回来）。
- Windows：`schtasks /Delete /TN sol /F` → `Remove-NetFirewallRule -DisplayName "sol (WoL)"` → 撤 PATH 项 → 删 `%ProgramData%\sol` 下的二进制与台账（安装目录里的配置保留，问过之后才删；执行目录里你自己那份，脚本既不认也不动）。

**先停进程，再删东西**：卸载第一步是停掉我们那份还在跑的 sol——只认"可执行文件就是我们装的那个"，名字叫 sol 的别人的进程一概不碰。不这么做有两处坏：正在运行的 exe 在 Windows 上被文件锁着删不掉（卸载"成功"了二进制还在）；更要紧的是它继续占着 UDP 端口，下一次安装的预检就会以"端口占用"失败——用户看到的是"装不上"，其实只是旧进程还在跑。安装侧同理：升级时端口必然被自己占着，所以预检绑不上就先停自己那份、再试一次。

**验收三条硬标准**：① 端口不再监听；② 服务单元/计划任务不存在；③ 配置文件仍在且脚本已打印其路径与删除方法。

## 9 各平台差异与文件清单

| | Linux | macOS | Windows |
| --- | --- | --- | --- |
| 二进制 | `/usr/local/bin/sol` | `/usr/local/bin/sol` | `C:\ProgramData\sol\sol.exe` |
| 二进制落点（脚本决定，不可配） | `/usr/local/bin`；当前用户没写权限时 `~/.local/bin` + 提示 PATH | 同 Linux | `C:\ProgramData\sol` + 机器 PATH |
| 台账 / 历史 | `/usr/local/share/sol/{install.json,install.log}` | 同 Linux | `C:\ProgramData\sol\{install.json,install.log}` |
| 安装配置 `install.yaml`（只有 `run.args`） | 执行脚本的那个目录 | 同 Linux | 同 Linux |
| 运行配置 `sol.yaml`（开箱即用的示例配置：三条规则 + 显式写出的默认护栏 `settle: 5s` 与三个 5s 冷却，只在不存在时生成，绝不覆盖） | 执行脚本的那个目录 | 同 Linux | **安装目录** `C:\ProgramData\sol\sol.yaml`（跟 `sol.exe` 做伴，计划任务读的就是它；执行目录里那份会被拷过去） |
| 服务管理器 | systemd | launchd | 计划任务（纯 exe 不能当服务） |
| 装服务的额外文件 | `/etc/systemd/system/sol.service` | `/Library/LaunchDaemons/com.lidiaoo.sol.plist` | 计划任务 `sol` + 防火墙规则 |
| 日志去向 | systemd 收 stdout → journald（`journalctl -u sol.service`） | launchd 要 `StandardErrorPath` → `/usr/local/var/log/sol.log` | **计划任务不收集 stdout**：任务跑我们生成的 `run-sol.cmd`，里面 `… >> C:\ProgramData\sol\sol.log 2>&1`；想改用配置的 `logging.output: file` 也行 |
| 日志路径报给用户 | 现状与完成报告都打 `日志`/`服务日志` 一行；台账 `log_paths` 记文件（systemd 没有文件，故为空） | 同左（`log_paths` = `/usr/local/var/log/sol.log`） | 同左（`log_paths` = `C:\ProgramData\sol\sol.log`） |
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
ExecStart=/usr/local/bin/sol listen --config /home/you/sol.yaml
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
    <string>/home/you/sol.yaml</string>
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
schtasks /Create /TN sol /TR "C:\ProgramData\sol\run-sol.cmd" /SC ONSTART /RU SYSTEM /RL HIGHEST /F

# run-sol.cmd（脚本写它）：任务跑它才有日志留得住——计划任务的 XML 没有重定向，
# 而把 cmd /c "… >> …" 塞进 /TR 是一串嵌套引号；包装脚本把引号关在自己文件里。
@echo off
"C:\ProgramData\sol\sol.exe" listen --config "C:\ProgramData\sol\sol.yaml" >> "C:\ProgramData\sol\sol.log" 2>&1
schtasks /Run /TN sol
schtasks /Query /TN sol /V /FO LIST
```

计划任务**不收集 stdout**，所以配置里要让 sol 自己写日志：`logging.output: file`（否则审计记录无处可去）。

三份模板都从**同一份配置的 `run.args`** 生成——这正是把参数放进文件的价值：改一次，三个平台一致。

### 9.2 三平台的首次进入方式（都零参数）

- **Windows**：`irm <...>/install.ps1 -OutFile install.ps1; powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1`（或双击 release 里的 `install.cmd`）。**为什么不用 `irm … | iex`**：那样 `$PSCommandPath` 是空的，没有文件可以"以管理员身份重跑自己"，需要 UAC 的步骤必然失败。
- **Linux / macOS**：`curl -fsSL https://github.com/lidiaoo/sol/releases/latest/download/install.sh -o install.sh && sh install.sh`。**为什么不直接管进 `sh`**：管道执行时 stdin 是脚本自己，提问读不到答案（脚本会去 `/dev/tty` 问，问不到就什么都不做），而且没有可重跑的文件、无法自己升权。
- **macOS 额外一步（脚本自己处理）**：下载来的二进制带 quarantine 标记，脚本执行 `xattr -d com.apple.quarantine`，否则首次运行会被 Gatekeeper 弹"无法验证开发者"。
- **Windows**：`irm https://github.com/lidiaoo/sol/releases/latest/download/install.ps1 | iex`；或双击随附的 `install.cmd`（它内部按正确的执行策略调用 ps1，用户不需要记 `-ExecutionPolicy`）。
- 三平台一致的部分：**零参数**、生成的配置形态相同（只有 `run.args`）、同样两次问答、默认不需要特权（除非要装服务，或用 <1024 / 保留端口）。

## 10 验证与证据强度

| 交付物 | 证据强度 | 方式 |
| --- | --- | --- |
| 安装脚本的现状报告 / 文件清单 / `--dry-run` 预检 | **真机全量** | 真机冒烟（沿用现有 `sNN` 机制）：真跑脚本 + 真起服务 + 真卸载 |
| `scripts/install.sh` | **真机全量（已完成）** | `scratch/s35` 63 条 + `scratch/s55` 25 条断言 + **今天在一台真 Ubuntu（qemu 纯软件模拟的 VM，真 systemd 251、免密 sudo）上跑通的完整生命周期**：非 root 起 → 自己 sudo 升权 → 单元 active+enabled → 真在听 10/11/12（`ss -lunp` 指到 sol 的 pid）→ journalctl 里能看到启动日志（含 `settle window window=5s` 与三个 5s 冷却）→ 现状/编号菜单 → 换一个字节不同的二进制做升级（预检失败 → 停掉我们装的服务 → 预检通过 → sha256 与本地一致）→ 卸载（单元/二进制/台账清掉、服务 inactive、配置保留）：无二进制时下载回退 / 有二进制时就地使用 / 重跑无变化 / 换二进制（升与降）/ 只改 `run.args` / 异源提醒 / 无台账 / 卸载，并真发一个魔法包确认能起来；s55 另覆盖"预检绑不上就先停掉正在跑的我们自己的那份"、"卸载先停进程再删"与编号菜单的新界面 |
| `scripts/install.ps1`、macOS 路径、三平台产物模板（unit / plist / 计划任务） | **CI 证据**（否则只能标"仅语法级"） | `.github/workflows/install-smoke.yml` **已落地**：matrix ubuntu/macos/windows，在一次性 runner 上按用户的用法真装（真 root / 真管理员）→ 断言服务真的 active、真的在监听配置端口、台账 `"incomplete": false` → 再跑一遍验幂等 → 用重新构建的二进制做升级（"先停掉自己那份"的回归断言）→ 卸载并断言没留下我们建的东西。`scratch/s54` 26→44 条断言是它在 Linux 上用便携 pwsh + schtasks 桩做的前置核对；`scratch/s57` 20 条断言是它在本机（macOS）用便携 pwsh + 真 sol 二进制 + 函数桩（`Get-CimInstance`/`schtasks`）跑的**逻辑级**冒烟：真解析器查语法、装/重跑/卸载全走一遍，并断言"沙箱里没有 `schtasks /Create|/Delete`"；工作流本身的证据要等它在 runner 上跑一轮 |
| 包管理器清单 | **schema 级** | scoop/winget 的 JSON schema 校验；winget 在 CI 里装不了，只能标 |
| README 里的安装命令 | **真跑** | 沿用现有 readme 断言脚本（抽 README 片段真执行） |

报告与台账字段由单测断言，防止实现漂移（与 `schema/sol.schema.json` 的防漂移测试同一思路）。

### 10.1 三平台真机咬出来的三个坑（同一轮修掉，各配了回归断言）

1. **探针不能读"更高权限进程的元数据"**：现状是在**未升权**时打印的，而服务通常以 root / SYSTEM 跑。
   - Windows：`Get-Process` 的 `.Path` 读 SYSTEM 进程会失败（异常被吞）→ 看起来永远"没有在跑"。
     改走 CIM 的 `Win32_Process`（`ExecutablePath` / `CommandLine` 任何人都能读）。
   - Linux：`readlink /proc/<pid>/exe` 读 **root 进程**会被内核拒（读到空）→ 同样永远"没有在跑"。
     改读 `/proc/<pid>/cmdline` 的 argv[0]（谁都能读），`hidepid` 下再退回 exe 链接。
   - 断言：CI 三个平台各加一条"现状必须认出正在跑的那份"。
2. **升级前要停的是"服务单元"，不是进程**：systemd 的 `Restart=` / launchd 的 `KeepAlive` 会在毫秒级
   把进程拉回来，端口立刻又被占上 → 预检第二次照样绑不上，升级就卡死在"端口占用"上（真 Linux 上
   就是这么死的）。改成先软停单元（`systemctl stop` / `launchctl bootout`），预检仍不过就用
   `restore_service` 按原样起回去（开机自启不丢）。
3. **沙箱里不许碰服务管理器**：`SOL_INSTALL_ROOT` 的语义是"只用我给的根"，但 `install.ps1` 在沙箱里
   照样调了 `schtasks /Create /TN sol` —— 它会在**本机**任务管理器里建一个指向沙箱路径的任务。
   （`install.sh` 本来就有 `[ -n "$ROOT" ]` 护栏，ps1 漏了。）断言：s57 的"沙箱里没有 `/Create` `/Delete`"。

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
| 2 | `scripts/install.sh`（Linux/macOS）+ `install.ps1`/`install.cmd`（Windows）：认出现状 → 生成按平台的配置 → 展示并确认 → 执行（`--dry-run` 预检、服务定义、台账、报告） |
| 3 | 台账 / 报告契约落地（`install.json` schema + `install.log` 格式 + 校验脚本 + 单测） |
| 4 | `scripts/install.sh`：探测 → 生成配置（内容只有 `run.args`，不覆盖已有）→ 展示并确认 → 执行 / 卸载（预检 / 原子替换留 `sol.bak` / 回读运行版本）；**零命令行参数**，交互问答完成全部选择 + 真机冒烟 |
| 5 | `scripts/install.ps1`（同上一行：零参数、交互问答） |
| 6 | `.github/workflows/install-smoke.yml`（三平台 matrix） |
| 7 | scoop + winget 清单 |
| 8 | Hermes skill `sol-install` + `references/{linux,macos,windows}.md` |
| 9 | README 一行安装 + 修正 Windows 那段 `move sol.exe C:\Windows\System32` + 收口 |

## 13 未决项

1. **已定：服务生命周期不下沉到 Go 侧。** 三套服务管理器（systemd / launchd / 计划任务）留在安装脚本里，由真机冒烟覆盖；sol 本体不新增 `sol service` 之类子命令。
2. **是否给 `sol listen` 加一行"生效配置 + 来源"（`msg="configuration file" path=… source=…`）**：sol 本体的 2 行改动，能让 journald 直接看出它读的是哪份配置。按"不改 sol"的方向本次不做；重跑安装脚本可以在脚本侧报同样的信息。
3. "监听端口是否真的在听"能否做成免特权探测——目前只列配置端口 + 提示用 `--dry-run` 验证。