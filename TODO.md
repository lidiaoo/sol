# SoL TODO

跟踪落地进度。设计见 [docs/routing-design.md](docs/routing-design.md)（括号内为章节号）；安装 / 覆盖升级 / 卸载 / 状态查看的设计见 [docs/install-design.md](docs/install-design.md)。
约定：每条尽量对应一次可提交的改动；阶段完成标准 = `make test` + `make lint` 通过。

## 整体情况

- 项目：sol（bavix/sol）—— 监听 Wake-on-LAN 魔法包，触发本机电源动作（反向 WoL）。
- 本次目标：从"单端口 × 单网卡 × 单动作"扩成"多端口 × 多网卡 × 包内容匹配 × 具名动作"，并用**保留端口 {7,9}** 把标准 WOL 端口变成安全边界；HTTP、自定义命令、远端命令列为后续阶段。
- 现状：**设计定稿**（docs/routing-design.md，21 节 + 背景）；**P1（domain 模型与匹配）、P2（动作模型与 CLI）、P3（配置文件）全部落地**；**P4 主体落地**：exec / HTTP 控制面 / cooldown / 远端命令通道 / HTTP 出站 / mTLS / exec 降权 / 热重载 / `sequence` / 配置文件自动 reload。剩余项见下方未勾选条目。
- 怎么读：设计文档讲"为什么这么做 / 具体怎么做"；本文件讲"做到哪了 / 下一步"；README 面向使用者（安装 / CLI / 配置 / 安全），CHANGELOG 面向升级者（breaking 变更）。
- 阶段：P1 模型 ✅ -> P2 动作与 CLI ✅ -> P3 配置文件 ✅ -> P4 自定义命令 + HTTP + 远端命令（**主体 ✅**，护栏与重入语义已补齐）；`wol.send` 唤醒别的机器 ✅（§19.13）；包级 HMAC ✅（§19.14）；远端原始命令（默认关）✅（§21.6 / §19.15）。

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
- [x] 歧义判定不再偏保守：`src_cidrs` **相交**即算条件相同（`netsIntersect`：`a.Contains(b.IP) || b.Contains(a.IP)`，v4/v6 互不相交），同作用域 -> `ErrAmbiguousRule`、跨作用域 -> `ErrRuleConflict`；顺带把 `ErrDuplicatePort` 收窄成"连 src 也完全相同"，消息不再误导。单测 `srcfilter_internal_test.go` + 冲突用例 5 条。剩余开放项：前缀长度不是特异性信号（"最长前缀优先"未实现）

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
- [x] 每作用域 `secure_on`：三级（`match.secure_on` > `server.interfaces[].secure_on` > `security.secure_on`），`secure_on: ""` = 显式不要口令；包解析改成多候选试读（`ParsePacketAny`，明文回退 + 规则级 `SecureOn` 对等比较，默认 `content: none` 让它 fail-closed）；命令通道仍用严格单口令；保留端口配口令启动即报错（breaking）；`ErrPerInterfaceSecureOn` 删除。见设计 §19.18，冒烟 s26
- [x] 冲突检测 `ErrRuleConflict` / `ErrInterfaceScopeConflict`：跨作用域（全局 vs 块）冲突的显式报错
- [x] 热重载（`SIGHUP` + `POST /v1/reload` + `server.watch`/`--watch` 自动 reload；见 §19.9）
- [x] 附 JSON Schema（编辑器补全）：`schema/sol.schema.json`（2020-12，未知字段一律拒绝、enum/required 与加载器一致）+ 防漂移测试 `internal/config/schema_internal_test.go`（字段集合与 Go 结构体 yaml tag 双向比对；action/content/mac/auth/logging 的 enum 与 domain 常量比对——这条抓到了我手写 schema 时把 content kind 误写成 `exact`，实际是 any|none|suffix|prefix）

## P4 自定义命令 + HTTP + 远端命令（§4.3、§18、§21）进行中

完成标准：默认全部关闭；显式开启后按安全约束生效。
实现对照见设计 §19.4。

