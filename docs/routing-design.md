# SoL 功能与配置设计草案

状态：设计讨论稿（定义模型、规则、配置与安全约束；不写实现）
范围：包内容匹配、端口路由、保留端口、动作模型（电源 / exec / HTTP / 远端命令）、多端口 / 多网卡、配置文件、HTTP、安全模型
进度跟踪：[TODO.md](../TODO.md)（P1-P4 可勾选落地清单）

## 目录

- 背景与定位
- 1 目标 · 2 术语 · 3 领域模型 · 4 动作模型 · 5 保留端口策略 · 6 匹配优先级 · 7 RoutingPolicy · 8 构造期校验
- 9 配置映射 · 10 CLI 兼容 · 11 改动面映射 · 12 已确认决策
- 13 配置格式示例集 · 14 使用案例 · 15 发送端配方
- 16 多端口与多实例 · 17 网卡发现与多网卡 · 18 HTTP 支持 · 19 落地阶段 · 20 安全模型总览 · 21 远端命令通道

## 背景与定位

同类项目对比（本设计的出发点）：

```
项目                     macOS Linux Windows  配置    触发            HTTP   睡眠  自定义命令
SR-G/sleep-on-lan        ❌    ✅    ✅      中等    反向WoL         ✅    ✅    ✅
jkmassel/shutdown-on-lan ✅    ✅    ✅      简单    TCP 字符串      ❌    ❌    ❌
bavix/sol（本仓库）       ✅    ✅    ✅      简单    反向WoL         ❌    ❌    ❌
```

- SR-G/sleep-on-lan：功能全（REST + 自定义命令 + sleep），配置重，不支持 macOS。定位是"常驻管理 agent"。
- jkmassel/shutdown-on-lan：TCP 发字符串触发，连 MAC 都不校验。最简，也最不安全（任何能连端口者即可关机）。
- bavix/sol：本仓库，有 MAC 校验 + 端口路由，只做电源动作。定位是"极简可靠"。

本设计把 sol 从"单端口 × 单网卡 × 单动作"扩成"多端口 × 多网卡 × 内容匹配 × 具名动作"，同时用**保留端口**把标准 WOL 端口变成安全边界；HTTP 与自定义命令作为后续阶段，且都强制带安全约束。

---

## 1. 目标

1. 支持"同一个端口 + 不同包内容 -> 不同动作"。
2. 支持"同一个普通包发到不同端口 -> 不同动作"（现状行为，保留）。
3. 默认（无内容）包走端口默认规则，改动最小、兼容手机 WOL 工具。
4. 标准 WOL 端口（7/9）作为保留端口：只允许纯魔法包，且只能映射到 `noop`（只记日志），形成安全边界。
5. 动作与触发解耦：动作是"具名 + 类型 + 参数"，由执行器注册表分发。

非目标（本期不做，但模型预留）：发送 WOL 唤醒别的机器（`wol.send`，§4 预留）、自定义命令执行（`exec`，设计见 §4.3）、HTTP（控制面 + 出站动作，§18）、远端命令通道（含裸 shell，§21）。

---

## 2. 术语

- Event：一次收到的数据包事件。
- Signature / Match：一组匹配条件（AND）。
- Rule：Match + ActionRef。
- ActionRef：具名动作的引用（字符串），不是枚举。
- ActionDef：具名动作的定义（类型 + 参数）。
- Executor：按动作类型执行的具体实现。
- Reserved port：保留端口（默认 7、9）。
- 远端命令：发送方携带"命令 id + 参数"，由白名单解析执行（§21）。
- 原始命令：发送方直接发任意 shell 行，默认关闭（§21.6）。
- Sequence：串行组合多个动作（预留）。

---

## 3. 领域模型（package `wol`，或新包 `routing`）

### 3.1 事件

    type Event struct {
        Payload []byte       // 原始包
        SrcIP   net.IP       // 来源地址
        SrcPort int
        DstPort int          // 命中的监听端口
    }

### 3.2 包解析结果

包布局（约定）：

    [0:102]   魔法包：6×0xFF + MAC×16
    [102:108] SecureOn 口令（仅当配置了 secureOn 才占位）
    [108:]    内容区（content），可空

    type ParsedPacket struct {
        HasMagic   bool
        MAC        net.HardwareAddr // 从魔法包解析出的目标 MAC
        Content    []byte           // 内容区
        HasContent bool
    }

    func ParsePacket(payload []byte, secureOn bool) ParsedPacket

要点：
- 魔法包必须位于偏移 0（精确解析），不再用 `bytes.Contains`，避免 payload 内嵌魔法序列误命中。
- 长度 < 102（或配置 secureOn 时 < 108）视为非法。
- 是否切分 SecureOn 由配置（是否设了 secureOn）决定，避免 108 字节歧义。

### 3.3 MAC 选择器（含每网卡）

    type MACKind int
    const (
        MACSelf      MACKind = iota // 属于"监听网卡 MAC 集合"（全部已配置网卡；默认）
        MACInterface                // 属于"指定网卡名"解析出的 MAC 集合（每网卡独立规则）
        MACExplicit                 // 等于指定 MAC
        MACAny                      // 不校验 MAC（预留给中继/唤醒场景）
    )

    type MACSelector struct {
        Kind   MACKind
        Addr   net.HardwareAddr // MACExplicit 时使用
        Ifaces []string         // MACInterface 时使用：网卡名列表
    }

### 3.4 内容匹配器

    type ContentKind int
    const (
        ContentAny    ContentKind = iota // 匹配任意内容（用于"不限内容"的规则；默认规则用 ContentNone）
        ContentNone                      // 必须是纯包（无内容区）
        ContentSuffix                    // 内容区以 Value 结尾
        ContentPrefix                    // 内容区 [Offset:Offset+len(Value)] == Value
    )

    type ContentMatcher struct {
        Kind   ContentKind
        Value  []byte
        Offset int
    }

配置层同时暴露 `value`（普通字符串）与 `value_hex`（十六进制），二者互斥，加载时统一解码进 `Value []byte`。

默认规则用 `ContentNone`（严格等于纯包），保证"带内容的包若没命中任何内容规则 = 不匹配（记日志）"是可预测的。

### 3.5 Match

    type Match struct {
        Ports     []int           // 空 = 任意端口（一般不用）
        MAC       MACSelector
        Content   ContentMatcher
        SrcCIDRs  []net.IPNet     // 空 = 不限来源；非空 = 白名单
    }

配置层 `match.interfaces: [eth0]` 是 `MAC: {Kind: MACInterface, Ifaces: [...]}` 的糖衣，与 `match.mac` 互斥；两者都不写 = `MACSelf`（全部已配置网卡）。

### 3.6 Rule

    type Rule struct {
        Match  Match
        Action ActionRef
    }

    type ActionRef string

---

## 4. 动作模型

### 4.1 定义

    type ActionType string
    const (
        ActionTypeNoop     ActionType = "noop"
        ActionTypeShutdown ActionType = "power.shutdown"
        ActionTypeReboot   ActionType = "power.reboot"
        ActionTypeSleep    ActionType = "power.sleep"   // 本期实现
        // 预留（P4）：exec / http / wol.send / sequence
    )

    type ActionDef struct {
        Name   ActionRef
        Type   ActionType
        Params map[string]any // 依类型解释
    }

内置动作：`noop`（收到即记日志：来源、端口、解析出的 MAC）。保留端口只允许 `noop`。

`power.sleep` 各平台命令：Linux `systemctl suspend`、macOS `pmset sleepnow`、Windows `rundll32 powrprof.dll,SetSuspendState 0,1,0`。仍由 `infra/system.PowerController` 按 `runtime.GOOS` 分发。

### 4.2 执行器

    type Executor interface {
        Type() ActionType
        Execute(ctx context.Context, def ActionDef, ev Event) error
    }

    type Registry struct { /* map[ActionType]Executor */ }
    func (r *Registry) Get(t ActionType) (Executor, bool)
    func (r *Registry) Dispatch(ctx context.Context, def ActionDef, ev Event) error

现状的 `wol.PowerController`（`Execute(ctx, Action) error`）改为实现 `Executor`，类型对应 `power.shutdown` / `power.reboot`。这一步是重构点：`ListenService` 从直接持有 PowerController 改为持有 `*Registry`。

### 4.3 自定义命令（exec 动作）

用途：规则命中后执行一条自定义命令（锁屏、通知、跑脚本、联动其他服务），即"接收端执行自定义指令"。

```yaml
actions:
  - name: lock-screen
    type: exec
    command: ["loginctl", "lock-session"]   # argv 数组，不走 shell
    timeout: 10s
    workdir: /opt/sol
    env:
      SOL_EVENT: "{{.Action}}"
    user: nobody                            # 可选：降权执行
    # shell: true                           # 逃生舱，默认 false，不推荐
```

参数：

- `command`：argv 数组（必填），**不走 shell** -> 免注入。
- `timeout`：默认 10s，超时强杀。
- `workdir` / `env` / `user` / `group`（降权）。
- `shell`：默认 `false`；仅当显式 `true` 才用 shell 解析（有注入风险）。

变量插值（白名单，不允许任意字符串拼接）：

```
{{.Action}}  {{.SrcIP}}  {{.DstPort}}  {{.Interface}}  {{.MAC}}  {{.Time}}
```

安全（exec 是最高危动作，护栏必须一起上）：

