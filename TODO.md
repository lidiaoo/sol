# SoL TODO

跟踪落地进度。设计见 [docs/routing-design.md](docs/routing-design.md)（括号内为章节号）。
约定：每条尽量对应一次可提交的改动；阶段完成标准 = `make test` + `make lint` 通过。

## 整体情况

- 项目：sol（bavix/sol）—— 监听 Wake-on-LAN 魔法包，触发本机电源动作（反向 WoL）。
- 本次目标：从"单端口 × 单网卡 × 单动作"扩成"多端口 × 多网卡 × 包内容匹配 × 具名动作"，并用**保留端口 {7,9}** 把标准 WOL 端口变成安全边界；HTTP、自定义命令、远端命令列为后续阶段。
- 现状：**设计定稿**（docs/routing-design.md，21 节 + 背景）；**P1（domain 模型与匹配）、P2（动作模型与 CLI）、P3（配置文件）全部落地**；**P4 主体落地**：exec / HTTP 控制面 / cooldown / 远端命令通道 / HTTP 出站 / mTLS / exec 降权 / 热重载 / `sequence` / 配置文件自动 reload。剩余项见下方未勾选条目。
- 怎么读：设计文档讲"为什么这么做 / 具体怎么做"；本文件讲"做到哪了 / 下一步"；README 面向使用者（安装 / CLI / 配置 / 安全），CHANGELOG 面向升级者（breaking 变更）。
- 阶段：P1 模型 ✅ -> P2 动作与 CLI ✅ -> P3 配置文件 ✅ -> P4 自定义命令 + HTTP + 远端命令（**主体 ✅**，剩包级 HMAC、`allow_raw_shell`、cooldown singleflight 等）；`wol.send` 唤醒别的机器 ✅（§19.13）。

---

## P1 domain 模型与匹配（§3、§5、§6、§7、§8）✅

完成标准：新模型单测全绿；对现有行为零破坏（除新增"内容匹配 + 保留端口"两条规则）。
落地位置：`internal/domain/wol/{packet,match,policy}.go`（+ `action.go` 扩展 `noop`）。实现对照见设计 §19.1。

包解析
- [x] `Event`（Payload / SrcIP / SrcPort / DstPort）
- [x] `ParsedPacket` + `ParsePacket(payload, secureOn)`：偏移 0 精确匹配魔法包；按 102/108 切分内容区
- [x] `Match` 走 `ParsePacket`，不再用 `bytes.Contains`（`ContainsMagicPacket` 保留但已不被服务调用）

匹配模型
- [x] `ContentMatcher`：kind `any|none|suffix|prefix`；`value` / `value_hex` 互斥解码（`Compile()` 校验）
- [x] `MACSelector`：`self | interface | explicit | any` + `Ifaces`
- [x] `Match`（ports / mac / content / src_cidrs）+ `Rule`（Match + Action）
- [x] `interfaces` 糖衣等价于 `MACSelector{Kind: interface}`；与显式 `mac` 同设 -> `ErrMACConflict`

策略与校验
- [x] `IfaceInfo` + `RoutingPolicy`（内部 ifaceMACs / allMACs）
- [x] `Resolve(Event)` + 具体度排序（content > src_cidr > mac > port）
- [x] 保留端口约束（§5）+ `PolicyOptions{ReservedPorts, AllowReserved, SecureOn}`
- [x] 错误类型：`ErrReservedPortAction` / `ErrInvalidPort` / `ErrDuplicatePort` / `ErrAmbiguousRule` / `ErrUnknownActionRef` / `ErrUnknownInterface` / `ErrDuplicateInterface` / `ErrMACConflict` / `ErrContentValue` / `ErrContentTooLarge` / `ErrUnknownContentKind` / `ErrUnknownMACKind` / `ErrInvalidCIDR`
- [x] 单测：包解析 / 内容匹配 / 优先级 / 保留端口 / 冲突 / 来源白名单（`internal/domain/wol/*_test.go`）

遗留（不阻塞 P1）
- [x] `ErrRuleConflict` / `ErrInterfaceScopeConflict`：已在 P3 落地（跨作用域冲突检测 + 块作用域校验，见设计 §19.3）
- [ ] 歧义判定仍偏保守（`src_cidrs` 需完全相同才算同一作用域；重叠但不相同的 CIDR 集合不报错）

## P2 动作模型与 CLI（§4、§10、§16、§17）✅

完成标准：`sol listen` 多端口多动作可跑；`sol ifaces` 可用；`--port 9` 打 warning。
实现对照见设计 §19.2。