- [x] `exec` 动作（argv 非 shell、timeout、workdir/env、审计日志、变量插值白名单）
- [x] `exec` 启动期静态校验（可执行存在 / 非目录 / 有执行位 / `security.exec_allowlist` 目录）
- [x] `exec` 的 `user`/`group` 降权（仅 unix；启动期解析用户/组 + 要求 root，运行时 `SysProcAttr.Credential` + `initgroups` 语义，sol 自己的附加组不泄漏）——见设计 §19.4
- [x] 降权边界如实：只支持 root（`requirePrivilege()` = `geteuid()==0`，**不**支持 `CAP_SETUID`/`CAP_SETGID` 单权限），`ErrNotRoot` 文案改成 `exec user/group requires root`（原来承诺的 `or CAP_SETUID/CAP_SETGID` 没接线，`TestRequirePrivilegeMessageOnlyPromisesRoot` 断言不再出现 `CAP_`）；非 unix 平台配了降权直接报 `ErrUserUnsupported`（`credential_other.go` + `//go:build !unix` 单测，证据 = `GOOS=windows/darwin/freebsd go build ./...` 与 `GOOS=windows go vet` 全过）
- [x] HTTP 控制面（bearer/basic/mTLS、默认 127.0.0.1、`/v1/status`、`/v1/rules`、`/v1/interfaces`、`/v1/actions/{name}`、`/metrics`、`/healthz`、审计）——实现对照见设计 §19.5
- [x] `/v1/reload` 热重载 + `SIGHUP`（原子换入 policy/registry/cooldown/remote；端口集合变化 -> 409 要求重启；配置非法 -> 400 且旧配置继续跑；`-race` 下 60 次 reload 无 data race）——见设计 §19.9
- [x] 热重载重绑端口 / 网卡集合：`listenerSet`（`internal/app/listeners.go`）先 `rebind()` 绑新增端口、全部成功才换状态并 `commit()`（新增开读、离开的 `Close()`）；失败整体拒绝（`ErrReloadBind` -> 409），已跑的端口一个不动。网卡不需要重绑（socket 绑 `0.0.0.0`），但 `ReloadOptions.Ifaces` 会刷新状态视图/日志——见设计 §19.9
- [x] 配置文件变更自动 reload（`server.watch: 5s` / CLI `--watch`；轮询式，刻意不引 fsnotify，最小间隔 1s，坏配置只记错不换掉老配置；实测覆盖写入后 1 个轮询周期内自动 `configuration reloaded`）——见设计 §19.9
- [x] mTLS 端到端冒烟（配置已支持 + 启动加载证书；带证书 200、无证书/异 CA 证书握手被拒、明文 HTTP 400；注意 mTLS 下 `/healthz` 也需客户端证书）——见设计 §19.5
- [x] HTTP 出站动作（webhook、`url_allowlist`、超时 / 重试、不跟随重定向、headers 不落日志）——见设计 §19.8
- [x] `sequence`（一个动作串多个动作：按序执行、失败不中断后续、`errors.Join` 汇总、不许嵌套 / 自引用；护栏按组合名算）——见设计 §19.10
- [x] 出站 allowlist 的精确 / 正则匹配（裸串前缀改为 host+path 边界匹配、`=` 精确、`~` 正则；条目非法启动即报；实测 lookalike host `https://hooks.example.com.evil.net` 被拒）
- [x] 远端命令通道（`commands[].id` 白名单 + HMAC + 参数校验 + UDP/HTTP 双传输；`remote:<id>` 注册为普通动作，复用 cooldown / dry-run / 审计）——见设计 §19.7
- [x] 远端命令的 `user`/`group` 降权（复用 §19.4 的 credential 代码：`commands[].user/group` 透传进 `ExecParams`，启动期校验与 setgroups 零改动继承；实测 `output=65534 run_as=nobody`、非 root 启动报 `ErrNotRoot`、未知用户报 `ErrUnknownUser`）
- [x] 覆盖整包的包级 HMAC（`security.packet_auth` + `match.auth: hmac`；tag = 截断 HMAC-SHA256 8 字节，覆盖 tag 之前的全部字节含 SecureOn；认证是包的属性并进审计日志 `authenticated=`；`auth` 规则只接受校验通过的包，同端口压过普通规则；保留端口 / 无 key / `sign` 无 key 三种组合启动即报错；`wol.send` 的 `sign: true` 让 sol 能唤醒要求认证的 sol）——见设计 §19.14
- [x] 认证包的重放防护（`security.packet_auth.window`，默认关；包尾变成 `[stamp 8B][tag 8B]`，stamp 在 tag 覆盖内；接收方只收 ±window 内的 stamp 且同一 tag 只接受一次；缓存 4096 条、满了拒绝不淘汰；两端须同配，reload 会清空缓存）——见设计 §19.16；冒烟 s21 12/12（同包重发被拒 `reason=replay`、-600s/+600s 被拒 `reason=stale`、旧布局被拒、windowed `wol.send` 端到端仍通）
- [x] §21 命令段的重放防护：`remote_command_auth.window` / `raw_shell_auth.window`（各自独立、默认关闭）；线格式 `[magic][secureon?]<段>[stamp 8B][tag 8B]`，与包级共用 `wol.ReplayGuard`；拒绝计入同一批 `replayed` / `sol_replayed_total`，日志带 `channel=command|raw_shell`；reload 经 `ReloadOptions.RemoteWindow` 保留（否则第一次 SIGHUP 就静默关掉它）；冒烟 s22 真机 16/16——见设计 §21.3 / §21.4 / §19.17
- [x] 重放的计数与指标：`/v1/status` 的 `replayed` + `replay_reasons`（全 0 时不出现）与 `/metrics` 的 `sol_replayed_total` / `sol_replayed_total{reason="..."}`；计数在 builder 里由 `OnAuthRejected` 累加，reload 不清零——见设计 §19.16
- [x] 远端原始命令（`allow_raw_shell` 默认关 + `/bin/sh -c` + 认证 / 专用端口 / allowlist / `src_cidrs` + 启动告警 + HTTP `POST /v1/exec`）——见设计 §21.6 / §19.15；冒烟 s20 27/27（含\"默认关\"的实证：去掉开关后同一个包什么都不做、落回该端口规则）
- [x] `wol.send`（唤醒别的机器：`mac`（必填，只来自配置）+ `broadcast` / `port` / `secure_on` / `repeat` / `interval`（默认广播 255.255.255.255、端口 9、1 份、100ms）；加载期填默认值；与 `ParsePacket` 互为逆的 `EncodeMagicPacket`；广播发送补 `SO_BROADCAST`（否则 `EACCES`）；复用 cooldown / 限流 / dry-run / 审计。顺手修掉 sequence 上 `timeout` 被静默忽略的既有漏洞）——见设计 §19.13
- [x] 按动作 cooldown（`security.cooldown` + `security.cooldowns.<动作名>`；包触发与手动触发共用，抑制计入 `sol_suppressed_total`，手动触发返回 429）——见设计 §19.6
- [x] 全局速率限制（令牌桶 / 每秒上限）：`security.rate_limit`（`10/s`、`600/m`、`3600/h`，裸数字 = 每秒；空/0 = 关闭）+ `security.rate_burst`（桶容量，0 = 一秒的 rate_limit；只写 burst 不写 rate 启动报错）。跨所有动作与触发源（包 / 手动 / 远端命令）计数；抑制计入 `sol_suppressed_total` 与新的 `sol_rate_limited_total`，手动触发 429；reload 时限额未变则保留已耗尽的桶；`/v1/status` 回显 `rate_limit{per_second,burst}` + `rate_limited`——见设计 §19.12
- [x] 执行中的重入（in-flight 去重，替代"cooldown singleflight"）：语义定为**抑制**而非合并等待；身份 = 动作名 + 参数 / 裸 shell 命令行；无条件开启；计入 `suppressed` + `inflight`（`/metrics` 的 `sol_inflight_total`）；三条 HTTP 触发路径回 429。顺带修掉两个真缺陷（远端命令被护栏拒绝回 500、`/v1/exec` 被护栏拒绝回 202 却不执行）——见设计 §19.12.1，冒烟 s24 真机 16/16