- 默认 argv 非 shell；禁止把规则里的原始字段直接拼进命令。
- 启动期静态校验：可执行文件存在、路径在允许目录（allowlist）内。
- 降权执行（`user`/`group`）。
- 超时强杀。
- cooldown / 速率限制（防止重放导致的反复执行）。
- 审计日志：来源 IP、命中规则、命令、退出码。
- `dry_run` 对 exec 同样生效。
- **不允许挂在保留端口 {7,9}**（§5）。

规则用法：配合内容匹配，同一端口不同包内容 -> 不同自定义命令（这正是"接收自定义命令"的核心场景）：

```yaml
rules:
  - match: { ports: [10], content: { kind: suffix, value: "lock" } }
    action: lock-screen
  - match: { ports: [10], content: { kind: suffix, value: "notify" } }
    action: notify
```

顺序组合（预留动作类型 `sequence`）：

```yaml
actions:
  - name: shutdown-then-notify
    type: sequence
    steps: [power.shutdown, notify]
```

阶段：`exec` / `sequence` 属 P4（见 [TODO.md](../TODO.md)），本期模型预留。

---

## 5. 保留端口策略

    const (
        PortDefault = 9
        PortEcho    = 7   // 另一个传统 WOL 端口
    )

    var DefaultReservedPorts = []int{PortEcho, PortDefault}

规则：
1. 保留端口上的 Rule 必须满足 `Match.Content.Kind == ContentNone`（纯包）。
2. 保留端口上的 Action 必须是 `noop`。
3. 违反 1 或 2 -> 构造策略时返回错误（fail fast），除非显式开启覆盖。
4. 覆盖开关：`allow_reserved_port_actions: true`（CLI `--allow-reserved-actions`），默认 false，开启后老行为可复刻，风险自负。
5. 远端命令端口（`remote_command_ports`）与裸 shell 端口（`raw_shell_ports`）不得落在保留端口内，否则 `ErrReservedPortAction`。

---

## 6. 匹配优先级与 Resolve 算法

节点内（同一端口）优先级，从高到低：

1. 带内容条件的规则（`ContentSuffix` / `ContentPrefix`）
2. 带来源白名单的规则（同等内容条件下更具体者优先）
3. 纯端口默认规则（`ContentNone`）
4. 无命中 -> 忽略（记日志）

实现方式：构造时把规则按"具体度"降序排好，运行期取第一个命中的。具体度打分（示例）：

    score(rule) = 0
        + 100 if Content.Kind in {Suffix, Prefix}
        +  10 if len(SrcCIDRs) > 0
        +  10 if MAC.Kind in {MACInterface, MACExplicit}
        +   1 if len(Ports) > 0

    // 排序：score 降序；同分视为歧义，构造期报错

Resolve（伪代码）：

    func (p *RoutingPolicy) Resolve(ev Event) (ActionRef, bool) {
        parsed := ParsePacket(ev.Payload, p.secureOn)
        if !parsed.HasMagic {
            return "", false
        }
        for _, r := range p.rules {          // 已按具体度排序
            if r.match(ev, parsed, p.targetMACs) {
                return r.Action, true
            }
        }
        return "", false
    }

    // selfMACs 是监听网卡的 MAC 集合
    func (r Rule) match(ev, parsed, selfMACs []net.HardwareAddr) bool {
        if len(r.Match.Ports) > 0 && !contains(r.Match.Ports, ev.DstPort) {
            return false
        }
        switch r.Match.MAC.Kind {
        case MACSelf:      if !containsMAC(selfMACs, parsed.MAC) { return false }
        case MACInterface: if !containsMAC(macsOf(r.Match.MAC.Ifaces), parsed.MAC) { return false }
        case MACExplicit:  if !bytes.Equal(parsed.MAC, r.Match.MAC.Addr) { return false }
        case MACAny:
        }
        // macsOf(names)：按网卡名从策略的接口表解析出 MAC 集合（构造期已解析好）
        if len(r.Match.SrcCIDRs) > 0 && !ipInAny(ev.SrcIP, r.Match.SrcCIDRs) {
            return false
        }
        switch r.Match.Content.Kind {
        case ContentAny:
        case ContentNone:   if parsed.HasContent { return false }
        case ContentSuffix: if !bytes.HasSuffix(parsed.Content, r.Match.Content.Value) { return false }
        case ContentPrefix: /* 长度校验 + bytes.Equal(片段) */
        }
        return true
    }

---

## 7. RoutingPolicy 结构（替换现状）

    type IfaceInfo struct {
        Name string
        MAC  net.HardwareAddr
        IPs  []net.IP
    }

    type RoutingPolicy struct {
        rules         []Rule          // 已排序
        targets       map[ActionRef]ActionDef
        reserved      map[int]bool
        secureOn      bool
        allowReserved bool
        ifaces        []IfaceInfo                  // 已解析的监听网卡（name/MAC/IP）
        ifaceMACs     map[string]net.HardwareAddr  // 名称 -> MAC（给 MACInterface）
        allMACs       []net.HardwareAddr           // 全集（给 MACSelf）
    }

    func NewRoutingPolicy(rules []Rule, actions []ActionDef,
        ifaces []IfaceInfo, opts PolicyOptions) (*RoutingPolicy, error)

    func (p *RoutingPolicy) Resolve(ev Event) (ActionDef, bool)
    func (p *RoutingPolicy) Ports() []int       // 需要建 socket 的端口
    func (p *RoutingPolicy) Rules() []Rule      // 供日志/status

与现状差异：
- `Match(payload, dstPort) (Action, bool)` -> `Resolve(Event) (ActionDef, bool)`
- `Rules()` 的 `Rule{Port, Action}` -> 新的 `Rule{Match, ActionRef}`
- `fallback` 字段不再需要（默认规则就是含 `ContentNone` 的普通规则）
- 新增 `actions` 与 `PolicyOptions`
- 新增 `IfaceInfo` 接口表：`MACSelf` 用 MAC 全集，`MACInterface` 按网卡名解析（每网卡独立规则）
- 规则来源：全局规则（`server.rules`，或顶层 `rules` 简写）+ `server.interfaces[].rules`（块内规则在加载期展开为带 interface 作用域的规则；作用域重叠且条件相同 -> `ErrRuleConflict`）

---

## 8. 构造期校验（fail fast 清单）

- 保留端口出现非 `noop` 动作或非 `ContentNone` 内容 -> `ErrReservedPortAction`（除非 allowReserved）
- 同端口出现两条 `ContentNone` 规则 -> `ErrDuplicateDefaultRule`
- 同一作用域内，同端口 + 同内容条件重复 -> `ErrAmbiguousRule`
- 引用不存在的动作名 -> `ErrUnknownActionRef`
- 规则引用了不在监听集合内的网卡名（`MACInterface`）-> `ErrUnknownInterface`
- 同名网卡在 `server.interfaces` 出现多次 -> `ErrDuplicateInterface`
- 块内 `match.interfaces` 不为空且不等于块名 -> `ErrInterfaceScopeConflict`
- 合并后两条规则作用域相交、且其余匹配条件（ports/content/mac/src_cidrs）相同 -> `ErrRuleConflict`（要求显式去重，不做静默覆盖）
- 内容长度超出读缓冲（`BufferSize`）-> `ErrContentTooLarge`
- 端口号非法（<1 或 >65535）-> 沿用现有解析错误

现有常量可直接复用：`HeaderSize=6`、`RepeatCount=16`、`MACSize=6`、`MagicByte=0xFF`、`BufferSize=2048`。`BuildMagicPacket` 继续用于"本机 MAC 期望值"的构造与将来发送侧。

---

## 9. 配置映射（YAML，示意）

    version: 1
    server:
      interface: eth0
    security:
      dry_run: false
      allow_reserved_port_actions: false
      reserved_ports: [7, 9]          # 可覆盖默认
    actions:
      - { name: noop,       type: noop }
      - { name: shutdown-now, type: power.shutdown }
      - { name: reboot-now,   type: power.reboot }
    rules:
      - match: { ports: [9], content: { kind: none } }   # 保留端口 -> 只记日志
        action: noop
      - match: { ports: [8] }                            # 纯包，换端口
        action: shutdown-now
      - match: { ports: [8], content: { kind: suffix, value: reboot } }
        action: reboot-now
      - match: { ports: [6], content: { kind: prefix, offset: 0, value_hex: "a1b2" },
                 src_cidrs: ["10.0.0.0/24"] }             # 来源白名单 + hex 内容
        action: shutdown-now

`config.Config` 从 `{InterfaceName, DryRun, Rules []wol.Rule}` 扩展为包含 `Actions`、`Security` 等段落；CLI flag 优先级高于配置文件。`server.interfaces` 项支持字符串或块（块可带专属 `rules`/`dry_run`/`secure_on`）；全局规则写 `server.rules`（顶层 `rules` 为其简写，二者同现报错），块内规则在加载期展开为带 interface 作用域的规则，详见第 17.9 节。

---

## 10. CLI 兼容

