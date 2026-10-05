# SoL - Shutdown on LAN

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
  prefix or exact), the source CIDR and the source MAC.

### Supported actions

| Type | What it does |
| --- | --- |
| `noop` | Only logs the match (the default for the reserved ports) |
| `power.sleep` | Suspends the machine |
| `power.shutdown` | Shuts the machine down |
| `power.reboot` | Reboots the machine |
| `exec` | Runs a configured command (argv, no shell by default; optional `shell: true`, command allow-list, timeout, workdir, env, and `user`/`group` privilege drop) |
| `http` | Calls a webhook (method, url, headers, body, timeout, retries; destination restricted by `security.url_allowlist`) |
| `sequence` | Runs an ordered list of the above as one action (a failing step never skips the ones behind it) |
| `remote:<id>` | A whitelisted remote command, registered as an ordinary action |

The trust ladder is documented in §20 of the design: log only < sleep/lock <
shutdown/reboot < custom command < outbound HTTP < raw remote command (off by default).

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

logging: { level: info, format: text }

security:
  dry_run: false
  reserved_ports: [7, 9]
  url_allowlist:              # outbound HTTP destinations; boundaries are enforced
    - "https://hooks.example.com/"
    - "=https://api.example.com/v1/notify"
  exec_allowlist: [/usr/bin]
  cooldown: 5s
  cooldowns: { power.shutdown: 30s }

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

rules:
  - match: { ports: [10010], content: { kind: suffix, value: "lock" } }
    action: lock
  - match: { ports: [10010], content: { kind: suffix, value: "off" }, src_cidrs: [192.168.0.0/24] }
    action: shutdown-then-notify
```

Configuration discovery order: `--config`, then `$SOL_CONFIG`, then `/etc/sol/sol.yaml`,
then `~/.config/sol/sol.yaml`. Field-by-field reference: the "字段速查" section in
[docs/routing-design.md](docs/routing-design.md).

### Remote commands (off by default)

The remote command channel lets an authenticated sender invoke a whitelisted command over
UDP (a magic packet followed by `<id>[:k=v,...]` and an HMAC-SHA256 tag) or HTTP
(`POST /v1/commands/{id}`). It is disabled unless `security.allow_remote_commands: true` and
an HMAC key is configured; the key never lives in YAML:

```yaml
security:
  allow_remote_commands: true
  remote_command_auth: { type: hmac, key_env: SOL_CMD_KEY }   # or key_file (0600)
  remote_command_ports: [10014]                              # reserved ports refused

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

### Control plane

`server.http.enabled: true` starts an HTTP control plane on `127.0.0.1:8080` by default with
mandatory authentication (`bearer` with `${SOL_TOKEN}`, `basic`, or `mtls`). It never binds a
public address unless you ask for one, and there is no unauthenticated mode.

Endpoints: `GET /healthz`, `GET /v1/status`, `GET /v1/rules`, `GET /v1/interfaces`,
`GET /metrics`, `POST /v1/actions/{name}`, `POST /v1/commands/{id}`, `POST /v1/reload`.

### Reloading

Three paths, one implementation: `SIGHUP`, `POST /v1/reload`, and `server.watch` / `--watch`
(polling the configuration file). Rules, actions, cooldowns, the remote command channel,
`dry_run` and the log level are swapped atomically per packet. A configuration that fails to
load leaves the running one untouched; a change to the **listening port set** needs a restart
and is refused with HTTP 409 rather than half-applied.

## Ports and privileges

- Ports **7, 9 and anything below 1024** are privileged: without root the socket bind fails
  with `permission denied`. Run as root, or grant the capability — the systemd unit below
  shows `AmbientCapabilities=CAP_NET_BIND_SERVICE` with a dedicated user.
- If you only use high ports (≥1024), no privilege is needed. High ports are the recommended
  default: they keep the reserved WOL ports meaningful and need no capabilities.
- `exec` actions with `user`/`group` require sol to run as root (`CAP_SETUID`/`CAP_SETGID`),
  otherwise start-up fails with `ErrNotRoot` — the drop is never silently skipped. The command
  then runs with the target account's groups, never with sol's inherited ones.
- The control plane should stay on `127.0.0.1` unless TLS and mTLS are configured.

## Choosing interfaces

`sol ifaces` shows every interface with its MAC/IPv4 and whether auto mode would select it —
useful before trusting an `--iface`-less startup:

```
NAME             TYPE      STATUS  MAC                IPV4           AUTO
lo               loopback  up                         127.0.0.1      no
enp6s0           physical  up      58:11:22:bc:78:66  192.168.0.120  yes
wlp5s0           physical  down    0a:e8:9e:0f:d3:8d  -              no
docker0          virtual   up      02:42:4e:d9:8c:14  172.17.0.1     no
```

`sol ifaces --json` prints the same list for scripts.

## Installation

### Quick Install

Download the latest release for your platform and architecture:

**Linux AMD64:**
```bash
curl -L https://github.com/bavix/sol/releases/download/v0.0.2/sol-v0.0.2-linux-amd64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**Linux ARM64:**
```bash
curl -L https://github.com/bavix/sol/releases/download/v0.0.2/sol-v0.0.2-linux-arm64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**macOS Intel:**
```bash
curl -L https://github.com/bavix/sol/releases/download/v0.0.2/sol-v0.0.2-darwin-amd64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**macOS Apple Silicon:**
```bash
curl -L https://github.com/bavix/sol/releases/download/v0.0.2/sol-v0.0.2-darwin-arm64.tar.gz | tar -xz && sudo mv sol /usr/local/bin/
```

**Windows (PowerShell):**
```powershell
Invoke-WebRequest -Uri "https://github.com/bavix/sol/releases/download/v0.0.2/sol-v0.0.2-windows-amd64.zip" -OutFile "sol.zip"
Expand-Archive -Path "sol.zip" -DestinationPath "." -Force
move sol.exe C:\Windows\System32\sol.exe
```

> v0.0.2 predates the configuration file, the extra actions and the port-9 change; read the
> [CHANGELOG](CHANGELOG.md) before upgrading an existing installation.

### Build from source

```bash
go build ./...
make test
make lint
```

### Verify Installation

```bash
sol --help
sol listen --help
sol ifaces
```

## Systemd Service Setup

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

### Service Configuration Notes

- `After=network-online.target` ensures the service starts after the network is fully online.
- `Restart=always` automatically restarts the service if it crashes.
- Keep the configuration in `/etc/sol/sol.yaml` and keep secrets in environment variables or
  0600 files referenced by the configuration — never in the YAML itself.
- The unit needs root only for privileged ports and for `exec` privilege drops; otherwise a
  dedicated user plus `CAP_NET_BIND_SERVICE` is the better default.
- When using multiple ports, repeat `--port` (or list `rules` in the configuration file).

## Security Notes

Wake-on-LAN is unauthenticated broadcast: anything that can reach a listening port can send a
valid magic packet. SoL therefore

- never exceeds `noop` on the reserved ports unless you explicitly ask for it,
- matches strictly by default (a rule only fires on the payload content it names),
- offers `src_cidrs` to restrict which networks may trigger a rule,
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

## License

See [LICENSE](LICENSE).