- [x] 构建身份（`internal/buildinfo`）：`sol --version` + `/v1/status` 的 `version`/`revision` + `/metrics` 的 `sol_build_info{version,revision} 1`；`make build`/`make build-static` 用 `git describe` 打标，未打标时回落到工具链自带的伪版本/模块 tag（`-s -w -trimpath` 之后仍在——发布流水线无需改动，它连 ldflags 都不暴露）；冒烟 s28 真机 11/11（含"跑完不许脏仓库"自检）——见设计 §19.5

## 文档 / 发布

- [x] README：`--port 9` 行为变更；systemd 示例改非保留端口；`sol ifaces` 说明（README 已重写：新增动作表、配置文件示例、控制面、远端命令、reload、接口选择、迁移说明）
- [x] README：<1024 端口（7/9/8）需 root 或 `CAP_NET_BIND_SERVICE`（含 systemd `AmbientCapabilities` 示例与非 root 建议）
- [x] CHANGELOG：标注 breaking（`--port 9` shutdown -> noop；`--iface` 不再必填；默认严格匹配）+ 全部新增能力；新建 CHANGELOG.md
- [x] 设计文档 §19 与本文件保持同步；并补齐四向交叉链接：README -> 设计/TODO/CHANGELOG、CHANGELOG -> 设计/TODO
- [x] README 配置示例经真机验证：`sol listen --config` 加载无误、控制面 `/healthz` 200、自动选网卡、`/v1/rules` 回显两条规则、带 `lock` 后缀的包命中并执行成功
- [x] 中文 README `README.zh-CN.md`：与英文版逐节对应 + 顶部双向语言切换；**代码块逐字节一致**由 `internal/config/readme_sync_internal_test.go`（`TestReadmeTranslationsAgree`，比对 fence 语言标签与正文）守护；真机侧 `s26/readme_both.sh` 把两份 README 的全部 `version: 1` yaml 块抽出真加载（10/10）
- [x] 交叉链接补全：README.zh-CN -> README/设计/TODO/CHANGELOG
- [x] README 补 `sol --version` 与构建身份说明（`/v1/status` 的 `version`/`revision`、`/metrics` 的 `sol_build_info`），中英文同步（bash 块逐字节一致）