- 保留 `--iface`、`--port`、`--dry-run`。
- `--iface` 可重复（`--iface eth0 --iface wlan0`）；省略或为空 = 自动选择全部合格网卡（有线+无线）。
- 新增子命令 `sol ifaces`：列出本机所有网卡（NAME/TYPE/STATUS/MAC/IPV4/AUTO），支持 `--json`，供用户确认自动选择结果。
- `--port` 可重复，每个端口可带动作：`--port 10:sleep --port 11:shutdown --port 12:reboot`。
- `ParseAction` 需扩展：新增 `sleep`（当前仅 shutdown/reboot）。
- `--port` 不带动作时默认动作 = shutdown（保持兼容），可用 `--default-action` 覆盖。
- 纯 CLI 模式（不给 `--config`）完全支持"端口→动作"矩阵（含多端口、多实例）；内容匹配与 `src_cidrs` 只能通过配置文件。
- 新增 `--config`（指定配置文件路径）与 `--allow-reserved-actions` 覆盖开关。
- rules 合并语义：一旦命令行给了任何 `--port`，则命令行**完全接管 rules**（忽略配置文件里的 rules）；`actions`/`security`/`logging`/`server` 等仍取配置文件。未给 `--port` 时，rules 全部来自配置文件。校验在合并后的最终集合上执行。
- `--port 9` 语义变更：从 shutdown 变为 `noop`（记日志）。属 breaking change，启动时打 warning。
- 老用户迁移：把破坏性动作改用非保留端口（如 8），或开覆盖开关。
- README / systemd 示例同步改：默认示例从 `--port 9 --port 8:reboot` 改为非保留端口。

---

## 11. 与现有代码的改动面映射

    domain/wol（或新包 routing）
      + Event / ParsedPacket / ParsePacket
      + MACSelector / ContentMatcher / Match
      ~ Action -> ActionRef + ActionDef + ActionType
      ~ Rule 重构
      ~ RoutingPolicy 重构（Resolve / 校验 / 排序）
      + 保留端口常量与校验

    app/listen_service
      ~ handlePacket：policy.Resolve(ev) -> registry.Dispatch
      ~ 依赖由 PowerController 换成 Executor Registry

    infra/system
      ~ PowerController 实现 Executor 接口

    infra/network
      ~ udp_listener：无需改动（BufferSize=2048 足够容纳内容）
      + interface_resolver：新增 List()/ListAll()，枚举合格网卡（name/MAC/IPv4/类型/是否 auto 选中）
      ~ 多网卡落点：socket 仍绑 0.0.0.0，多网卡/auto 的实质是"MAC 集合参与匹配"（见第 17 节）

    config
      + 扩展 Config 结构 + YAML 加载（后续）
      ~ server.interface -> server.interfaces（列表，空 = auto）

    deps/builder
      + 构建动作注册表；NewRoutingPolicy 传入 actions/opts
      + 解析网卡集合（显式或 auto），得到 targetMACs

    cmd/listen
      + 解析新 flag（--iface 可重复、--allow-reserved-actions、--config、--default-action）
      + 把 --port 映射成新 Rule
      + 新增子命令 ifaces（列网卡）

    infra/exec        + exec 执行器（argv 非 shell、timeout、降权、审计），P4
    infra/remote      + 远端命令通道 + 裸 shell（§21，默认关闭），P4+
    infra/http        + 控制面 server（§18.6），P4
    infra/httpx       + 出站动作 client（§18.6），P4
    config            + security.allow_remote_commands / allow_raw_shell / http 段

---

## 12. 已确认决策（锁定）

1. 默认规则用 `ContentNone`（严格）：带内容但未命中任何内容规则的包 = 不匹配（记日志）。
2. 保留端口仅在用户声明的规则里才建监听；不声明则不监听（不会自动监听 9）。
3. 来源白名单 `SrcCIDRs` 本期即接入配置并生效。
4. 内容值同时支持普通字符串 `value` 与 `value_hex`，二者互斥。
5. 多端口 / 多网卡：一次可监听多个端口；网卡可显式列举，或省略 = auto 全部合格网卡（有线 + 无线）。
6. 每网卡独立规则：两种等价写法——扁平 `match.interfaces` 与块级 `server.interfaces[].rules`（§17.8-17.10）。
7. 全局规则位置：规范写法 `server.rules`，顶层 `rules` 为其简写；二者同现 -> 报错（§17.9）。
8. 规则冲突：两条规则作用域相交且其余条件相同 -> `ErrRuleConflict`，**不做静默覆盖**（§17.9）。
9. 远端原始命令（裸 shell）：默认关闭，需显式 `allow_raw_shell: true` + 强制认证（§21.6）。
10. 多实例：仅端口集合不重叠时可运行；同端口多实例不可行（§16.2）。

---

## 13. 配置格式示例集

每个示例都是自包含、可直接落盘的。内置动作名（`noop` / `power.shutdown` / `power.reboot` / `power.sleep`）可直接在 `rules[].action` 引用，无需 `actions` 段。顶层 `rules` 与 `server.rules` 等价（后者为规范写法）。

### 13.1 最小可用：单端口关机

```yaml
# 收到端口 8 的纯魔法包就关机
version: 1
server:
  interface: eth0
rules:
  - match: { ports: [8], content: { kind: none } }
    action: power.shutdown
```

### 13.2 一次性多端口：每端口一个动作（含睡眠）

```yaml
# 10 -> 睡眠，11 -> 关机，12 -> 重启
version: 1
server:
  interface: eth0
rules:
  - match: { ports: [10], content: { kind: none } }
    action: power.sleep
  - match: { ports: [11], content: { kind: none } }
    action: power.shutdown
  - match: { ports: [12], content: { kind: none } }
    action: power.reboot
```

命令行等价写法：`sol listen --iface eth0 --port 10:sleep --port 11:shutdown --port 12:reboot`

### 13.3 同端口内容分流：端口 8

```yaml
# 纯包 -> 关机；magic+"reboot" -> 重启；magic+"sleep" -> 睡眠
version: 1
server:
  interface: eth0
rules:
  - match: { ports: [8], content: { kind: none } }
    action: power.shutdown
  - match: { ports: [8], content: { kind: suffix, value: "reboot" } }
    action: power.reboot
  - match: { ports: [8], content: { kind: suffix, value: "sleep" } }
    action: power.sleep
```

### 13.4 来源白名单 + hex 内容

```yaml
# 只有指定网段、且内容以 a1b2 开头，才允许关机
version: 1
server:
  interface: eth0
rules:
  - match:
      ports: [6]
      content: { kind: prefix, offset: 0, value_hex: "a1b2" }
      src_cidrs: ["10.0.0.0/24", "192.168.1.0/24"]
    action: power.shutdown
```

### 13.5 保留端口：安全默认 与 复刻老行为

安全默认（端口 9 只记日志）：

```yaml
version: 1
server:
  interface: eth0
rules:
  - match: { ports: [9], content: { kind: none } }
    action: noop
```

复刻老行为（需显式覆盖，风险自负）：

```yaml
version: 1
server:
  interface: eth0
security:
  allow_reserved_port_actions: true
rules:
  - match: { ports: [9], content: { kind: none } }
    action: power.shutdown
```

### 13.6 全量字段参考（所有可配项都出现一遍）

```yaml
# /etc/sol/sol.yaml
version: 1

server:
  interfaces:                  # 网卡：字符串简写 或 块；省略/空 = auto 全部合格网卡
    - eth0
    - name: wlan0              # 块：可带专属 dry_run / secure_on / rules
      dry_run: true
      rules:
        - match: { ports: [12], content: { kind: none } }
          action: power.reboot
  rules:                       # 全局规则（顶层 rules 为其简写，二者同现报错）
    - match: { ports: [9], content: { kind: none } }        # 保留端口 -> 只记日志
      action: noop
    - match: { ports: [8], content: { kind: none } }        # 纯包
      action: power.shutdown
    - match: { ports: [8], content: { kind: suffix, value: "reboot" } }
      action: power.reboot
    - match: { ports: [6], content: { kind: prefix, offset: 0, value_hex: "a1b2" },
               src_cidrs: ["10.0.0.0/24"] }
      action: power.shutdown
  http:                        # 可选，控制面（P4，§18）
    enabled: false
    listen: 127.0.0.1:8080
    auth: { type: bearer, token_env: SOL_TOKEN }

logging:
  level: info                  # debug | info | warn | error，默认 info
  format: text                 # text | json，默认 text

security:
  dry_run: false               # true = 只记日志不执行
  reserved_ports: [7, 9]       # 保留端口集合，可覆盖默认
  allow_reserved_port_actions: false
  allow_remote_commands: false # 远端白名单命令（P4+，§21），默认关
  allow_raw_shell: false       # 远端裸 shell（§21.6），默认关；开启等价远程 shell
  remote_command_ports: [14]
  raw_shell_ports: [15]

# 内置动作 noop / power.shutdown / power.reboot / power.sleep 可直接引用；
# actions 段用于带参数或自定义动作。
actions:
  - name: lock-screen
    type: exec                 # P4
    command: [loginctl, lock-session]
    timeout: 10s

commands:                      # 远端可调用的白名单（P4+，§21）
  - id: lock
    type: exec
    command: [loginctl, lock-session]
```

### 13.7 多网卡 / 自动全部网卡

自动（省略 `interfaces`，默认有线+无线全部监听）：

```yaml
version: 1
server:
  interfaces: []          # 空/省略 = 自动选择全部合格网卡
rules:
  - match: { ports: [11], content: { kind: none } }
    action: power.shutdown
```

显式指定多网卡：

```yaml
version: 1
server:
  interfaces: [eth0, wlan0]
rules:
  - match: { ports: [11], content: { kind: none } }
    action: power.shutdown
```

### 13.8 每网卡独立规则