- [x] `Executor` 接口 + `Registry`（按 ActionType 分发；`Register(executor, types...)`）
- [x] 内置动作 `noop` / `power.shutdown` / `power.reboot` / `power.sleep`（`BuiltinActions()`）
- [x] `PowerController` 改造为实现 `Executor`（+ `NoopExecutor`）；补 sleep 各平台命令
- [x] `ListenService.handlePacket` -> `policy.Resolve` + `registry.Dispatch`
- [x] 网卡枚举 `List()` / `Select()`（Up / 非 loopback / 有 MAC / 排虚拟）
- [x] CLI：`--port` 可重复带动作；`--iface` 可重复 / 省略即 auto
- [x] CLI：`--allow-reserved-actions`、`--default-action`
- [x] 子命令 `sol ifaces`（NAME/TYPE/STATUS/MAC/IPV4/AUTO，`--json`）
- [x] 启动日志打印选中网卡集合 + 规则列表
- [x] `--port 9` 行为改为 `noop` + 启动 warning（breaking）
- [x] `PolicyOptions.Actions` 动作白名单（P3 的 `actions` 段从这里注入）

## P3 配置文件（§9、§13、§17.3/17.9）进行中

完成标准：同一套规则能用 YAML 表达并入参；严格解码；优先级正确。
实现对照见设计 §19.3。

- [x] 引入 YAML 库（`gopkg.in/yaml.v3`）+ 严格解码（未知字段报错）——顺带在 `.golangci.yml` 的 depguard 允许列表加 `gopkg.in`
- [x] `--config` flag + 配置发现顺序：`--config` > `$SOL_CONFIG` > `/etc/sol/sol.yaml` > `~/.config/sol/sol.yaml`
- [x] 优先级：默认 < 文件 < 环境变量（`SOL_DRY_RUN` / `SOL_ALLOW_RESERVED_PORT_ACTIONS` / `SOL_INTERFACES` / `SOL_SECURE_ON` / `SOL_LOG_LEVEL` / `SOL_LOG_FORMAT`）< flag
- [x] 环境变量插值 `${VAR}` / `$VAR`（未设置即报错）
- [x] 全局规则 `server.rules` + 顶层 `rules` 简写（同现 -> `ErrRulesConflict`）
- [x] `server.interfaces`：字符串简写 + 块 `{name, dry_run?, rules?}`
- [x] 块内规则展开为 interface 作用域 + 块级 `dry_run` -> `Rule.DryRun`
- [x] `match.src_cidrs` 接线
- [x] `secure_on` 接线（全局；长度必须 6 字节 -> `ErrSecureOnLength`）
- [x] 同名网卡 / 端口重复 / 歧义规则校验（`ErrDuplicateInterface` / `ErrDuplicatePort` / `ErrAmbiguousRule`）
- [x] `actions` 段 + 命名动作引用（注册进 Registry；重名 -> `ErrDuplicateAction`）
- [x] `logging` 段（`level` / `format`：text/json）+ `log` -> `log/slog` 结构化日志迁移（`internal/infra/logging`）
- [ ] 每网卡 `secure_on`：需要支持 per-rule secureOn 的包解析（现状是整 policy 一个）
- [x] 冲突检测 `ErrRuleConflict` / `ErrInterfaceScopeConflict`：跨作用域（全局 vs 块）冲突的显式报错
- [x] 热重载（`SIGHUP` + `POST /v1/reload` + `server.watch`/`--watch` 自动 reload；见 §19.9）
- [x] 附 JSON Schema（编辑器补全）：`schema/sol.schema.json`（2020-12，未知字段一律拒绝、enum/required 与加载器一致）+ 防漂移测试 `internal/config/schema_internal_test.go`（字段集合与 Go 结构体 yaml tag 双向比对；action/content/mac/auth/logging 的 enum 与 domain 常量比对——这条抓到了我手写 schema 时把 content kind 误写成 `exact`，实际是 any|none|suffix|prefix）

## P4 自定义命令 + HTTP + 远端命令（§4.3、§18、§21）进行中

完成标准：默认全部关闭；显式开启后按安全约束生效。
实现对照见设计 §19.4。