## 待确认 / 开放问题（逐条落定，决定 + 理由 + 证据）

- [x] **不需要"多个 server 块"**：决定 = 单实例单策略。理由 = 策略/动作注册表/护栏/控制面都是进程级不变量，两个 server 块会让"哪一份接口枚举、哪一份保留端口集合、哪个注册表生效"变成不可判定；想要两套配置的正解是跑两个进程（各自 `--config`），`server.interfaces` 块已经覆盖"按网卡分规则"这一真实需求。见设计 §8 / §17.9。
- [x] **块级可覆盖字段 = `dry_run` + `secure_on`**（就是已实现的两个，见设计 §?；`ifaceConfig` 只有这两个字段）：决定 = 不扩到 `reserved_ports` / 块级 `actions`。理由 = 这两个是**作用域天然相关**的（"只让某张网卡试运行"、"每张网卡一个 WOL 口令"），而保留端口是安全边界、actions 是注册表，都是进程级不变量；块级副本会让"这条规则受哪份集合约束"不可判定，还要新定义"块级动作与全局重名"的冲突语义。要扩先定冲突语义，当前不做。
- [x] 内容 token 的整包 HMAC 认证（`security.packet_auth` + `match.auth: hmac`，§19.14；此前为明文）
- [x] **裸 shell 空 allowlist = 放行一切**（保持现状，见 §21.6 / §19.15）：决定 = 不改语义。理由 = 开启通道（`allow_raw_shell: true` + 独立密钥 + 专用端口 + `src_cidrs`）本身就是那个显式决定，再要求"必须显式放行"等于第二道开关，反而制造"以为开了其实没开"的错觉；但**必须可观测**：启动告警已带 `allowlist_entries=0`（真机日志可证），README 也写明"空 = 每条命令都放行"。
- [x] **`ContentPrefix` 不支持绝对偏移**：决定 = 不做。理由 = 内容区被定义为"魔法包之后那段"（102/108 字节起算），`prefix` = 内容区开头、`suffix` = 内容区结尾；绝对偏移会引入第二套坐标系，而且对 `suffix` 无意义。要匹配更深处的内容用 `suffix`（尾部对齐）即可；真有用例再谈。
- [x] **"最长前缀优先"（CIDR 前缀长度 / 嵌套内容取值）**：决定 = **不实现，保持"重叠必须显式化"**。现状 = 嵌套条件（`10.1/16` + `10/8` 同端口；`prefix: off` + `prefix: offnow` 同端口）启动期一律 `ErrAmbiguousRule`；不相交网段 / 不同端口 / 不同 `secure_on` / 更严 `auth` 四种写法都能加载（单测 `TestAmbiguityHintIsActionable` 逐一验证）。理由 = 特异性是**各维度加权和**（content 100 / auth 50 / src 10 / mac 10 / port 1），把长度加进和里会跨维度串味（一条不需认证但前缀稍长的规则能反超要求 HMAC 的规则），而长度只在同一维度内有序，跨维度（更窄网段 vs 更窄 MAC）本无全序。**顺手改进**：`ErrAmbiguousRule` 文案点名四条出路（真机 s25 14/14）。
- [x] **请求级代理**（设计里最后一条"未做"之一）：已落地 `actions[].proxy`（http/https/socks5/socks5h，启动期校验 `ErrProxy`；Go transport 原生支持，无新依赖）+ 按代理串缓存 client（上界 = 配置条数）+ 代理 client 同样不跟随重定向。语义：空 = 共享 client（与 Go 默认 transport 一样读 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`），非空则覆盖。**不是 allowlist 后门**：目的地照旧按 `security.url_allowlist` 校验（启动期 + 每次尝试），单测证明被拒时代理零命中。剩余未做只剩"响应体内容过滤"。见设计 §19.8。
- [x] **网卡身份不能只看启动那一刻的 up/down**（用户指出）：已落地 = 合格判据去掉 `Up`（身份 ≠ 可用性）+ 运行期身份集合热换（`RoutingPolicy.SetInterfaces`，原子换 `policyState`）+ 未命中惰性刷新（1 秒下限，变化则重试该包一次）+ 30 秒轮询兜底 + `interface set changed` 审计。真机根因：`wlp5s0` down 时 `82:44:59:ff:68:48`（随机占位）vs up 后 `f0:d4:15:57:9c:c5`（真 MAC）——启动时可能锁死一个永不上线的地址。显式 `interfaces: [x]` 启动期仍严格报错、运行期稍后出现则纳入。冒烟 s30 真机 12/12（dummy 卡；含零流量下轮询纳入）。见设计 §17.2 / §17.2.1。
- [x] **审计日志落地形式**：已落地 `logging.output` = `stderr`（默认）/ `stdout` / `file`（`logging.file`，追加 + 0600 + 不缓冲 + 不轮转），**不做 syslog**（标准库 `log/slog` 没有 syslog handler，引第三方库越界；要 syslog 就让 journald/rsyslog 转发）。三层校验（config 合并后校验 / OpenOutput 再校验 / Setup 装载），三处取值集合（config、infra、schema enum）由 `TestLoggingOutputNamesAgreeEverywhere` 防漂移；冒烟 s29 真机 14/14（文件里有审计记录、0600、stderr 为空、重启追加 5->9 行、stdout 模式、三种启动期拒绝）。见设计 §18。
- [x] **热重载原子性：不需要回滚，因为失败即不应用**：`Reload` 先 `rebind()` 绑全部新端口、全部成功才换状态（否则 `ErrReloadBind`），配置加载失败时 reloader 根本不会被调用——旧配置一直在线。真机证据 s10：坏配置 -> 400 且旧规则继续命中；**新端口被外部占用 -> 409 且规则集一字未动**（10061/10062 两个端口照旧触发）；释放端口后同一次 reload -> 200，新端口开始服务、离开集合的 10062 关闭。

---

## P5 安装 · 覆盖升级 · 卸载 · 状态查看（docs/install-design.md）

- [x] 设计定稿文档 `docs/install-design.md`（13 节：目标与非目标 / 交付形态与输出约定 / 安装台账 / 安装报告 / 三个查询子命令 / 探测决策矩阵 / 覆盖安装与升级 / 卸载 / 各平台差异与文件清单 / 验证与证据强度 / 包管理器 / 落地顺序 / 未决项）
- [ ] `sol paths` 子命令（二进制真实路径 + 生效配置与来源 + 候选路径命中项 + 日志目的地 + 台账路径；`--json`）
- [ ] `sol config check` 子命令（加载 + 全部启动期校验 + 每条问题的修法；`--json`；升级预检复用）
- [ ] `sol status` 子命令（**默认入口**：版本 / 二进制 sha256 与台账比对 / 安装方式 / 服务线索（`INVOCATION_ID`、`XPC_SERVICE_NAME`）/ 配置有效性 / 监听端口与权限告警；未纳管时也输出；退出码 0/1；`--json`）
- [ ] `sol listen` 启动日志补配置来源行（`msg="configuration" path=... source=...`）
- [ ] 台账 / 报告契约：`install.json` schema + `install.log` 格式 + 防漂移校验（报告值必须来自现场：重算 sha256、真跑 `--version`；不打印配置内容与密钥）+ 默认只给**简短**报告（文件清单 + 校验 + 接下来），完整动作清单写进 `install.log`
- [ ] 本机安装配置（§6.1，**由安装脚本生成、用户可改**）：固定位置（Linux/macOS `~/.config/sol/install.yaml`、Windows `%APPDATA%\sol\install.yaml`；sudo 时写 `$SUDO_USER` 的家目录而不是 `/root`）+ **内容只有 `run.args`**（服务类型 / 单元路径 / CAP / 防火墙工具按平台推导，不进文件）+ **默认不覆盖已有**（问答里选 `r` 才重写）+ 未知键报错 + `run.args` 为列表并按真实 flag 集合校验 + 示例 `example/install-example.yaml`
- [ ] 二进制来源（§6.2）：**就地发现优先**（脚本旁 `./sol` / 当前目录 / `$PATH`），找不到才回退下载（OS/arch 自动 + 校验）；不因"网上有新版"去动用户放的二进制
- [ ] `scripts/install.sh`：探测（二进制发现 → 版本/哈希与台账比对 → `run.args` 与服务定义比对 → 异源提醒 → PATH 多命中只报告 → 无台账就地接管）→ **生成配置 → 展示并确认**（答 n = 只生成不执行，文件保留）→ 执行（配置预检不通过不覆盖 + 原子替换留 `sol.bak` + 重新生成服务定义 + 回读运行中版本）+ **零命令行参数**（未安装：`执行吗？[Y/n]`；已安装：`回车=按配置应用 / u=卸载 / r=重新生成 / n=退出`；卸载再问一句"配置文件也删吗？[y/N]"；非交互环境只生成不执行；CI 用管道喂答案 `printf 'y\n' | bash install.sh`）+ `flock`
- [ ] `scripts/install.ps1`：`C:\ProgramData\sol` + 生成 `%APPDATA%\sol\install.yaml`（只有 `run.args`）并确认——**零参数**，交互问答与 Linux 版一致（装不装服务现场问，默认沿用台账）；替换前先 `schtasks /End`（运行中 exe 有文件锁）
- [ ] 三平台产物模板（§9.1/§9.2）：systemd unit / launchd plist / 计划任务三条注册命令 + `scripts/install.cmd`（Windows 双击入口，内部按对的执行策略调 ps1）+ macOS quarantine 处理（`xattr -d com.apple.quarantine`）+ Windows 上必须 `logging.output: file`（计划任务不收集 stdout）+ 各平台"首次进入方式"一行（§9.2）
- [ ] `.github/workflows/install-smoke.yml`：三平台 matrix 真跑安装脚本 + 注册后确认服务真的起来（把 macOS / Windows 从"未验证"提到"有 CI 证据"）
- [ ] scoop + winget 清单（schema 校验；winget 在 CI 里装不了，只能标 schema 级证据）
- [ ] Hermes skill `sol-install` + `references/{linux,macos,windows}.md`（决策树：先判断此前是怎么装的，再选路径；装完给摘要，证据取自 `sol status --json` 与台账）
- [ ] README 一行安装（替换现有 5 段复制粘贴）+ 修正 Windows 那段 `move sol.exe C:\Windows\System32` + 中英文同步（代码块逐字节一致）
- [ ] （可选，需对应实机或明确标"未验证"）brew formula / AUR

## P5 未决项

- 服务生命周期是否下沉为 `sol service {install,uninstall,status}`（Go 侧跨平台，取代脚本里三套服务管理器逻辑；安装、升级、卸载、状态四处共用）——用户入口仍是安装脚本与 `sol status`。
- `sol status` 是否解析日志尾部推断"最近一次动作"——当前不做（不为好看去解析日志）。
- 免特权探测"端口是否真的在监听"——当前只列配置端口 + 提示用 `--dry-run` 验证。