```yaml
# 有线口允许关机；无线口只记日志
version: 1
server:
  interfaces: [eth0, wlan0]
rules:
  - match: { interfaces: [eth0], ports: [8], content: { kind: none } }
    action: power.shutdown
  - match: { interfaces: [wlan0], ports: [8], content: { kind: none } }
    action: noop
```

（注意：不能再加一条不限网卡的 `ports:[8]` 兜底——它与上面两条作用域重叠、条件相同，会触发 `ErrRuleConflict`。要覆盖就显式列举网卡。）

### 13.9 server 多 interfaces 块（全局规则 + 每网卡专属）

```yaml
version: 1
server:
  # 全局规则：对本实例所有网卡生效（顶层 rules 亦可，二者等价）
  rules:
    - match: { ports: [11], content: { kind: none } }   # 所有网卡：端口11 -> 关机
      action: power.shutdown

  # 多网卡，各自 name + 可选专属规则
  interfaces:
    - name: eth0                     # 仅监听，用全局规则
    - name: eth1
      rules:
        - match: { ports: [10], content: { kind: none } }
          action: power.sleep
    - name: wlan0
      dry_run: true                  # 该网卡专属：命中只记日志
      rules:
        - match: { ports: [12], content: { kind: none } }
          action: power.reboot
```

（端口各不重叠，所以不冲突。若想让某网卡对端口 11 做不同动作，不能在全局再写端口 11，而要显式列举网卡、让作用域不重叠。）

### 13.10 自定义命令（exec）：同端口内容分流到不同命令

```yaml
version: 1
server:
  interfaces: [eth0]
actions:
  - name: lock-screen
    type: exec
    command: [loginctl, lock-session]
    timeout: 10s
  - name: run-backup
    type: exec
    command: [/usr/local/bin/backup.sh]
    timeout: 5m
    user: backup
rules:
  - match: { ports: [10], content: { kind: suffix, value: "lock" } }
    action: lock-screen
  - match: { ports: [10], content: { kind: suffix, value: "backup" } }
    action: run-backup
```

（阶段：`exec` 已在 P4 落地，见 §19.4；示例里的 `user: backup` 降权尚未实现，写上会在启动期报错。）

字段速查：

```
version        必填，schema 版本，当前 1
server.interfaces           可选；项可为字符串或块 { name, dry_run?, secure_on?, rules? }；省略/空 = auto 全部合格网卡；单网卡可 server.interface: eth0 简写
server.rules                可选，全局规则（对本实例所有网卡生效）；顶层 rules 为其简写，二者同现报错
logging.level               可选，默认 info
logging.format              可选，默认 text
security.dry_run            可选，默认 false
security.reserved_ports     可选，默认 [7, 9]
security.allow_reserved_port_actions  可选，默认 false
actions[].name              动作名，唯一
actions[].type              noop | power.shutdown | power.reboot | power.sleep | exec（http / sequence 见 P4）
actions[].command / timeout / workdir / env / shell   exec 用：argv 数组（非 shell）；shell: true 走 /bin/sh -c（逃生舱）；user/group 未实现
security.exec_allowlist     可选，限 exec 的绝对路径命令只能落在这些目录下（§19.4）
commands[].id / type / command / args        远端白名单命令（§21）
server.http.{enabled,listen,auth,tls}        控制面（§18 / §19.5）：auth.type = bearer|basic|mtls（默认 bearer，无 none）
server.http.auth.token_env / token_file      bearer 密钥来源（二选一；文件必须 600 权限）
server.http.auth.user + password_env / password_file   basic 认证
server.http.tls.{cert_file,key_file,client_ca_file}    TLS / mTLS（client_ca_file 用于 mtls）
security.allow_remote_commands / allow_raw_shell / remote_command_ports / raw_shell_ports   远端命令（§21）
rules[].match.ports         端口列表（留空 = 任意端口，一般不推荐）
rules[].match.content       { kind: any|none|suffix|prefix, value | value_hex, offset }
rules[].match.interfaces    网卡名列表；限定只对某些网卡生效（与 match.mac 互斥）
rules[].match.mac           self(默认) | { addr: "AA:BB:.." } | any
rules[].match.src_cidrs     CIDR 列表，空 = 不限来源
rules[].action              引用 actions[].name 或内置名
```

---

## 14. 使用案例

### 案例 1：最小用法 —— 端口 8 纯包关机
- 配置：见 §13.1（端口 8，纯包 -> 关机）
- 发送：`wakeonlan -p 8 <监听主机的MAC>`
- 效果：日志 `match port=8 action=power.shutdown`，然后关机

### 案例 2：同一端口，靠内容分流
- 配置：见 §13.3（端口 8：纯包关机 / +reboot 重启 / +sleep 睡眠）
- 发送纯包 -> 关机；发送 `magic + "reboot"` -> 重启
- 前提：发送端能构造带内容的包（标准 WOL 工具不行，见第 15 节）

### 案例 3：同一个普通包，发不同端口
- 配置：`ports:[8] -> shutdown-now`，`ports:[6] -> reboot-now`
- 发送端同一个 102 字节包，只改目标端口即得到不同动作（零内容依赖，老工具可用）

### 案例 4：来源白名单
- 配置：见 §13.4（`src_cidrs: ["10.0.0.0/24"]` + hex 内容）
- 只有该网段能触发 port 6 的关机，其他来源记 non-match 日志

### 案例 5：保留端口的安全边界
- 往 9 发纯包 -> 只记日志，绝不执行动作（防止误发/恶意唤醒包关机）

### 案例 6：复刻老行为（迁移路径）
- 方式 A（配置文件）：`allow_reserved_port_actions: true` + 加一条 `ports:[9] -> shutdown-now`
- 方式 B（CLI）：`sol listen --iface eth0 --port 9 --allow-reserved-actions`
- 不迁移的老用户需把破坏性动作改到非保留端口（如 8）

### 案例 7：dry-run 验证
- 配置文件 `security.dry_run: true` 或 CLI `--dry-run`
- 所有命中只打印日志、不执行，用来上线前验证规则

### 案例 8：systemd 常驻
```ini
[Service]
ExecStart=/usr/local/bin/sol listen --config /etc/sol/sol.yaml
Restart=always
User=root
```

### 案例 9：配置优先级
- `默认值 < 配置文件 < 环境变量 < 命令行 flag`
- rules 合并：命令行只要给了任何 `--port`，就完全接管 rules；否则 rules 全部来自配置文件（详见第 10 节）
- 例：配置文件写 `ports:[8]`，运行时 `sol listen --config ... --port 6`，则规则集只按 `--port 6` 生效

### 案例 10：自定义命令（exec）
- 配置：`action: run-backup`（`type: exec`，`command: [/usr/local/bin/backup.sh]`，`user: backup`）
- 发送：`magic + b"backup"` 到端口 10
- 效果：命中后以降权用户执行脚本，记录退出码；dry-run 时只打印不执行
- 注意：exec 属 P4（见 TODO.md）；不允许挂在保留端口

---

## 15. 发送端配方

要点：触发包必须是"合法魔法包（含 MAC）+ 可选内容区"。默认 `mac: self`，即包内 MAC 必须是**监听主机的 MAC**。

纯包（102 字节），大多数标准工具可用：

```bash
# Perl 版 wakeonlan 支持指定端口
wakeonlan -p 8 AA:BB:CC:DD:EE:FF
```

带内容 / 任意端口（标准工具做不到，用脚本）：

```python
# python3 send.py
import socket

mac   = bytes.fromhex("AABBCCDDEEFF")     # 监听主机的 MAC
magic = b"\xff" * 6 + mac * 16            # 102 字节魔法包

payload = magic                            # 纯包
# payload = magic + b"reboot"              # 后缀内容 -> 命中案例2
# payload = magic + bytes.fromhex("a1b2")  # 内容前缀 hex -> 命中案例4

s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
s.sendto(payload, ("192.168.1.255", 8))    # 只改端口即可切换动作
```

注意事项：
- 目标地址用子网广播（`192.168.1.255`）或 `255.255.255.255`；监听端绑 0.0.0.0 均可收到。
- 内容区上限受读缓冲约束（`BufferSize=2048` 减去魔法包部分），足够常规 token 使用。
- 内容 token 本期是明文、无认证。破坏性动作（关机/重启）建议**同时**配 `src_cidrs` 白名单；更强的包级认证（HMAC）列为后续项。
- 路由器固件 / 手机 WOL App 一般只能发 102 字节纯包，因此它们只能命中 `content: { kind: none }` 的默认规则。

---

## 16. 多端口与多实例

### 16.1 单实例多端口（推荐）

一个进程监听多个端口，每端口一个动作，互不干扰（现状已支持此形态）：

```bash
sol listen --iface eth0 --port 10:sleep --port 11:shutdown --port 12:reboot
```

对应配置：

```yaml
rules:
  - match: { ports: [10], content: { kind: none } }
    action: power.sleep
  - match: { ports: [11], content: { kind: none } }
    action: power.shutdown
  - match: { ports: [12], content: { kind: none } }
    action: power.reboot
```

- `policy.Ports()` 返回全部端口，`createListeners` 逐端口建 socket。
- 每个端口可再叠加内容规则（同端口内容分流），两种维度正交叠加。

### 16.2 多实例（端口集合必须互不重叠）

```bash
# 进程 A
sol listen --iface eth0 --port 10:sleep
# 进程 B
sol listen --iface eth1 --port 11:shutdown
```

