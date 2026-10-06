# SoL - Shutdown on LAN（局域网关机）

[English](README.md) | **简体中文**

SoL 是一个监听 Wake-on-LAN 魔法包的服务：当收到的包命中配置好的规则时，就触发一个动作——
关机、重启、睡眠、执行命令、调用 webhook，或这些动作的有序组合。它是 Wake-on-LAN 的反向用法：
包是触发信号，"收到包的那台机器"负责执行。

规则可以按端口、目标 MAC、网卡、来源网段和包内容匹配，所以一个实例就能同时服务多个发送方、
多张网卡和多种语义。

## Inspiration

项目的灵感来自 Habr 上的文章 ["Выключаем компьютер через Wake-on-Lan"](https://habr.com/ru/articles/816765/)：
它演示了如何把 Wake-on-LAN 包改造成"关机"而不是"唤醒"。

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
| `http` | 调用 webhook（method、url、headers、body、timeout、retries；目的地受 `security.url_allowlist` 限制） |
| `sequence` | 把上面这些按顺序串成一个动作（某一步失败不会跳过它后面的步骤） |
| `wol.send` | 唤醒另一台机器：向固定目标发魔法包（`mac`，可选 `broadcast`、`port`、`secure_on`、`repeat`、`interval`） |
| `remote:<id>` | 白名单里的远端命令，注册成普通动作 |

sol 也能唤醒**别的**机器。`wol.send` 向固定目标发魔法包（`mac` 是唯一必填项；`broadcast` 默认
`255.255.255.255`，`port` 默认 9，`repeat` 默认 1，`interval` 默认 100ms）。可以由规则触发、作为
`sequence` 的一步，或在控制面用 `POST /v1/actions/wake-nas` 触发。目标 MAC 只能来自配置——sol
绝不会从触发包里取——并且它和其它动作一样走 cooldown、限流、dry-run 与审计日志。

信任阶梯见设计文档 §20：只记日志 < 出站唤醒 < 睡眠/锁屏 < 关机/重启 < 自定义命令 < 出站 HTTP <
远端原始命令（默认关闭）。

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
# yaml-language-server: $schema=https://raw.githubusercontent.com/bavix/sol/master/schema/sol.schema.json
```

Schema 与加载器严格一致——未知字段、类型不对、枚举值超出范围都会被拒；只要配置字段和 schema
对不上，测试就会失败。

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

两个互不干扰的限制保护机器不被广播风暴打穿：

- `security.cooldown` / `security.cooldowns.<动作名>` —— 按动作：**同一个**动作两次执行之间的最小
  间隔。
- `security.rate_limit` / `security.rate_burst` —— 全局：跨**所有**动作与所有触发源
  （包、`POST /v1/actions/{name}`、远端命令）的令牌桶。
- 以及无需任何配置的"执行中重入护栏"：同一个"要跑的活"还在执行时，再次触发会被拒绝而不是起第二个
  实例。身份 = 动作名 + 它实际要做什么（已校验的远端参数，或裸 shell 的命令行），所以
  `remote:backup target=home` 不会抑制 `target=work`。两个并发请求打同一个长动作，靠的就是它；
  而纯粹的包突发归 `security.cooldown` 管，因为包路径是一个一个处理的。

被抑制的动作会记日志（`action suppressed by cooldown` / `by rate limit` / `already running`，
有重试时间就带上），计入 `/v1/status`（`suppressed`、`rate_limited`、`inflight`）与 `/metrics`
（`sol_suppressed_total`、`sol_rate_limited_total`、`sol_inflight_total`），在控制面回 **429**。
配了限流时 `GET /v1/status` 也会回显当前的 `rate_limit`。

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
enp6s0           physical  up      58:11:22:bc:78:66  192.168.0.120  yes
wlp5s0           physical  down    0a:e8:9e:0f:d3:8d  -              no
docker0          virtual   up      02:42:4e:d9:8c:14  172.17.0.1     no
```

`sol ifaces --json` 输出同样的列表，供脚本使用。

## Installation

### Quick Install

按平台与架构下载最新 release：

**Linux AMD64：**
```bash
curl -L https://github.com/bavix/sol/releases/download/v0.0.2/sol-v0.0.2-linux-amd64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**Linux ARM64：**
```bash
curl -L https://github.com/bavix/sol/releases/download/v0.0.2/sol-v0.0.2-linux-arm64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**macOS Intel：**
```bash
curl -L https://github.com/bavix/sol/releases/download/v0.0.2/sol-v0.0.2-darwin-amd64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**macOS Apple Silicon：**
```bash
curl -L https://github.com/bavix/sol/releases/download/v0.0.2/sol-v0.0.2-darwin-arm64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**Windows (PowerShell)：**
```powershell
Invoke-WebRequest -Uri "https://github.com/bavix/sol/releases/download/v0.0.2/sol-v0.0.2-windows-amd64.zip" -OutFile "sol.zip"
Expand-Archive -Path "sol.zip" -DestinationPath "." -Force
move sol.exe C:\Windows\System32\sol.exe
```

> v0.0.2 早于配置文件、额外动作和端口 9 的变更；升级既有安装前请先读
> [CHANGELOG](CHANGELOG.md)。

### Build from source

```bash
make build          # or: go build .
make test
make lint
```

`make build` 用 `git describe` 打上版本号，`make build-static` 产出发布流水线那种形态（静态、
strip）。裸 `go build` 不需要打标：工具链自己会嵌入一个点明源码树的伪版本
（`v0.0.0-<时间戳>-<提交>`）以及提交号，`go install ...@v1.2.3` 则嵌入那个 tag。这些信息在
`-s -w -trimpath` 之后依然在，所以发布的二进制仍然说得出自己是谁。

### Verify Installation

```bash
sol --version
sol --help
sol listen --help
sol ifaces
```

## Systemd Service Setup

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

### Service Configuration Notes

- `After=network-online.target` 保证服务在网络就绪之后才启动。
- `Restart=always` 在崩溃后自动拉起。
- 配置放 `/etc/sol/sol.yaml`，密钥放环境变量或配置引用的 0600 文件——绝不写进 YAML 本身。
- unit 只有在用特权端口或需要 `exec` 降权时才需要 root；其它情况下"专用用户 +
  `CAP_NET_BIND_SERVICE`"是更好的默认。
- 用多端口时重复写 `--port`（或在配置文件里列 `rules`）。

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

## License

见 [LICENSE](LICENSE)。