- [x] `exec` 动作（argv 非 shell、timeout、workdir/env、审计日志、变量插值白名单）
- [x] `exec` 启动期静态校验（可执行存在 / 非目录 / 有执行位 / `security.exec_allowlist` 目录）
- [x] `exec` 的 `user`/`group` 降权（仅 unix；启动期解析用户/组 + 要求 root，运行时 `SysProcAttr.Credential` + `initgroups` 语义，sol 自己的附加组不泄漏）——见设计 §19.4
- [ ] 降权只支持 root（`CAP_SETUID`/`CAP_SETGID` 单权限）；非 unix 平台直接报 `ErrUserUnsupported`
- [x] HTTP 控制面（bearer/basic/mTLS、默认 127.0.0.1、`/v1/status`、`/v1/rules`、`/v1/interfaces`、`/v1/actions/{name}`、`/metrics`、`/healthz`、审计）——实现对照见设计 §19.5
- [x] `/v1/reload` 热重载 + `SIGHUP`（原子换入 policy/registry/cooldown/remote；端口集合变化 -> 409 要求重启；配置非法 -> 400 且旧配置继续跑；`-race` 下 60 次 reload 无 data race）——见设计 §19.9
- [ ] 热重载重绑端口 / 网卡集合（当前必须重启）
- [x] 配置文件变更自动 reload（`server.watch: 5s` / CLI `--watch`；轮询式，刻意不引 fsnotify，最小间隔 1s，坏配置只记错不换掉老配置；实测覆盖写入后 1 个轮询周期内自动 `configuration reloaded`）——见设计 §19.9
- [x] mTLS 端到端冒烟（配置已支持 + 启动加载证书；带证书 200、无证书/异 CA 证书握手被拒、明文 HTTP 400；注意 mTLS 下 `/healthz` 也需客户端证书）——见设计 §19.5
- [x] HTTP 出站动作（webhook、`url_allowlist`、超时 / 重试、不跟随重定向、headers 不落日志）——见设计 §19.8
- [x] `sequence`（一个动作串多个动作：按序执行、失败不中断后续、`errors.Join` 汇总、不许嵌套 / 自引用；护栏按组合名算）——见设计 §19.10
- [x] 出站 allowlist 的精确 / 正则匹配（裸串前缀改为 host+path 边界匹配、`=` 精确、`~` 正则；条目非法启动即报；实测 lookalike host `https://hooks.example.com.evil.net` 被拒）
- [x] 远端命令通道（`commands[].id` 白名单 + HMAC + 参数校验 + UDP/HTTP 双传输；`remote:<id>` 注册为普通动作，复用 cooldown / dry-run / 审计）——见设计 §19.7
- [x] 远端命令的 `user`/`group` 降权（复用 §19.4 的 credential 代码：`commands[].user/group` 透传进 `ExecParams`，启动期校验与 setgroups 零改动继承；实测 `output=65534 run_as=nobody`、非 root 启动报 `ErrNotRoot`、未知用户报 `ErrUnknownUser`）
- [ ] 覆盖整包的包级 HMAC（当前只认证命令段）
- [ ] 远端原始命令（`allow_raw_shell` 默认关 + `/bin/sh -c` + 认证 / 端口 / allowlist + 启动告警）
- [x] `wol.send`（唤醒别的机器：`mac`（必填，只来自配置）+ `broadcast` / `port` / `secure_on` / `repeat` / `interval`（默认广播 255.255.255.255、端口 9、1 份、100ms）；加载期填默认值；与 `ParsePacket` 互为逆的 `EncodeMagicPacket`；广播发送补 `SO_BROADCAST`（否则 `EACCES`）；复用 cooldown / 限流 / dry-run / 审计。顺手修掉 sequence 上 `timeout` 被静默忽略的既有漏洞）——见设计 §19.13
- [x] 按动作 cooldown（`security.cooldown` + `security.cooldowns.<动作名>`；包触发与手动触发共用，抑制计入 `sol_suppressed_total`，手动触发返回 429）——见设计 §19.6
- [x] 全局速率限制（令牌桶 / 每秒上限）：`security.rate_limit`（`10/s`、`600/m`、`3600/h`，裸数字 = 每秒；空/0 = 关闭）+ `security.rate_burst`（桶容量，0 = 一秒的 rate_limit；只写 burst 不写 rate 启动报错）。跨所有动作与触发源（包 / 手动 / 远端命令）计数；抑制计入 `sol_suppressed_total` 与新的 `sol_rate_limited_total`，手动触发 429；reload 时限额未变则保留已耗尽的桶；`/v1/status` 回显 `rate_limit{per_second,burst}` + `rate_limited`——见设计 §19.12
- [ ] cooldown 的 singleflight（执行中再次触发的合并语义）

## 文档 / 发布

- [x] README：`--port 9` 行为变更；systemd 示例改非保留端口；`sol ifaces` 说明（README 已重写：新增动作表、配置文件示例、控制面、远端命令、reload、接口选择、迁移说明）
- [x] README：<1024 端口（7/9/8）需 root 或 `CAP_NET_BIND_SERVICE`（含 systemd `AmbientCapabilities` 示例与非 root 建议）
- [x] CHANGELOG：标注 breaking（`--port 9` shutdown -> noop；`--iface` 不再必填；默认严格匹配）+ 全部新增能力；新建 CHANGELOG.md
- [x] 设计文档 §19 与本文件保持同步；并补齐四向交叉链接：README -> 设计/TODO/CHANGELOG、CHANGELOG -> 设计/TODO
- [x] README 配置示例经真机验证：`sol listen --config` 加载无误、控制面 `/healthz` 200、自动选网卡、`/v1/rules` 回显两条规则、带 `lock` 后缀的包命中并执行成功

## 待确认 / 开放问题

- [ ] 是否需要"多个 server 块"（当前设计为单 server）
- [ ] 块级可覆盖字段范围：`dry_run`/`secure_on` 之外是否还要 `reserved_ports`、块级 `actions`？
- [ ] 内容 token 的包级 HMAC 认证（本期为明文，建议后续）
- [ ] 远端原始命令的默认 allowlist 策略（即便 raw 模式也建议限制，见 §21.6）
- [ ] 内容前缀 `ContentPrefix` 的 offset 是否需要支持超出内容区的绝对偏移
- [ ] 审计日志落地形式（stdout / 文件 / syslog）
- [ ] 热重载的原子性（重载失败是否回滚到旧配置）