为什么同端口多实例不行（务必知道）：
- 当前 listener 绑 `0.0.0.0`（`udp_listener.go` 用 `IPv4zero`），两个实例绑同一端口会 `EADDRINUSE`。
- 用 `SO_REUSEPORT` 共存，在**广播**场景不可靠：内核只把每个广播包投给其中一个 socket，另一个实例收不到。
- 把 socket 绑到具体网卡 IP，又收不到 WOL 广播（广播只投给绑 `INADDR_ANY` 的 socket）。
- 结论：**同端口多实例本质上做不到**。要区分就用"单实例多端口"，或给各实例分配不同端口（可用不同 `--iface`）。

### 16.3 多实例下的保留端口

保留端口规则在每个实例内独立生效：某实例监听 9 -> 9 上只允许 `noop`。

### 16.4 纯 CLI 能力边界

```
完全支持（无需配置文件）：
  - 端口 -> 动作矩阵
  - 单实例多端口 / 多实例（端口不重叠）
  - dry-run、保留端口 warning 与覆盖开关

必须配置文件：
  - 内容匹配（suffix / prefix / value_hex）
  - src_cidrs 来源白名单
  - 带参数或自定义的动作
```

即"10 睡眠 / 11 关机 / 12 重启"这类完全可以命令行运行，无需配置文件。

---

## 17. 网卡发现与多网卡监听

### 17.1 三种模式

```
指定单网卡：  --iface eth0
指定多网卡：  --iface eth0 --iface wlan0
自动全部：    省略 --iface（默认）-> 全部合格网卡（有线 + 无线）
```

关键认知：socket 本来就绑 `0.0.0.0`，物理上已能收到所有网卡的包。"监听哪个网卡"实质是两件事：

1. 哪些网卡的 **MAC 参与匹配**（`MACSelf` 从单值扩成集合）；
2. 启动日志里显示监听的是哪些网卡。

所以多网卡/自动全部的落地就是：把匹配用的 MAC 从"一个"变成"一个集合"，无需改 socket。

### 17.2 合格网卡（auto 的选择规则）

- `Up`
- 非 loopback
- 有硬件地址（MAC）
- （尽力）排除虚拟/隧道：名字匹配 `docker*` / `veth*` / `virbr*` / `br-*` / `tun*` / `tap*` / `vmnet*` 等
- 有线 + 无线都算（本设计不区分介质，只做分类显示）

若 auto 选到 0 个合格网卡 -> 启动报错。

### 17.3 配置与字段

- `server.interfaces: [eth0, wlan0]`：显式列表
- `server.interface: eth0`：单网卡简写（等价 `interfaces: [eth0]`）
- 列表项可简写为字符串，也可为块 `{ name, dry_run?, secure_on?, rules? }`（每网卡专属配置，见 17.9）
- 省略 / 空列表：auto 全部合格网卡
- 两者同时出现 -> 配置报错

### 17.4 枚举网卡的命令（OS 层，供文档/排障）

```bash
# Linux
ip -br link                       # 简洁列出所有网卡 + 状态 + MAC
ip -o link show                   # 详细
ls /sys/class/net                 # 直接列目录
test -d /sys/class/net/wlan0/wireless && echo wireless   # 判定无线

# macOS
networksetup -listallhardwareports   # 列出 Ethernet / Wi-Fi 等硬件端口
ifconfig -a

# Windows
netsh interface show interface
getmac /v
# PowerShell
Get-NetAdapter
```

程序内部（跨平台）用 Go 标准库 `net.Interfaces()`，返回 `name / flags / hardwareAddr / mtu`。无线判定无标准 API，按平台尽力而为：Linux 用 sysfs（`/sys/class/net/<name>/wireless`）、macOS 用 `networksetup`、Windows 用 WMI/WLAN API。

### 17.5 新增子命令：`sol ifaces`

让用户"提前看到程序枚举到哪些网卡"，再决定是否显式指定。

```
$ sol ifaces
NAME     TYPE   STATUS  MAC                IPV4           AUTO
eth0     wired  up      aa:bb:cc:dd:ee:ff  192.168.1.5    yes
wlan0    wifi   up      aa:bb:cc:dd:ee:10  192.168.1.6    yes
docker0  virt   up      02:42:ac:11:00:01  172.17.0.1     no
lo       loop   up      -                  127.0.0.1      no
```

- `AUTO` 列 = 该网卡是否会被自动模式选中。
- `--json` 输出，方便脚本/集成。
- `TYPE`：wired / wifi / virt / loop（尽力判定）。

### 17.6 启动日志

给出监听网卡集合，便于用户确认 auto 选对了：

```
Using interfaces: eth0(aa:bb:cc:dd:ee:ff), wlan0(aa:bb:cc:dd:ee:10)
```

### 17.7 与匹配模型的衔接

- `RoutingPolicy.targetMACs []net.HardwareAddr` 由所选网卡集合填充。
- `MACSelf` 命中条件：`parsed.MAC ∈ targetMACs`。
- 一台机器同时插网线又连 Wi-Fi 时，两个 MAC 任一命中即可触发（这正是"有线+无线都监听"的效果）。

### 17.8 每网卡独立规则（不再统一）

默认（不写网卡条件）时 `MACSelf` = 命中全部已配置网卡，这就是"统一"的来源。要按网卡区分，用 `match.interfaces`：

```yaml
# eth0（有线，可信内网）：允许关机
# wlan0（无线，不可信）：只记日志
version: 1
server:
  interfaces: [eth0, wlan0]
rules:
  - match: { interfaces: [eth0], ports: [8], content: { kind: none } }
    action: power.shutdown
  - match: { interfaces: [wlan0], ports: [8], content: { kind: none } }
    action: noop
```

（不要加不限网卡的兜底——会与上面两条作用域重叠，触发 `ErrRuleConflict`。）

规则：

- `match.interfaces` 与 `match.mac` 互斥；两者都不写 = 全部网卡（`MACSelf`）。
- 配置里出现的网卡名必须在监听集合（`server.interfaces` 或 auto 结果）内，否则构造期 `ErrUnknownInterface`。
- 同一端口按网卡区分动作时，必须让作用域不重叠：每条规则都显式限定网卡（穷举），或让全局规则避开与网卡规则同端口/同内容。作用域重叠且条件相同 -> `ErrRuleConflict`（不做静默覆盖，见第 6/8 节）。
- 物理含义：魔法包里的目标 MAC 决定"发给哪张网卡"，据此分流。

### 17.9 server 多 interfaces 块（全局规则 + 每网卡专属）

`server` 下可以有一份**全局规则**（`server.rules`，或顶层 `rules` 简写），以及一组 `interfaces` 块，每块携带该网卡的专属配置（`rules` / `dry_run` / `secure_on`）：

```yaml
version: 1

server:
  # 全局规则：对本实例所有网卡生效
  rules:
    - match: { ports: [11], content: { kind: none } }
      action: power.shutdown

  interfaces:
    - eth0                       # 简写：仅声明监听，用全局规则
    - name: eth1                 # 块：监听 + 专属规则
      rules:
        - match: { ports: [10], content: { kind: none } }
          action: power.sleep
    - name: wlan0
      dry_run: true              # 该网卡专属：命中只记日志
      rules:
        - match: { ports: [12], content: { kind: none } }
          action: power.reboot
```

语义：

- 监听集合 = 所有条目 name 的并集；整个 `interfaces` 省略/空 = auto 全部合格网卡。
- 全局规则放 `server.rules`；顶层 `rules` 是它的简写，二者同现 -> 报错。
- 最终规则表 = 全局规则（作用域 = 本实例全部网卡）∪ 各块规则（作用域 = 该网卡，隐式 `interfaces: [块名]`）。
- 冲突：两条规则**作用域相交且其余匹配条件（ports/content/mac/src_cidrs）相同** -> 启动报错 `ErrRuleConflict`，要求显式去重。**不做静默覆盖**（不存在"块内覆盖全局"）。
- 想按网卡区分同端口/同内容的动作：必须让作用域不重叠，即**显式列举网卡**，而不是写全局兜底。
- 块内 `match.interfaces` 必须为空或等于块名，否则报错。
- 同名网卡出现多次 -> 报错。
- 块级可覆盖项：`dry_run`、`secure_on`；其余配置仍取全局。

### 17.10 两种等价写法

同一件事（按网卡区分）有两种表达，最终归约成同一种内部表示（规则带 interface 作用域）：

```yaml
# 写法 A：扁平的每规则维度
rules:
  - match: { interfaces: [eth0], ports: [8], content: { kind: none } }
    action: power.shutdown
```

```yaml
# 写法 B：块级维度（server.interfaces）
server:
  interfaces:
    - name: eth0
      rules:
        - match: { ports: [8], content: { kind: none } }
          action: power.shutdown
```

- A 适合"少量规则、跨网卡混写"。
- B 适合"每张网卡一套完整策略"，配置更清晰、可分别覆盖 dry_run 等。
- 两者可混用；最终都编译进同一张规则表。

---

## 18. HTTP 支持（控制面 + 出站动作）

"HTTP" 在这里是两件不同的事，别混：

- A. 控制面（入站，sol 当服务端）：起一个 HTTP server，对外提供状态查询、手动触发动作、热重载。对应 SR-G/sleep-on-lan 那套 REST API。
- B. 出站动作（sol 当客户端）：规则命中后，动作类型 `http` 向某个 URL 发请求（webhook 通知/联动）。

