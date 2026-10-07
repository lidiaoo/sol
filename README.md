# SoL - Shutdown on LAN

**English** | [简体中文](README.zh-CN.md)

SoL is a service that listens for Wake-on-LAN magic packets and triggers an action when a
packet matches a configured rule: shut down, reboot, sleep, run a command, call a webhook —
or any ordered combination of those. It is the reverse of Wake-on-LAN: the packet is the
trigger, the machine that receives it acts.

Rules can match on the port, the target MAC address, the interface, the source network and
the packet payload, so one instance can serve several senders, several network cards and
several meanings at once.

## Inspiration

This project was inspired by the article ["Выключаем компьютер через Wake-on-Lan"](https://habr.com/ru/articles/816765/)
on Habr, which demonstrates how to repurpose Wake-on-LAN packets for shutdown functionality
instead of wake-up.

## Description

The sol service is built with the [cobra](https://github.com/spf13/cobra) CLI framework and
the Go standard library. The only other dependencies are `yaml.v3` (configuration) and
`testify` (tests).

The full design — routing model, configuration schema, security model, and the
implemented/planned status of every feature with its real-machine smoke evidence — lives in
[docs/routing-design.md](docs/routing-design.md). Progress is tracked in [TODO.md](TODO.md).

### Listen

`sol listen` listens for Wake-on-LAN magic packets on the configured interfaces (or on every
eligible interface by default) and triggers an action when a packet matches a rule. Rules
come from the configuration file, unless `--port` is given, which takes over the whole rule
set.

### Protocol

The service listens for Wake-on-LAN magic packets on UDP ports. A magic packet is
6 × `0xFF` followed by the target MAC address repeated 16 times (102 bytes); with SecureOn
configured, a 6-byte password follows (108 bytes).

- Ports **7** and **9** are reserved for plain WOL. They only accept a bare magic packet with
  no payload and can only run `noop` — anything else fails at start-up. `--allow-reserved-actions`
  or `security.allow_reserved_port_actions: true` restores the old behaviour, on request.
- On every other port, a rule may additionally match the packet payload (`content`: suffix,
  prefix or any), the source CIDR and the source MAC.

### Supported actions

| Type | What it does |
| --- | --- |
| `noop` | Only logs the match (the default for the reserved ports) |
| `power.sleep` | Suspends the machine |
| `power.shutdown` | Shuts the machine down |
| `power.reboot` | Reboots the machine |
| `exec` | Runs a configured command (argv, no shell by default; optional `shell: true`, command allow-list, timeout, workdir, env, and `user`/`group` privilege drop) |
| `http` | Calls a webhook (method, url, headers, body, timeout, retries, proxy; destination restricted by `security.url_allowlist`) |
| `sequence` | Runs an ordered list of the above as one action (a failing step never skips the ones behind it) |
| `wol.send` | Wakes another machine: sends a magic packet to a fixed target (`mac`, optional `broadcast`, `port`, `secure_on`, `repeat`, `interval`) |
| `remote:<id>` | A whitelisted remote command, registered as an ordinary action |

sol can also wake *another* machine. `wol.send` sends a magic packet to a fixed target
(`mac` is the only required parameter; `broadcast` defaults to `255.255.255.255`, `port` to 9,
`repeat` to 1 and `interval` to 100ms). Trigger it from a rule, from a sequence step, or with
`POST /v1/actions/wake-nas` on the control plane. The target MAC must come from the
configuration — sol never takes it from the packet that triggered the action — and the action
goes through the same cooldown, rate limit, dry-run and audit path as every other one.

The trust ladder is documented in §20 of the design: log only < outbound wake-up < sleep/lock <
shutdown/reboot < custom command < outbound HTTP < raw remote command (off by default).

### Platform support

The listening side is the same everywhere: one UDP socket per port, bound to `0.0.0.0`, and the
same rule engine behind it. What differs per platform is the power actions and the privilege rules.

| | Linux | macOS | Windows |
| --- | --- | --- | --- |
| `power.shutdown` | `shutdown -h now` | `shutdown -h now` | `shutdown -s -t 0 -f` |
| `power.reboot` | `shutdown -r now` | `shutdown -r now` | `shutdown -r -t 0 -f` |
| `power.sleep` | `systemctl suspend` | `pmset sleepnow` | `rundll32 powrprof.dll,SetSuspendState 0,1,0` |
| reserved ports 7/9, anything below 1024 | root or `CAP_NET_BIND_SERVICE` | root | no privileged ports |
| `exec` with `user:`/`group:` | yes (needs root) | yes (needs root) | refused: `exec user/group requires root` is unix only |
| `exec`, `http`, `sequence`, `wol.send`, control plane, reload, guards | yes | yes | yes |

Other systems (a BSD, for instance) build and listen; a power action there fails with
`unsupported operating system`, which is when an `exec` action is the way to do it.

One thing worth saying plainly: sol acts on the machine it runs on, and only while it runs. It is a
shutdown-on-LAN receiver, not something that wakes a sleeping box.

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

| Flag | Meaning |
| --- | --- |
| `--config` | Configuration file to load (default: `$SOL_CONFIG`, then `/etc/sol/sol.yaml`, then `~/.config/sol/sol.yaml`) |
| `--iface` | Interface to match on; repeatable. Omit to auto-select every eligible interface (up, non-loopback, has a MAC, not virtual) |
| `--port` | UDP port to listen on, optionally with an action (`10010` or `10010:sleep`); repeatable. Ports 7 and 9 are reserved and always map to `noop`. Any `--port` takes over the whole rule set |
| `--default-action` | Action for ports given without one (`noop`\|`sleep`\|`shutdown`\|`reboot`, default `shutdown`) |
| `--allow-reserved-actions` | Allow non-`noop` actions on ports 7 and 9 (the old behaviour; it gives up the WoL interoperability that makes those ports worth reserving) |
| `--dry-run` | Log matching packets instead of executing the action |
| `--watch` | Poll the configuration file and reload it on change (e.g. `5s`); `0` disables it. Overrides `server.watch` |

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

Configuration discovery order: `--config`, then `$SOL_CONFIG`, then `/etc/sol/sol.yaml`,
then `~/.config/sol/sol.yaml`. Field-by-field reference: the "字段速查" section in
[docs/routing-design.md](docs/routing-design.md).

Editors with YAML support can complete and validate the file against the published JSON
Schema. Either point the editor at the local `schema/sol.schema.json`, or start the file with:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/lidiaoo/sol/master/schema/sol.schema.json
```

The schema mirrors the loader exactly — unknown keys, wrong types and the allowed enum values
are all rejected, and a test fails whenever a configuration field and the schema disagree.

### Remote commands (off by default)

The remote command channel lets an authenticated sender invoke a whitelisted command over
UDP (a magic packet followed by `<id>[:k=v,...]` and an HMAC-SHA256 tag) or HTTP
(`POST /v1/commands/{id}`). It is disabled unless `security.allow_remote_commands: true` and
an HMAC key is configured; the key never lives in YAML:

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

Every command becomes an ordinary action named `remote:<id>`, so cooldowns, dry-run, audit
logging and `POST /v1/actions/remote:<id>` all apply to it.

### Authenticating packets (off by default)

A content token is plaintext: anyone who can reach the port can trigger the rule. With
`security.packet_auth` a rule can require an HMAC tag (truncated SHA-256, 8 bytes) over the
whole packet, and `wol.send` can produce it:

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

`window` bounds replays: the packet then ends with an 8-byte unix-second stamp in front of the
tag, and the stamp is inside the tag's coverage, so it cannot be edited.

The receiver accepts a stamp no older (and no newer) than the window, and accepts each tag only
once inside it — a captured packet sent a second time is refused as a replay, and one kept for
longer than the window is refused as stale. Both refusals are logged with their reason
(`authenticated packet refused reason=replay|stale`), counted in `/v1/status` (`replayed`, with a
`replay_reasons` breakdown) and exported as `sol_replayed_total` on `/metrics`, and the seen-tag
cache is bounded: when it is full, new packets are refused rather than evicting an entry that
could still be replayed. Both
ends must agree: a receiver with a window refuses the plain tag-only layout, and `wol.send` emits
the stamp when its own instance sets `window` too (it needs the key and the window, not a rule).
A reload starts with an empty cache, so a packet seen just before a reload could be replayed once
inside the window; the window is the bound in every other case.

`match.auth: hmac` is per rule, so existing rules keep working. The tag covers every byte before
it (including a `secure_on` password), the content matcher sees the payload without the tag, and
the audit log records `authenticated=true|false` for every match. A rule that requires
authentication while no key is configured, a reserved port that requires it, and `sign: true`
without a key are all refused at start-up instead of failing silently. It proves the packet came
from someone holding the key. On its own it does not stop a replay of a captured packet: set
`packet_auth.window` (below) or rely on cooldowns and the rate limit to bound that.

### SecureOn per interface or per rule

`security.secure_on` sets the password (6 bytes) every rule expects. An interface block or a
single rule can require a different one, which is how two targets on one port stay separate:

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

Both rules listen on port 10020 and one of them inherits the global password while the other
declares its own: they can never match the same packet, so this is not a conflict.

- A rule without `secure_on` inherits its block's, and a rule or block without one inherits
  `security.secure_on`; with none of them set no password is required.
- `secure_on: ""` is not the same as leaving it out: it opts *out* of the default and requires a
  packet without a password.
- Ports 7 and 9 always take plain magic packets, so a password there is refused at startup.
- Two rules that differ only in their password are not a conflict: a packet carries exactly one of
  them. A packet whose password matches nothing is read as a plain magic packet (the six bytes
  become content), which the default `content: none` rejects - so a wrong password never opens a
  rule that asks for one.

### Raw shell (off by default)

`security.allow_raw_shell: true` lets a remote sender run an arbitrary shell command through
`/bin/sh -c`. It is the only place in sol that uses a shell, it is off unless you ask for it, and
it is refused unless you also provide its own HMAC key and a dedicated port that the command
channel does not use:

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

Both channels can also refuse replays: `remote_command_auth.window` and
`raw_shell_auth.window` (e.g. `60s`) make the sender stamp each command segment, the receiver
accept a stamp only inside that window and each tag only once — the same code that guards
authenticated packets (`security.packet_auth.window`). Off unless you set it, and both ends have
to agree; `docs/routing-design.md` §21.3/§21.4 has the wire format and §19.17 the details.

Every command is checked against `raw_shell_allowlist` when one is set (the whole command line
must match, so `^echo .*$` cannot be reached by `id; echo hi`), the sender must be inside
`raw_shell_src_cidrs` when that is set, and every command is logged in full along with its exit
code. Dry-run, cooldowns, the rate limit, the timeout and the privilege drop all apply, and the
start-up log carries a warning naming the ports it opened. `raw:shell` is an internal label for
the guards and the audit log — it is not an action name you can put in a rule. An enabled channel
with a bad key, an empty command, a reserved port, a port shared with the command channel or an
uncompilable allowlist entry is refused at start-up.

### Control plane

`server.http.enabled: true` starts an HTTP control plane on `127.0.0.1:8080` by default with
mandatory authentication (`bearer` with `${SOL_TOKEN}`, `basic`, or `mtls`). It never binds a
public address unless you ask for one, and there is no unauthenticated mode.

The token itself comes from the environment (`export SOL_TOKEN=...`) or a 0600 file; it is never
written in the YAML, and a reference to an unset variable fails the start-up rather than running
with an empty secret. References inside comments are ignored, so commenting out an optional
line — like the `secure_on` above — always leaves a loadable configuration.

Endpoints: `GET /healthz`, `GET /v1/status`, `GET /v1/rules`, `GET /v1/interfaces`,
`GET /metrics`, `POST /v1/actions/{name}`, `POST /v1/commands/{id}`, `POST /v1/exec`, `POST /v1/reload`.

`GET /v1/status` reports the build it is answering from - `"version"` (the stamp, or the
toolchain's pseudo-version) and `"revision"` (the commit, with a `-dirty` suffix when the tree
had uncommitted changes) - and `/metrics` exposes the same pair as
`sol_build_info{version="...",revision="..."} 1`. Two processes with different configs are then
distinguishable from a bug report alone.

### Audit log destination

The audit trail goes to stderr by default, so a service manager owns it (`journalctl -u sol`).
`logging.output` moves it: `stdout`, or `file` together with `logging.file` (`output: file` without
a path is a start-up error, and so is a path without `output: file`). A log file is opened
append-only with mode 0600 — it names source addresses, the action that ran and, for `exec` and the
raw shell, the command line — and it is never rotated: pair it with `logrotate`, or leave the
default and let journald do it. The destination is read at start-up only; `logging.level` is the
part that reloads. `SOL_LOG_OUTPUT` and `SOL_LOG_FILE` override both fields.

### Reloading

Three paths, one implementation: `SIGHUP`, `POST /v1/reload`, and `server.watch` / `--watch`
(polling the configuration file). Rules, actions, cooldowns, the rate limit, the remote command
channel, `dry_run` and the log level are swapped atomically per packet, and the **listening
port set moves with them**: ports the new configuration adds are bound before anything is closed,
so a reload that cannot bind one of them (HTTP 409) changes nothing at all, while a successful
one starts reading the new ports immediately and closes the ports that left the set. A
configuration that fails to load leaves the running one untouched, and an unchanged cooldown or
rate limit keeps its running state, so a reload cannot be used to refresh a guard by accident.
The interface list is refreshed too, so `/v1/interfaces` and the audit log describe the machine as
it is now.

### Guards

Three limits protect the machine from a broadcast storm, and the first two are on by default:

- `security.cooldown` / `security.cooldowns.<action>` — per action: the minimum interval between
  two executions of the *same* action. The destructive power actions (`power.sleep`,
  `power.shutdown`, `power.reboot`) default to **5s**: a wake-on-LAN sender repeats its packet for
  reliability — three copies or more — and every copy arrives as its own trigger, so honouring the
  second one would undo the first. Override one with `security.cooldowns.<action>`, switch it off
  with `0s`.
- `security.settle` (default **5s**) — the window right after sol started (a boot, a reboot, a
  service restart) and right after the machine came back from suspend, during which the actions in
  `security.settle_actions` (`power.sleep`, `power.shutdown`, `power.reboot` by default) are
  refused. That is exactly where the rest of a magic-packet burst lands — the NIC wakes the machine
  for the next copy — and this is the only guard that can break that ping-pong, because a machine
  going down takes the process and its in-memory windows with it. `security.settle: 0` disables it.
- `security.rate_limit` / `security.rate_burst` — global: a token bucket capping executions
  across *every* action and every trigger (packets, `POST /v1/actions/{name}`, remote commands).
- and, without any configuration, the in-flight guard: a trigger that arrives while the *same*
  run is still going is refused rather than started a second time. The identity is the action plus
  what it would actually do (the validated remote arguments, or the raw shell command), so
  `remote:backup target=home` never suppresses `target=work`. This is what bounds two concurrent
  requests for the same long action; a plain burst of packets is `security.cooldown`'s job, since
  the packet path handles one packet at a time.

Both defaults are configuration, not policy. To switch the protection off:

```yaml
security:
  settle: 0                     # no window after a boot or a resume
  cooldowns:
    power.sleep: 0s             # no built-in window for the power actions
    power.shutdown: 0s
    power.reboot: 0s
```

A suppressed action is logged (`action suppressed by cooldown` / `by rate limit` / `the machine
just started or woke up` / `already running`) with the retry delay where there is one, counted in
`/v1/status` (`suppressed`, `rate_limited`, `settle_skipped`, `inflight`) and `/metrics`
(`sol_suppressed_total`, `sol_rate_limited_total`, `sol_settle_skipped_total`,
`sol_inflight_total`), and answered with **429** on the control plane. Start-up logs the
configured windows (`action cooldown`, `settle window`), and `GET /v1/status` reports the live
`rate_limit` when one is configured.

## Ports and privileges

- Ports **7, 9 and anything below 1024** are privileged: without root the socket bind fails
  with `permission denied`. Run as root, or grant the capability — the systemd unit below
  shows `AmbientCapabilities=CAP_NET_BIND_SERVICE` with a dedicated user.
- If you only use high ports (≥1024), no privilege is needed. High ports are the recommended
  default: they keep the reserved WOL ports meaningful and need no capabilities.
- `exec` actions with `user`/`group` require sol to run as root, otherwise start-up fails with
  `ErrNotRoot` — the drop is never silently skipped. Root is the only accepted setup: the check
  is `geteuid() == 0`, so granting just `CAP_SETUID`/`CAP_SETGID` to a non-root process is
  refused rather than half-applied. The command then runs with the target account's groups,
  never with sol's inherited ones.
- The control plane should stay on `127.0.0.1` unless TLS and mTLS are configured.

## Choosing interfaces

`sol ifaces` shows every interface with its MAC/IPv4 and whether auto mode would select it —
useful before trusting an `--iface`-less startup:

```
NAME             TYPE      STATUS  MAC                IPV4           AUTO
lo               loopback  up                         127.0.0.1      no
enp6s0           physical  up      00:11:22:33:44:55  192.168.0.120  yes
wlp5s0           physical  down    0a:e8:9e:0f:d3:8d  -              yes
docker0          virtual   up      02:42:4e:d9:8c:14  172.17.0.1     no
```

`sol ifaces --json` prints the same list for scripts.

AUTO is about **identity, not availability**: a NIC that is down right now is still this machine
(`wlp5s0` above), because it can come up later - and its MAC can even change when it does (a
wireless card that reports a randomized placeholder while down switches to its real address when it
comes up). The listener therefore re-reads the interface list while it runs: a packet that matches
no rule triggers one re-read (at most once a second), and a 30-second poll covers a machine that has
received nothing at all. Changes are audited as `interface set changed` with the interfaces that
appeared or went away. Additions take effect within a second of the next packet; a NIC that *goes
away* keeps matching until the next unmatched packet or the next poll, so at most 30 seconds -
`SIGHUP` (or `--watch`) applies the current list immediately. Explicit `interfaces: [x]` names are
still validated at start-up: a name that is not there is a typo, and it is refused.

## Installation

### Quick Install

One script, no arguments. It works out the situation, shows you what it would do, and asks before it
touches anything.

**Linux / macOS:**
```console
curl -fsSL https://github.com/lidiaoo/sol/releases/latest/download/install.sh -o install.sh && sh install.sh
```

**Windows** (PowerShell, or double-click the shipped `install.cmd`):
```console
irm https://github.com/lidiaoo/sol/releases/latest/download/install.ps1 -OutFile install.ps1; powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1
```

It uses the `sol` you already have - next to the script, in the current directory, or on `PATH` - and
never replaces it with a download. In the directory you run it from it writes `install.yaml`, whose only
content is how to run sol, and it makes sure a working configuration exists: a plain magic packet to port
11 shuts the machine down, `"reboot"` after the magic packet to 12 reboots it, `"sleep"` to 10 sleeps it -
a service with no rules refuses to start, so the script writes one that runs. It also spells out the two
repeat guards in that file - `security.settle` and the per-action cooldowns, 5s each - so you can see the
defaults, and see how to switch them off, without reading any docs. Where that configuration
lives differs by platform: on Linux and macOS it is `sol.yaml` in the directory you ran the script from;
**on Windows it goes next to the installed `sol.exe`** (`C:\ProgramData\sol\sol.yaml`), because the
scheduled task runs as `SYSTEM` at boot and should not depend on a project directory that can move. If you
already edited a `sol.yaml` there, the script copies it over rather than throwing it away. Edit the
configuration and re-run the script to change it. Ports below 1024 need root on Linux/macOS, which the
script sorts out before it touches anything - on a user-level install without root, move them to 1024 or
above. It prints the current situation and where the configuration lives, then asks two questions:
install as a service, and go ahead. Nothing changes until you say yes.

Privileges: it does not run as root from the start. Before any step that needs it (writing
`/usr/local/bin`, registering the service), it re-runs itself through `sudo` on Linux/macOS, or
relaunches under UAC on Windows, carrying your answers across so you are not asked twice. Decline and
it stops with an explanation rather than half-installing.

Re-running the script is how you see the current state, change how sol runs (edit the file, run it
again), or uninstall (choose `2`; it stops the running sol first, and asks before deleting your
configuration). Every action it takes
is appended to `install.log`, with the equivalent command, next to the ledger `install.json`.

Where things go: the binary in `/usr/local/bin/sol` (`C:\ProgramData\sol\sol.exe`), the ledger and
history in `/usr/local/share/sol/` (`C:\ProgramData\sol\`), the configuration in `sol.yaml` in the
directory you ran the script from (`C:\ProgramData\sol\sol.yaml` - next to the binary, where the
scheduled task reads it), and - only if you asked for the service - a systemd unit, a launchd plist, or a
scheduled task. A configuration you already have (or any other one you point `run.args` at) and your logs
are never touched - the script only creates one when there is none.

The script is Linux/macOS/Windows aware but has been exercised on Linux; the Windows and macOS paths
are marked as such in [docs/install-design.md](docs/install-design.md) until CI covers them.

### Download the release directly

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

> Upstream's last tagged release, v0.0.2, predates the configuration file, the extra actions and the port-9 change; read the
> [CHANGELOG](CHANGELOG.md) before upgrading an existing installation.

**Building the release packages yourself**: one command, the same on Linux, macOS and Windows (Git Bash) — see
[Building the release packages yourself](#building-the-release-packages-yourself) below. Go cross-compiles, so a
single machine produces all six; CI publishes the same set through goreleaser (same names, same contents).

### Building the release packages yourself

`scripts/release.sh` writes the packages CI publishes into `dist/`. It runs on any one of the three
platforms — Go cross-compiles, so you do not need one machine per target:

```bash
sh scripts/release.sh v0.1.0
```

```text
dist/sol-v0.1.0-linux-amd64.tar.gz      dist/sol-v0.1.0-windows-amd64.zip
dist/sol-v0.1.0-linux-arm64.tar.gz      dist/sol-v0.1.0-windows-arm64.zip
dist/sol-v0.1.0-darwin-amd64.tar.gz     dist/checksums.txt
dist/sol-v0.1.0-darwin-arm64.tar.gz     dist/install.sh  dist/install.ps1  dist/install.cmd
```

Per platform — the command is identical; only the shell you type it in differs:

| Host | Run it | Notes |
| --- | --- | --- |
| Linux | `sh scripts/release.sh v0.1.0` | needs `git` and `go`; `zip` only when you build the Windows targets |
| macOS | `sh scripts/release.sh v0.1.0` | same; `file` gives the extra format/architecture check (optional) |
| Windows | Git Bash: `cd /e/Common/Project/GolandProjects/sol && sh scripts/release.sh v0.1.0` | stock Git for Windows is enough |

On Windows without `zip`, the `.zip` archives are written through PowerShell's `Compress-Archive`; without
`file`, the format/architecture check is skipped with a note. Neither blocks the build. `make` has no target
for this on purpose — packaging and building are separate jobs.

Every archive unpacks to a single directory — the run directory: the binary, the three installer scripts,
both READMEs, CHANGELOG, LICENSE, `sol-example.yaml`, `sol.schema.json` and `skills/` (the Hermes
`sol-install` skill). Unpack it, run the installer, done. Nothing else to configure.

Options:

- `sh scripts/release.sh` with no version takes `git describe --tags --always` and refuses a dirty tree —
  tracked changes only, so untracked `dist/` and `.idea/` do not count; `--allow-dirty` overrides.
- `--platforms "windows/amd64 linux/arm64"` builds a subset instead of all six.
- `-h` prints the usage.

It refuses to ship something wrong: each binary's format and architecture is verified, the packaged
installers must be byte-identical to the ones in the repo, the skills directory must be complete, and on
the host's own platform it runs the freshly built binary with `--version`.

CI produces the same set through goreleaser whenever a GitHub **release** is created
(`.github/workflows/release.yaml`):

```bash
git tag v0.1.0 && git push origin v0.1.0
gh release create v0.1.0 dist/* --title v0.1.0      # or upload the files from dist/ by hand
```

### Build from source

```bash
make build          # or: go build .
make test
make lint
```

`make build` stamps the version with `git describe`, and `make build-static` produces the shape
the release pipeline ships (static, stripped). A plain `go build` needs no stamp: the toolchain
embeds a pseudo-version naming the tree (`v0.0.0-<timestamp>-<commit>`) plus the commit, and
`go install ...@v1.2.3` embeds that tag. Everything survives `-s -w -trimpath`, so the released
binary can still say what it is.

### Using the installer (no arguments, numbered menu)

Install, upgrade, check status and uninstall all go through **one script with one
entry point**, and you never pass it any arguments:

| Platform | How to run it |
| --- | --- |
| Linux / macOS | `./install.sh` (or `sh install.sh`) |
| Windows | double-click the shipped `install.cmd`, or `powershell -ExecutionPolicy Bypass -File .\install.ps1` |

It **reports what it found first, then asks** — nothing is changed until you answer.

#### What each line of the status block means

- `二进制` / `版本`: the copy in the directory you ran the script from; `PATH 上的 sol` is what the system already has.
- `已安装` / `台账`: the installer's own record (`/usr/local/share/sol/install.json`; Windows `C:\ProgramData\sol\install.json`). "last time did not finish" means: run it again and it will.
- `服务`: whether the systemd unit / launchd job / scheduled task exists. On Windows an extra `启动` line prints the command the task actually runs (executable + arguments), so you do not have to dig through Task Scheduler.
- `日志`: where the service log is — Linux `journalctl -u sol.service -f`, macOS `/usr/local/var/log/sol.log`, Windows `C:\ProgramData\sol\sol.log`.
- `进程`: whether sol is running, matched by **executable path**, never by process name. Without elevation it can only match by name and says so explicitly.
- `权限`: when you are not an administrator, this says when privileges will be needed (a Unix re-exec through sudo; one UAC prompt on Windows).
- `运行配置`: the config the service actually reads. Edit that one; the copy in your exec directory is only the input.

#### The menu (Enter = 1)

    1  Apply the install config (install or upgrade per run.args)
    2  Uninstall (remove the service and everything the ledger says we created)
    3  Regenerate install.yaml
    4  Quit, change nothing

#### Afterwards

- **Change behaviour**: edit the runtime `sol.yaml` (ports, matching, actions), or `install.yaml`'s `run.args` (how it is started). Re-run and pick `1`.
- **Upgrade**: drop the new `sol` / `sol.exe` next to the script and pick `1`. It stops our own service first, swaps the binary, then starts it again — so it never trips over "address already in use".
- **Uninstall**: pick `2`. Service, binary and ledger go away; you are asked whether to keep `sol.yaml` / `install.yaml`.
- **Every step is logged**: `install.log` records each action with its equivalent command, so you can reproduce any of it by hand.

#### When a line does not make sense

- **`服务 没有（计划任务 X 不存在）` while the ledger says it was installed**: the task was deleted, or a "cleanup/optimizer" tool removed it. Re-run and pick `1` to create it again. (The script now verifies the task right after creating it, and reports a failure instead of claiming success.)
- **Windows `服务 … 看不清`**: no permission to list tasks. Re-run as administrator.
- **`预检 被拒绝`**: the port cannot be bound. Usually the previous copy is still running (the script stops it and retries); it can also be a Windows **excluded port range** (Hyper-V / WSL / Docker / VM) — use a higher port, or `net stop winnat && net start winnat` (until the next reboot).
- **No log file on Windows**: check the runtime config for `logging: { output: file, file: ... }`. A scheduled task runs windowless, so nothing collects stdout — the log must be written by sol itself. The installer adds that stanza when you have not set one, and sol writes `C:\ProgramData\sol\sol.log`.

- On Windows without administrator rights the service line says "读不到内容（计划任务 sol）：当前不是管理员，要管理员才看得到" (cannot read it -- needs administrator). That is not a failure: the scheduled task is registered as SYSTEM, so only administrators can read its definition, and an unelevated enumeration silently skips it. To see the whole picture, right-click `install.cmd` and choose "Run as administrator".
#### Trying it somewhere harmless

Set `SOL_INSTALL_ROOT=<dir>` before running: everything lands under that directory, it **never elevates**, and it **never touches** this machine's service manager, task scheduler or firewall. Our tests and CI run this way.

> With Hermes installed, the same rules (including the traps each platform actually bit us with) live in the `sol-install` skill: type `/sol-install`.

### Verify Installation

```bash
sol --version
sol --help
sol listen --help
sol ifaces
```

## Quick start

1. **See which interfaces will answer.** `sol ifaces` marks them `AUTO yes`: that is the machine's
   identity (its real NICs), not a snapshot of what happens to be up at that moment.

2. **Write a configuration.** `sol` looks for `/etc/sol/sol.yaml` first, then
   `~/.config/sol/sol.yaml` - the same two places on every platform, `~` being your home
   directory. `--config` (or `$SOL_CONFIG`) points it somewhere else, and an explicit path that
   does not exist is an error, not a silent fallback. With none of them it refuses to start
   (`no rules configured`) instead of listening for nothing.

   ```yaml
   version: 1
   rules:
     - match: { ports: [10010] }
       action: power.shutdown
   ```

3. **Try it without consequences.** `--dry-run` logs what would happen and performs nothing, which
   is also the quickest way to check that a packet is matched at all.

   ```bash
   sol listen --config /etc/sol/sol.yaml --dry-run
   ```

4. **Send a magic packet from another machine** and watch for
   `magic packet matched ... action=power.shutdown` - see [Testing your setup](#testing-your-setup).

5. **Install it as a service** so it survives a reboot and keeps running with nobody logged in - see
   [Running as a service](#running-as-a-service).

6. **Confirm what is running**: `sol --version`, `journalctl -u sol.service -f` (Linux),
   `tail -f /usr/local/var/log/sol.log` (macOS), `Get-Content -Wait C:\ProgramData\sol\sol.log`
   (Windows; the scheduled task runs `sol.exe` itself, and sol writes that file itself through the
   `logging` stanza in the runtime config), or `GET /v1/status` when the control plane is on. The state
   block and its completion report each print a line telling you where the log is.

Secrets never belong in the YAML: put them in environment variables (`SOL_TOKEN`, `SOL_CMD_KEY`,
`SOL_PACKET_KEY`) or in a file only the service account can read, and reference that from the
configuration.

## Testing your setup

On the machine that should react, start sol in the foreground with `--dry-run`:

```bash
sol listen --config /etc/sol/sol.yaml --dry-run
```

Then, from another machine on the same network, send one magic packet: six `0xff` bytes followed by
the target MAC repeated sixteen times, 102 bytes in total, and it has to start at offset 0. Use the
MAC that `sol ifaces` prints, replacing `00:11:22:33:44:55`, `192.168.0.120` and port `10010` with
your own values.

Linux and macOS, with the `wakeonlan` tool:

```bash
wakeonlan -i 192.168.0.120 -p 10010 00:11:22:33:44:55
```

Anywhere with Python 3 - no tools to install:

```bash
python3 -c "import socket; mac=bytes.fromhex('001122334455'); socket.socket(socket.AF_INET, socket.SOCK_DGRAM).sendto(b'\xff'*6+mac*16, ('192.168.0.120', 10010))"
```

Windows, in PowerShell:

```powershell
$mac = 0x00,0x11,0x22,0x33,0x44,0x55
$packet = [byte[]]((1..6 | ForEach-Object { 0xff }) + (1..16 | ForEach-Object { $mac }))
$udp = New-Object Net.Sockets.UdpClient
$null = $udp.Send($packet, $packet.Length, "192.168.0.120", 10010)
$udp.Close()
```

What to look for:

- in the log: `magic packet matched` with the port, the interface and the action, or
  `non-matching packet` with the length - a length other than 102 (108 with `secure_on`, plus 8 more
  with `auth: hmac`) means the payload is not a plain magic packet;
- in `/v1/status` when the control plane is on: `packets`, `matched` and the per-action counters;
- with `--dry-run`, the action line is logged and nothing is executed - that is the safe end of the
  test. Run it again without `--dry-run` when you trust the rule set.

## Running as a service

### Linux (systemd)

1. **Create the unit file**

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

   With `server.watch: 5s` in the configuration, editing `/etc/sol/sol.yaml` is enough to apply
   changes; alternatively declare `ExecReload=/bin/kill -HUP $MAINPID` and use
   `systemctl reload sol.service`.

2. **Reload, enable and start**

   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable --now sol.service
   sudo systemctl status sol.service
   ```

3. **View logs**

   ```bash
   journalctl -u sol.service -f
   ```

4. **Open the ports you use** if a firewall is running - the listeners are UDP:

   ```bash
   sudo ufw allow 10010/udp comment 'sol'
   ```

### macOS (launchd)

1. **Install the binary and a configuration**

   ```bash
   sudo cp sol /usr/local/bin/sol && sudo mkdir -p /usr/local/etc/sol
   sudo cp sol.yaml /usr/local/etc/sol/sol.yaml
   ```

2. **Create the launchd daemon**

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

3. **Load it and watch the log**

   ```bash
   sudo launchctl load -w /Library/LaunchDaemons/com.lidiaoo.sol.plist
   sudo launchctl list | grep sol
   tail -f /usr/local/var/log/sol.log
   ```

   `launchctl unload -w` stops it, and `RunAtLoad` brings it back after a reboot. A LaunchDaemon
   runs as root, which is what ports below 1024 need; to run as a dedicated user instead, add
   `UserName` to the plist and stay on high ports.

### Windows (Task Scheduler)

A plain `.exe` cannot be a Windows service, so the reliable low-friction option is Task Scheduler
with a machine-level environment variable for the token:

1. **Install to a stable path and start it at boot, as SYSTEM**

   ```powershell
   New-Item -ItemType Directory -Force -Path C:\ProgramData\sol | Out-Null
   Move-Item .\sol.exe C:\ProgramData\sol\sol.exe -Force
   # The task runs sol.exe itself: the task's action is just the exe and its arguments.
   # A task has no stdout to collect, so sol writes its own log file — add this to the config:
   #   logging: { output: file, file: 'C:\ProgramData\sol\sol.log' }
   schtasks /Create /TN sol /TR "\"C:\ProgramData\sol\sol.exe\" listen --config \"C:\ProgramData\sol\sol.yaml\"" /SC ONSTART /RU SYSTEM /RL HIGHEST /F
   schtasks /Run /TN sol
   ```

2. **Let the packets in** (listeners are UDP; Windows has no privileged ports, so 7 and 9 work too)

   ```powershell
   New-NetFirewallRule -DisplayName "sol (WoL)" -Direction Inbound -Protocol UDP -LocalPort 10010,7,9 -Action Allow
   ```

3. **Check it** - the task's history, or the log file the configuration names:

   ```powershell
   schtasks /Query /TN sol /V /FO LIST
   Get-Content C:\ProgramData\sol\audit.log -Wait
   ```

   The `SYSTEM` account reads machine environment variables, so a secret set once with
   `[Environment]::SetEnvironmentVariable('SOL_TOKEN','...','Machine')` is available to it. Keep in
   mind that machine variables are readable by administrators; a file ACL'd to `SYSTEM` plus a
   0600-style reference in the configuration is the stronger option.

### Service configuration notes

- `After=network-online.target` ensures the service starts after the network is fully online.
- `Restart=always` automatically restarts the service if it crashes.
- Keep the configuration in `/etc/sol/sol.yaml` and keep secrets in environment variables or
  0600 files referenced by the configuration - never in the YAML itself.
- A service gets its own home directory (root's for systemd and launchd, `SYSTEM`'s on Windows),
  so `~/.config/sol/sol.yaml` there is not yours: give the unit an absolute path with `--config`
  (as the examples do) or keep the file at `/etc/sol/sol.yaml`.
- The unit needs root only for privileged ports and for `exec` privilege drops; otherwise a
  dedicated user plus `CAP_NET_BIND_SERVICE` is the better default. On macOS the equivalent is
  dropping `UserName` into the plist; on Windows `exec` has no user/group drop at all.
- When using multiple ports, repeat `--port` (or list `rules` in the configuration file).
- The console output is the audit log: with `logging.output: file` it goes to
  `logging.file` instead, and it is never rotated - pair it with `logrotate` on Linux, `newsyslog`
  on macOS, or a size-limited task on Windows.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| `bind: permission denied` | ports 7/9 and anything below 1024: run as root or grant `CAP_NET_BIND_SERVICE` (a high port needs neither) |
| the packet arrives and nothing happens | `sol ifaces` (is that NIC `AUTO yes`?), the rule's `ports` and `mac`, and the log: `non-matching packet` names the port and the payload length |
| the log says `length=...` and skips it | a magic packet starts at offset 0 and is exactly 102 bytes (108 with `secure_on`, 8 more with `auth: hmac`); a payload on a reserved port is refused by design |
| `--port 9` no longer shuts down | 7 and 9 are reserved for plain WOL and mean `noop`; move the action to a high port or pass `--allow-reserved-actions` |
| `exec user/group requires root` | `user:`/`group:` drops need root, and do not exist on Windows at all |
| a wireless or hot-plugged NIC is not matched | the identity set is re-read while sol runs (`interface set changed` in the log); `SIGHUP` applies the current list immediately |
| the control plane answers 401 | it binds `127.0.0.1` and always authenticates: export `SOL_TOKEN` and send `Authorization: Bearer ...` |
| a reload seems to do nothing | a rejected reload keeps the previous configuration on purpose; the error is in the log (`POST /v1/reload` reports it too) |

## Security Notes

Wake-on-LAN is unauthenticated broadcast: anything that can reach a listening port can send a
valid magic packet. SoL therefore

- never exceeds `noop` on the reserved ports unless you explicitly ask for it,
- matches strictly by default (a rule only fires on the payload content it names),
- offers `src_cidrs` to restrict which networks may trigger a rule,
- can require an HMAC tag over the whole packet (`security.packet_auth` + `match.auth: hmac`),
  which proves the sender holds the key, and can bound replays with its `window` (as can the
  remote command and raw shell channels, each with their own window),
- requires HMAC authentication, a strict command whitelist and per-argument validation for
  remote commands, and keeps that channel off by default,
- keeps the control plane on localhost with mandatory authentication,
- runs commands as argv without a shell unless `shell: true` is set explicitly,
- and logs every decision, so a ruleset can be reviewed against reality.

The strongest boundary is still the network: put the listening ports behind firewall rules or
a separate VLAN, and give every rule a `src_cidrs` when the senders are known.

## Migration notes (breaking changes)

See [CHANGELOG.md](CHANGELOG.md) for the full list. The two that bite existing installations:

- **`--port 9` no longer shuts down.** Ports 7 and 9 are reserved for plain WOL and now map to
  `noop`. Move the action to a high port (recommended), or pass `--allow-reserved-actions` to
  keep the old behaviour.
- **`--iface` is no longer required.** Without it, every eligible interface participates in
  matching; use `sol ifaces` to confirm which ones that is.

## Attribution

SoL was written by [bavix](https://github.com/bavix) and is MIT licensed. This repository is a fork
of it, [lidiaoo/sol](https://github.com/lidiaoo/sol), and keeps the original copyright notice and
licence text untouched - both notices live in [LICENSE](LICENSE).

On top of upstream, this fork carries the work described in
[docs/routing-design.md](docs/routing-design.md) and [CHANGELOG.md](CHANGELOG.md): the rule and
action model with a configuration file, multi-port and multi-NIC routing, the remote command and raw
shell channels, the HTTP control plane, outbound HTTP actions, hot reload, the guards
(cooldown, rate limit, in-flight dedup, replay windows), and the module path of its own
(`github.com/lidiaoo/sol`) with release artifacts built from it. Upstream's own last tagged release,
v0.0.2, predates all of it - read the CHANGELOG before upgrading an existing installation.

## License

MIT. See [LICENSE](LICENSE) for both copyright notices: the original one from
[bavix](https://github.com/bavix), and this fork's.
