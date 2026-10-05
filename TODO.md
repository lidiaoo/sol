# SoL TODO

跟踪落地进度。设计见 [docs/routing-design.md](docs/routing-design.md)（括号内为章节号）。
约定：每条尽量对应一次可提交的改动；阶段完成标准 = `make test` + `make lint` 通过。

## 整体情况

- 项目：sol（bavix/sol）—— 监听 Wake-on-LAN 魔法包，触发本机电源动作（反向 WoL）。
- 本次目标：从"单端口 × 单网卡 × 单动作"扩成"多端口 × 多网卡 × 包内容匹配 × 具名动作"，并用**保留端口 {7,9}** 把标准 WOL 端口变成安全边界；HTTP、自定义命令、远端命令列为后续阶段。
- 现状：**设计定稿**（docs/routing-design.md，21 节 + 背景）；**P1（domain 模型与匹配）、P2（动作模型与 CLI）已落地，P3（配置文件）部分落地**，其余待开发。
- 怎么读：设计文档讲"为什么这么做 / 具体怎么做"；本文件讲"做到哪了 / 下一步"。
- 阶段：P1 模型 ✅ -> P2 动作与 CLI ✅ -> P3 配置文件（进行中）-> P4 自定义命令 + HTTP + 远端命令。

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
- [ ] `ErrRuleConflict` / `ErrInterfaceScopeConflict`：等 P3 的块级作用域（`server.interfaces[].rules` vs 全局）落地后再加；P1 用 `ErrDuplicatePort` / `ErrAmbiguousRule` 覆盖同作用域冲突
- [ ] 歧义判定目前保守（`src_cidrs` 集合需完全一致才算同一作用域）

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
- [x] 优先级：默认 < 文件 < 环境变量（`SOL_DRY_RUN` / `SOL_ALLOW_RESERVED_PORT_ACTIONS` / `SOL_INTERFACES` / `SOL_SECURE_ON`）< flag
- [x] 环境变量插值 `${VAR}` / `$VAR`（未设置即报错）
- [x] 全局规则 `server.rules` + 顶层 `rules` 简写（同现 -> `ErrRulesConflict`）
- [x] `server.interfaces`：字符串简写 + 块 `{name, dry_run?, rules?}`
- [x] 块内规则展开为 interface 作用域 + 块级 `dry_run` -> `Rule.DryRun`
- [x] `match.src_cidrs` 接线
- [x] `secure_on` 接线（全局；长度必须 6 字节 -> `ErrSecureOnLength`）
- [x] 同名网卡 / 端口重复 / 歧义规则校验（`ErrDuplicateInterface` / `ErrDuplicatePort` / `ErrAmbiguousRule`）
- [x] `actions` 段 + 命名动作引用（注册进 Registry；重名 -> `ErrDuplicateAction`）
- [ ] `logging` 段：需要先把 `log` 换成 `slog`（`level` / `format`：text/json）
- [ ] 每网卡 `secure_on`：需要支持 per-rule secureOn 的包解析（现状是整 policy 一个）
- [ ] 冲突检测 `ErrRuleConflict` / `ErrInterfaceScopeConflict`：跨作用域（全局 vs 块）冲突的显式报错
- [ ] 热重载（可选，等价 SIGHUP）
- [ ] 附 JSON Schema（编辑器补全）

## P4 自定义命令 + HTTP + 远端命令（§4.3、§18、§21）

完成标准：默认全部关闭；显式开启后按安全约束生效。

- [ ] `exec` 动作（argv 非 shell、timeout、workdir/env/user/group、审计）
- [ ] `exec` 启动期静态校验（可执行存在 / allowlist 目录）
- [ ] HTTP 控制面（bearer/basic/mTLS、默认 127.0.0.1、`/v1/*`、审计）
- [ ] HTTP 出站动作（webhook、`url_allowlist`、超时 / 重试）
- [ ] 远端命令通道（`commands[].id` 白名单 + args 校验 + HMAC）
- [ ] 远端原始命令（`allow_raw_shell` 默认关 + `/bin/sh -c` + 认证 / 端口 / allowlist + 启动告警）
- [ ] `wol.send`（预留，唤醒别的机器）
- [ ] 全局 cooldown / 速率限制

## 文档 / 发布

- [ ] README：`--port 9` 行为变更；systemd 示例改非保留端口；`sol ifaces` 说明
- [ ] README：<1024 端口（7/9/8）需 root 或 `CAP_NET_BIND_SERVICE`
- [ ] CHANGELOG：标注 breaking（`--port 9` shutdown -> noop）
- [ ] 设计文档 §19 与本文件保持同步

## 待确认 / 开放问题

- [ ] 是否需要"多个 server 块"（当前设计为单 server）
- [ ] 块级可覆盖字段范围：`dry_run`/`secure_on` 之外是否还要 `reserved_ports`、块级 `actions`？
- [ ] 内容 token 的包级 HMAC 认证（本期为明文，建议后续）
- [ ] 远端原始命令的默认 allowlist 策略（即便 raw 模式也建议限制，见 §21.6）
- [ ] 内容前缀 `ContentPrefix` 的 offset 是否需要支持超出内容区的绝对偏移
- [ ] 审计日志落地形式（stdout / 文件 / syslog）
- [ ] 热重载的原子性（重载失败是否回滚到旧配置）