阶段：A（控制面）已在 P4 部分落地（见 §19.5）：`/healthz`、`/v1/status`、`/v1/rules`、`/v1/interfaces`、`POST /v1/actions/{name}`、`/metrics` 已实现，`POST /v1/reload` 返回 501（热重载未做）；`/v1/commands/{id}`、`/v1/exec` 随远端命令通道留到后续。B（出站动作）仍未做。

### 18.1 A. 控制面（入站 REST API）

端点：

```
GET  /healthz                存活探针
GET  /v1/status              运行时长、选中网卡、已加载规则、最近事件
GET  /v1/rules               当前规则（脱敏）
GET  /v1/interfaces          枚举网卡（等价 sol ifaces）
POST /v1/actions/{name}      手动触发某个具名动作
POST /v1/reload              热重载配置（等价 SIGHUP）
GET  /metrics                Prometheus 指标
POST /v1/commands/{id}       远端白名单命令（§21，需 allow_remote_commands）
POST /v1/exec                远端原始命令（§21.6，需 allow_raw_shell，默认关闭）
```

安全：

- 默认只绑 `127.0.0.1:8080`；要对外必须显式改，并强制认证。
- 认证三选一：`bearer`（默认）、`basic`、`mTLS`。
- TLS 可选（局域网强信任可用 mTLS）。
- 触发接口能调破坏性/exec 动作，等于敏感面 -> 必须认证 + 审计。

配置：

```yaml
server:
  http:
    enabled: true
    listen: 127.0.0.1:8080
    auth:
      type: bearer
      token_env: SOL_TOKEN      # 或 token_file: /etc/sol/token
    tls:                        # 可选
      cert_file: /etc/sol/tls.crt
      key_file:  /etc/sol/tls.key
```

用法：

```bash
curl -H "Authorization: Bearer ${SOL_TOKEN}" http://127.0.0.1:8080/v1/status
curl -X POST -H "Authorization: Bearer ${SOL_TOKEN}" \
     http://127.0.0.1:8080/v1/actions/power.shutdown
```

### 18.2 B. 出站动作（webhook 联动）

```yaml
actions:
  - name: notify
    type: http
    method: POST
    url: "https://hooks.example.com/sol"
    headers:
      Content-Type: application/json
      Authorization: "Bearer ${HOOK_TOKEN}"   # 支持环境变量插值
    body: '{"event":"{{.Action}}","src":"{{.SrcIP}}","port":{{.DstPort}}}'
    timeout: 5s
    retries: 2
```

参数：`method / url / headers / body(模板) / timeout / retries`。

安全：出站可被用来打内网（SSRF），可选 `url_allowlist`；默认校验 TLS；日志不打印 header 里的密钥。

### 18.3 两者关系

- 控制面是"入站"，让外部通过 HTTP 触发/查询；HTTP 动作是"出站"，让规则命中后回调外部。
- 二者共用同一套动作/规则模型：控制面的"手动触发"就是调 `registry.Dispatch(action, ev)`。
- 可组合成 sequence：规则命中 -> 先 `shutdown` 再 `notify`（webhook 通知）。

### 18.4 完整配置示例

```yaml
version: 1
server:
  interfaces: [eth0]
  http:
    enabled: true
    listen: 127.0.0.1:8080
    auth: { type: bearer, token_env: SOL_TOKEN }
actions:
  - name: notify
    type: http
    method: POST
    url: "https://hooks.example.com/sol"
    headers: { "Content-Type": "application/json" }
    body: '{"event":"{{.Action}}","src":"{{.SrcIP}}","port":{{.DstPort}}}'
    timeout: 5s
rules:
  - match: { ports: [11], content: { kind: none } }
    action: notify            # 收到关机包 -> 只发通知，不真关机
```

### 18.5 安全模型（HTTP 相关）

```
控制面  默认本地 + 强制认证 + 可选 TLS + 审计；触发接口视为敏感
出站动作 出站方向，注意 SSRF；可选 url_allowlist；超时 + 重试；不记录密钥
共同    审计日志：谁（来源 IP / token 身份）、何时、命中/触发了什么、结果
```

### 18.6 改动面

```
infra/http        + 控制面 server（net/http 标准库，无强依赖）
infra/httpx       + 出站动作 client
domain/wol        + ActionTypeHTTP 的 params 定义
deps/builder      + 注册 http 执行器；按配置决定是否起 server
config            + server.http 段 + http 动作参数 + token 来源
cmd/listen        + 触发 HTTP server 启动（或仅配置文件开关）
```

---

## 19. 落地阶段（路线图）

每个阶段可独立发布、独立回退。可勾选的落地清单见 [TODO.md](../TODO.md)。

```
P1  domain 模型与匹配
      + Event / ParsedPacket / ParsePacket（精确解析 102/108 + 内容区）
      + Match / ContentMatcher / MACSelector / Rule
      ~ RoutingPolicy.Resolve + 构造期校验 + 保留端口约束
      + 单元测试；行为上仅新增"内容匹配 + 保留端口"两条规则

P2  动作模型与 CLI
      + Executor 接口 + Registry；内置 noop / power.shutdown / power.reboot / power.sleep
      ~ PowerController 改造成 Executor
      + CLI：--port 可重复带动作、--iface 可重复/省略即 auto、--allow-reserved-actions、
             --default-action、--config、sol ifaces
      ！--port 9 行为改为 noop（breaking，启动打 warning）

P3  配置文件
      + YAML 加载 + 严格解码（未知字段报错）+ 优先级（默认<文件<env<flag）
      + actions 段、src_cidrs 接线
      + 热重载（可选）

P4  自定义命令 + HTTP
      + exec 动作（argv 非 shell、超时、降权、cooldown、审计）
      + HTTP 控制面（认证 + 默认本地）
      + HTTP 出站动作（webhook）
      + 远端命令通道（白名单 id + 参数校验 + HMAC，§21）
      + 原始命令（裸 shell，默认关闭，§21.6）
      + 预留 wol.send
```

### 19.1 P1 已落地（实现对照）

代码位置：`internal/domain/wol/packet.go`（解析）、`match.go`（ContentMatcher / MACSelector / Match / Rule）、`policy.go`（RoutingPolicy / PolicyOptions / Event / IfaceInfo）、`action.go`（Action 扩展 `noop`）；测试 `internal/domain/wol/{wol,packet,match,policy}_test.go`。

实现要点（与本文的对齐 / 差异）：

- `Rule{Match, Action}`：P1 的 `Action` 即设计中的 ActionRef（字符串类型），内置值 `noop` / `power.shutdown` / `power.reboot`；`ParseAction` 兼容 `shutdown`/`s`、`reboot`/`r`、`noop`/`n` 与 `power.*` 全名。P2 引入 Registry 后再细化。
- `Match` 无独立 `interfaces` 字段：`match.interfaces` 糖衣在编译期等价于 `MACSelector{Kind: interface, Ifaces: [...]}`；与显式 `mac` 同设 -> `ErrMACConflict`。
- `ParsePacket` 严格：魔法包必须起始于偏移 0（不再 `bytes.Contains`）；`secureOn` 非空时要求 102..108 匹配，内容区从 108 起，否则从 102 起。
- `RoutingPolicy.Resolve(Event)` 取分数最高且命中的规则；分数 `content(100) > src_cidr(10) > mac interface/explicit(10) > port(1)`。
- 构造期校验：端口范围、动作已知、内容 token（`value`/`value_hex` 互斥、≤ `MaxContentLen`=64）、CIDR、MAC、保留端口、重复 / 歧义规则。
- 保留端口默认 `DefaultReservedPorts()` = {7,9}；`PolicyOptions.ReservedPorts` 可覆盖，`AllowReserved` 复刻旧行为。
- 冲突判定（同作用域）：`ErrDuplicatePort`（同端口同内容条件）、`ErrAmbiguousRule`（同分且内容可能同时命中，如 prefix vs suffix）。`src_cidrs` 需完全一致才视为同作用域（保守）。
- 后续阶段已补齐：`ErrRuleConflict` / `ErrInterfaceScopeConflict` 与 `SecureOn` 的配置接线都在 P3 落地（见 §19.3）。

CLI 侧的 P1 配套：`sol listen --port 9` 现在把动作降级为 `noop` 并打印 WARNING（`cmd/listen.go`），其余端口行为不变。

### 19.2 P2 已落地（实现对照）

代码位置：`internal/domain/wol/action.go`（ActionType / ActionDef / Executor / Registry / `power.sleep`）、`internal/infra/system/{power_controller,noop_executor}.go`（Executor 实现）、`internal/infra/network/{ifaces,interface_resolver}.go`（`List` / `Select` / 合格判定）、`internal/app/listen_service.go`（Resolve -> Dispatch、启动打印网卡集合）、`internal/deps/builder.go`（装配 registry + 网卡选择）、`cmd/{listen,ifaces}.go`。

实现要点（与本文的对齐 / 差异）：

- `Executor` 接口为 `Execute(ctx, ActionDef, Event) error`；**去掉了设计里的 `Type() ActionType`**——一个执行器可服务多个类型，注册改为 `Registry.Register(executor, types...)`（`PowerController` 一次注册 shutdown/reboot/sleep，`NoopExecutor` 注册 noop）。
- `ActionDef{Name, Type, Params}`；`BuiltinActions()` 四个内置动作的名字与类型名相同（`noop` / `power.shutdown` / `power.reboot` / `power.sleep`）。
- 分发入口有两个：`Dispatch(ctx, name, ev)`（服务侧：按名字取定义 -> 按类型找执行器 -> 执行）与 `Registry.Actions()`（交给策略做构造期校验）。`RoutingPolicy.Resolve` 仍返回**动作名**而非 `ActionDef`，与 §4.2 的示意略有差异（P1 测试与 P3 命名动作都需要名字这一层）。
- `PolicyOptions.Actions` 是策略的动作白名单来源，默认 `BuiltinActions()`；P3 的 `actions` 段届时注入这里，`ErrUnknownActionRef` 天然覆盖"引用了未定义动作"。
- 网卡：`network.List()` 返回全部网卡（含 `Up/Loopback/Virtual/Eligible` 标志），`network.Select(names)` 显式名字优先、空则取全部合格网卡；合格 = `Up` + 非 loopback + 有 MAC + 名字不以 `docker*/veth*/virbr*/br-*/vnet/vmnet/tun/tap/tailscale/wg/zt/podman/cni/flannel` 开头。无合格网卡 -> `ErrNoEligibleInterface`；显式名字未知 / 重复 / 无 MAC -> `ErrUnknownInterface` / `ErrDuplicateInterface` / `ErrNoMACAddress`。
- `IfaceInfo` 扩为 `{Name, MAC, IPs, Up, Loopback, Virtual, Eligible}` + `IPv4()`；`MACSelf` 即"全部选中网卡的 MAC 集合"，这就是"多网卡监听"的落地形式（socket 仍绑 `0.0.0.0`）。
- CLI：`--iface` 可重复、省略即 auto；`--port` 可重复且支持 `port:action`；新增 `--default-action`（默认 `shutdown`）、`--allow-reserved-actions`；新增子命令 `sol ifaces [--json]`（NAME/TYPE/STATUS/MAC/IPV4/AUTO）。
- **`--config` 未随 P2 落地**：P2 不暴露这个 flag，避免出现"加了但用不了"的假接口，随 P3 的加载器一起加。
- 保留端口：`--port 9`（无动作或显式非 noop 动作）在 CLI 层降级为 `noop` 并打 WARNING，除非 `--allow-reserved-actions`；策略层仍对"保留端口 + 内容规则 / 非 noop"报 `ErrReservedPortAction`（fail fast）。
- 冒烟（真机 `enp6s0` + `enp9s0f3u1`）：`sol ifaces` 把 `docker0/br-*/veth*` 标为 virtual 且 `AUTO=no`、`wlp5s0`（down）`AUTO=no`；auto 模式启动打印两张网卡与规则列表；`--port 10010:sleep --port 10011:shutdown` 分别命中 sleep/shutdown；显式 `--iface enp9s0f3u1` 时 `enp6s0` 的魔法包被判为不匹配。

### 19.3 P3 已落地部分（配置文件）

代码位置：`internal/config/{config,schema,load}.go`（YAML 模式、严格解码、发现顺序、`${VAR}` 插值、环境变量覆盖、转换为 `Config`）、`internal/domain/wol/{match,policy}.go`（`Rule.DryRun` + `Decision`）、`cmd/listen.go`（`--config` + 合并优先级）、`internal/deps/builder.go`（把 `ReservedPorts` / `SecureOn` / `Actions` 交给策略）。测试 `internal/config/load_internal_test.go`（覆盖率约 91%）。

已实现：

- YAML（`gopkg.in/yaml.v3`）+ `KnownFields(true)` **严格解码**：未知字段直接报错，不静默忽略。为此在 `.golangci.yml` 的 depguard 允许列表加了 `gopkg.in`（原本只允许 std / github.com / golang.org / google.golang.org）。
- `version: 1` 校验，其它值 -> `ErrUnsupportedVersion`。
- 发现顺序：`--config` > `$SOL_CONFIG` > `/etc/sol/sol.yaml` > `~/.config/sol/sol.yaml`；都不存在则用内置默认（此时无规则，CLI 报 `no rules configured`）。
- `${VAR}` / `$VAR` 插值：引用了未设置的变量直接报错（避免凭据静默变空）。
- 环境变量覆盖（在文件之后、CLI flag 之前）：`SOL_DRY_RUN`、`SOL_ALLOW_RESERVED_PORT_ACTIONS`、`SOL_INTERFACES`（逗号分隔）、`SOL_SECURE_ON`、`SOL_LOG_LEVEL`、`SOL_LOG_FORMAT`。
- 规则来源：`server.rules`（规范写法）与顶层 `rules` 等价，二者同现 -> `ErrRulesConflict`。
- `server.interfaces`：字符串简写或块 `{name, dry_run?, rules?}`；块内规则在加载期展开为 `MACSelector{Kind: interface, Ifaces: [块名]}`（即 §17.8/17.9 的"等价写法"），块级 `dry_run` 落成 `Rule.DryRun`——命中仍打印 action，但打 `DRY-RUN` 不执行。
- `match` 全字段接线：`ports` / `interfaces` / `mac`（标量 `self|any|<MAC>` 或块 `{kind, address, interfaces}`）/ `content`（`kind`、`value`、`value_hex`、`offset`）/ `src_cidrs`。
- `security`：`dry_run`、`reserved_ports`、`allow_reserved_port_actions`、`secure_on`（全局；长度必须 6 字节，否则 `ErrSecureOnLength`——旧实现里长度不对会**静默永不匹配**，现在 fail fast）。
- `actions` 段：命名动作 `{name, type}`，type 暂限四种内置类型；定义会注册进 Registry，`rules[].action` 可直接引用；重名 -> `ErrDuplicateAction`（内置名不可重定义）。
- `logging` 段：`level`（debug|info|warn|error，默认 info）+ `format`（text|json，默认 text）。程序日志已从 stdlib `log` 迁到 `log/slog` 结构化日志（`internal/infra/logging.Setup`，启动时装载、`slog.SetDefault`），非法 level/format fail fast。
- 语义校验仍在 policy 构造期 fail fast：保留端口非 noop / 带内容、未知动作、端口范围、CIDR、重复与歧义规则等。
- 冲突检测：同作用域同分且可能同时命中 -> `ErrDuplicatePort` / `ErrAmbiguousRule`（P1 已有）；**跨作用域**相交（per-NIC 规则 + 不限网卡的兜底、`mac: any` + `mac: self`）且 ports/content/src_cidrs 完全相同 -> `ErrRuleConflict`，错误信息带上两个 scopeKey，拒绝静默覆盖；块内规则把自己的 `match.interfaces` 指到别的网卡 -> `ErrInterfaceScopeConflict`。
- 冲突冒烟：§13.8 的"每网卡规则 + 兜底一条"确实报 `ErrRuleConflict`；去掉兜底即通过；`enp6s0` 的块规则不能写成 `match.interfaces: [enp9s0f3u1]`；§13.9 的"全局规则 + 多个 interfaces 块（端口不重叠）"可用，且 enp6s0 的魔法包在只属于 enp9s0f3u1 的端口上判为不匹配。
- 冒烟：文件版"全局 noop + 每网卡块 dry_run"生效（块规则命中打 `DRY-RUN`，全局规则照常执行）；出现 `--port` 时完全接管 rules；未设置变量 / 未知字段 / rules 双写 / 无规则 / secure_on 长度 / 保留端口违规 全部给出明确错误。

**未实现（本小节不装作已有）**：

- `security.allow_remote_commands` / `allow_raw_shell` / `remote_command_ports` / `raw_shell_ports`、`commands` 段：留到 P4 后续（HTTP 控制面本体见 §19.5）。
- 每网卡 `secure_on`：解析到就报错（`ErrPerInterfaceSecureOn`），因为包解析目前是"整个 policy 一个 secure_on"。
- 热重载、JSON Schema：未做。
- `--default-action` 只作用于 `--port` 生成的规则；文件里的规则必须显式写 `action`（缺 `action` -> `ErrActionRequired`）。

---

### 19.4 P4 已落地部分（exec 自定义命令）

- 动作模型扩到 `exec`：`ActionDef` 的参数从 `Params map[string]any` 改为随类型携带的强类型字段 `Exec *ExecParams{Command, Timeout, Workdir, Env, Shell}`——`map` 会让每个执行器各自解析、丢掉编译期校验；后续 `http`/`sequence` 走同一方式。
- 配置：`actions[]` 增 `command`（argv 数组）/ `timeout`（`5s`、`5m`）/ `workdir` / `env` / `shell`；新增 `security.exec_allowlist`（绝对路径命令必须落在列出的目录内，空 = 不限制）。
- 执行器 `internal/infra/exec.Executor`：默认 argv 直执（不经 shell），`context.WithTimeout`（默认 10s，超时即杀），`workdir` + 继承环境 + 追加 `env`，合并捕获 stdout/stderr；`shell: true` 才走 `/bin/sh -c`（Windows 用 `cmd /C`），启动时打 WARN。
- 启动期静态校验（fail fast）：`command` 必填；绝对路径须存在、非目录、带执行位，且配了 allowlist 时须在允许目录内；裸命令名走 `PATH` 查找；`user`/`group` 降权未实现 -> 写进配置直接报错 `ErrExecUserUnsupported`。
- 变量插值（白名单，§4.3）：`{{.Action}}` `{{.SrcIP}}` `{{.SrcPort}}` `{{.DstPort}}` `{{.Interface}}` `{{.MAC}}` `{{.Time}}`，用 `text/template` + `missingkey=error`（写错模板名在解析期就报错）。为此 `Decision` 增 `Interface`/`TargetMAC`（由包内目标 MAC 反查合格网卡），监听侧把上下文带进下发事件。
- 审计：执行前后 `slog` 记录动作名、argv、来源 IP、网卡、耗时、退出码与输出；非零退出/超时按错误上报。
- dry-run：`security.dry_run` 或规则级 `dry_run` 命中时只记日志（`trigger=DRY-RUN`），不执行。
- 冒烟（真机 enp6s0 + enp9s0f3u1，端口 10031）：按 allowlist 执行脚本，插值出 `mark=enp6s0 src=127.0.0.1 port=10031 mac=58:11:22:bc:78:66`，`env` 生效、审计行含 `exit_code=0`；allowlist 越界与命令不存在都在启动期 exit 1；dry-run 下目标文件不增长。
- 未做（继续留 P4）：`user`/`group` 降权、cooldown / 速率限制、HTTP 出站动作、远端命令通道（控制面见 §19.5）。

### 19.5 P4 部分落地（HTTP 控制面）

- 包 `internal/infra/httpapi`：纯 `net/http`（Go 1.22+ 方法模式路由），无框架依赖；配置段 `server.http.{enabled,listen,auth,tls}`，默认 `listen: 127.0.0.1:8080`、默认 `auth.type: bearer`。
- 已实现端点：`GET /healthz`（免认证，只回 `{"status":"ok"}`）、`GET /v1/status`（uptime、计数器、最近命中事件、网卡、规则数、dry_run、auth 类型）、`GET /v1/rules`（脱敏视图：ports / mac / content / src_cidrs / action / dry_run）、`GET /v1/interfaces`、`POST /v1/actions/{name}`（手动触发 -> 202，未知动作 -> 404）、`GET /metrics`（`sol_packets_total` / `sol_matched_total` / `sol_actions_total{action=...}` / `sol_rules` / `sol_uptime_seconds`）；`POST /v1/reload` 显式 501（热重载未做）。
- 认证三选一，**没有 `none`**：`bearer`（`crypto/subtle` 常量时间比较）、`basic`（用户名在 YAML，口令走环境变量或 600 文件）、`mtls`（TLS 层 `RequireAndVerifyClientCert` + `client_ca_file`）；所有 `/v1/*` 与 `/metrics` 强制认证，401 带 `WWW-Authenticate`。
- 密钥不落 YAML：只从 `*_env` 或 `*_file` 读；文件带 group/other 权限位直接启动报错（`ErrHTTPSecret`）。TLS 证书与客户端 CA 启动时加载，失败即失败。
- 审计：认证拒绝、手动触发、触发失败、reload 请求都进 slog（带来源地址与动作名）。
- 手动触发走 `ListenService.Dispatch`：实例处于 dry-run 时只记日志不执行；成功后计入 `sol_actions_total`。
- 冒烟（真机，`127.0.0.1:18080`，token 来自环境变量）：`/healthz` 免认证 200；无 token / 错 token 401；带 token 的 `/v1/status` 显示 `packets=1 matched=1 last_event={port:10040, interface:enp6s0, action:noop}`；`/v1/rules` 回 `{ports:[10040], mac:self, content:none, action:noop}`；`POST /v1/actions/noop` -> 202，未知动作 -> 404，`/v1/reload` -> 501。
- 未做（留后续）：热重载、`/v1/commands/{id}` 与 `/v1/exec`（随远端命令通道）、出站 `type: http` 动作、`/v1/status` 的版本号（需构建期注入）、mTLS 端到端冒烟。

---

## 20. 安全模型总览（汇总）

分层信任（风险从低到高）：

```
noop/记日志  <  power.sleep/lock  <  power.shutdown/reboot  <  exec 自定义命令  <  HTTP 出站/控制面  <  远端原始命令(裸 shell, 默认关闭)
```

要点：

- 保留端口 {7, 9} 只允许纯包 + `noop`（安全边界，防止误发/恶意唤醒包关机）。
- 端口号不是密钥：靠"换端口"路由不带任何密钥，谁扫到端口都能触发。
- 内容 token 本期是明文、无认证；破坏性动作建议同时配 `src_cidrs`；包级 HMAC 认证列为后续项。
- HTTP 控制面：默认本地 + 强制认证 + 可选 TLS + 审计；触发接口视为敏感。
- HTTP 出站动作：注意 SSRF，可选 `url_allowlist`；不记录密钥。
- 自定义命令：默认 argv 非 shell + 启动期静态校验 + 降权 + cooldown + 审计。
- 远端命令（§21）：白名单 `id` + 参数校验；裸 shell 默认关闭，开启后等价远程 shell，必须认证 + 专用端口 + 建议 allowlist/来源白名单 + 启动告警。
- 全局：`dry-run` 与构造期校验作为第一道防线；所有触发都写审计日志（来源、端口、命中规则、动作、结果）。

---

## 21. 远端命令通道（发送方指定要执行的命令）

定位：允许发送端不只是"触发预定义动作"，而是携带"命令 id + 参数"，由 sol 在**白名单**中解析并执行。这是 §4.3 exec 的动态版本。

风险先说清楚：WoL 是无认证广播。若允许远端发送任意命令，等于在本机开一个**无认证远程执行入口（RCE）**。因此本设计**不支持任意原始命令/裸 shell**，只支持"白名单命令 + 受校验参数"。

### 21.1 传输方式

- UDP（走包内容）：魔法包 + [SecureOn] + 命令段
  命令段格式：`<cmd-id>[:k=v,k=v]`（ASCII），前后附 HMAC。
- HTTP：`POST /v1/commands/{id}`，JSON body 传参数（复用 §18 控制面的认证）。

### 21.2 强制安全要求（缺一不可）

- 默认关闭：`security.allow_remote_commands: false`。
- 强制认证：包级 HMAC（预共享密钥）或 HTTP token/mTLS；无认证则拒绝启用该功能。
- 白名单：只有 `commands[].id` 注册过的才能被调用。
- 参数校验：每个命令声明 `args` 及类型/enum/正则，校验通过才拼进 argv。
- 非 shell：一律 argv，参数只做白名单插值，绝不拼接成 shell 字符串。
- 专用端口：只能绑非保留端口（不得 7/9）。
- 护栏：timeout、cooldown、速率限制、降权、审计、dry-run。

### 21.3 配置示例

```yaml
version: 1
server:
  interfaces: [eth0]
security:
  allow_remote_commands: false
  remote_command_auth: { type: hmac, key_env: SOL_CMD_KEY }
  remote_command_ports: [14]          # 非保留端口
commands:                              # 可被远端调用的白名单
  - id: lock
    type: exec
    command: [loginctl, lock-session]
  - id: backup
    type: exec
    command: [/usr/local/bin/backup.sh, "--target={{.Arg.target}}"]
    args:
      target: { type: string, enum: [home, work] }
    timeout: 5m
```

### 21.4 线格式（UDP）

```
[magic 102B] [secureon 6B?] "lock"                 -> 调用 id=lock
[magic 102B] [secureon 6B?] "backup:target=home"   -> 调 id=backup, arg target=home
```

HMAC：命令段前/后附 8 字节截断 `HMAC-SHA256(magic+命令段+key)`；校验失败即丢弃并记日志。

### 21.5 与现有模型的关系

- 静态：`rules: match.content -> action`（预定义固定动作，§13.10）。
- 动态：`远端命令段 -> 白名单 commands[].id`（本节）。
- 两者可共存：用不同端口/内容前缀区分；动态命令只绑在 `remote_command_ports`。

### 21.6 原始命令（裸 shell）——默认关闭，可显式开启

默认不提供（`allow_raw_shell: false`）。显式开启后，远端可直接发送任意 shell 命令，由 sol 用 `/bin/sh -c` 执行——**等价于开放一个远程 shell**。

开启前提（缺一即配置报错）：

- `security.allow_remote_commands: true`
- 强制认证已配（HMAC / token / mTLS）
- 绑定专用非保留端口

```yaml
security:
  allow_remote_commands: true
  allow_raw_shell: false                 # 默认关闭；true = 开放远程 shell
  raw_shell_ports: [15]                  # 专用非保留端口
  raw_shell_auth: { type: hmac, key_env: SOL_CMD_KEY }
  raw_shell_src_cidrs: ["10.0.0.0/24"]   # 强烈建议
  raw_shell_allowlist:                   # 可选；即便开了 raw 也建议限制
    - "^systemctl (suspend|reboot|poweroff)$"
    - "^/usr/local/bin/[a-z_]+\\.sh( .*)?$"
```

行为：

- 这是全项目唯一走 shell 的地方（其余动作一律 argv）。
- 执行前按 `raw_shell_allowlist`（若配）校验，不匹配即拒绝并记日志。
- 完整记录命令与退出码到审计日志。
- 降权、timeout、cooldown、限速、dry-run 同样生效。
- 启用时启动日志打**显著警告**。

线格式：

```
UDP:  [magic 102B] [secureon 6B?] <shell 命令字符串>     （HMAC 校验）
HTTP: POST /v1/exec  { "cmd": "..." }                    （认证）
```

阶段：P4+（见 [TODO.md](../TODO.md)）。