# Changelog

All notable changes to this project are documented in this file. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project intends to follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) once it leaves `0.x`.

The design document ([docs/routing-design.md](docs/routing-design.md)) records the reasoning
and the real-machine smoke evidence behind each of these entries; [TODO.md](TODO.md) tracks what
is still open.

## [Unreleased]

Since v0.0.2 sol grew from "shutdown or reboot on a magic packet" into a configurable
receiver: a rule model, a configuration file, an action model, a remote command channel, an
HTTP control plane and hot reload. Everything below is in `master`; v0.0.2 is the last
tagged release.

### Breaking changes

- **`--port 9` no longer shuts the machine down.** Ports 7 and 9 are reserved for plain
  Wake-on-LAN and now map to `noop` (log only); a packet with a payload is not accepted there
  and a reserved port with a non-`noop` action fails at start-up. Move the action to a high
  port (recommended), or pass `--allow-reserved-actions` /
  `security.allow_reserved_port_actions: true` to keep the old behaviour.
- **`--iface` is no longer required.** Without it, every eligible interface (up, non-loopback,
  has a MAC, not virtual) participates in matching. Use `sol ifaces` to see the selection.
- **A bare port number no longer means what it used to for every port.** A `--port` without an
  action uses `--default-action` (`shutdown` by default), so `--port 8` shuts down; combine
  with `--default-action noop` for a listen-only port.
- **Matching is strict by default.** A rule on a non-reserved port only fires on the payload
  content it names; a plain magic packet no longer matches a content rule.

### Added

- **Routing model**: rules match on `ports`, `mac` (`self`, `interface`, explicit set),
  `content` (kind any/none/suffix/prefix, with `value` or `value_hex`), `src_cidrs` and the interface;
  rules are scored so the most specific one wins and ambiguous overlaps are refused at
  start-up.
- **Configuration file** (`version: 1`): `server` (interfaces, per-interface blocks, rules,
  `watch`, `http`), `logging`, `security`, `actions`, `commands`, top-level `rules` as a
  shorthand for `server.rules`. Discovery order: `--config`, `$SOL_CONFIG`,
  `/etc/sol/sol.yaml`, `~/.config/sol/sol.yaml`. Invalid documents fail at start-up with a
  named error.
- **JSON Schema** (`schema/sol.schema.json`): editor completion and validation for the
  configuration file, mirroring the loader (unknown keys refused, enums and required keys
  declared). A test compares it against the configuration structs and the domain constants, so
  a new field or a renamed value fails the build until the schema follows.
- **Actions**: `noop`, `power.sleep`, `power.shutdown`, `power.reboot`, `exec` (argv, optional
  `shell`, `timeout`, `workdir`, `env`, `security.exec_allowlist`), `sequence` (ordered
  steps, one action to the guards) and `wol.send` (wake another machine: `mac`, `broadcast`,
  `port`, `secure_on`, `repeat`, `interval`).
- **Outbound wake-ups (`wol.send`)**: send a magic packet to a fixed target — `mac` (the only
  required parameter), `broadcast` (default `255.255.255.255`, a subnet broadcast or a unicast
  address both work), `port` (default 9), `secure_on` (6 bytes, appended to the packet),
  `repeat` (1..10) and `interval` (default 100ms, max 10s). Defaults are applied when the
  configuration is loaded. The target MAC comes from the configuration only, never from the
  triggering packet, and the action goes through the same cooldown / rate limit / dry-run /
  audit path as every other one. Sending to a broadcast address needs `SO_BROADCAST`, which the
  executor sets explicitly (a plain socket is refused with `EACCES`).
- **Packet authentication (`security.packet_auth`, off by default)**: a rule with
  `match.auth: hmac` only fires on a packet that ends with a valid truncated HMAC-SHA256 tag
  (8 bytes) over every preceding byte, `secure_on` included. The key comes from an environment
  variable or a 0600 file, never from the YAML; `wol.send` can append the tag with
  `sign: true`, so one sol can wake another that requires authentication. A rule requiring
  authentication without a key, a reserved port requiring it, and `sign: true` without a key are
  refused at start-up. The audit log records `authenticated=true|false` per match.
- **Replay protection (`security.packet_auth.window`, off by default)**: with a window set, an
  authenticated packet ends with an 8-byte unix-second stamp in front of the tag, the stamp is
  covered by the tag, and the receiver refuses a stamp outside `±window` and accepts each tag only
  once inside it. A capture sent again is a replay (`reason=replay`), one kept too long is stale
  (`reason=stale`), and both are logged. The seen-tag cache is bounded (4096) and expires entries
  with their stamp; when it is full new packets are refused instead of evicting one that could
  still be replayed. Both ends have to agree: a windowed receiver refuses the tag-only layout, and
  `wol.send` emits the stamp once its own instance sets the window. A reload starts with an empty
  cache. Refusals are counted: `/v1/status` reports `replayed` with a `replay_reasons` breakdown
  and `/metrics` exports `sol_replayed_total` (plus a line per reason), so a channel under replay
  is visible without scraping logs.
- Interface identity is re-read while sol runs: a NIC that is down at start-up is no longer
  excluded (identity is not availability), a NIC that appears later - or one whose MAC changes when
  it comes up, which is what a wireless card does - starts matching without a restart, and a NIC
  that goes away stops matching at the next unmatched packet or the 30-second poll. Every change is
  audited as `interface set changed`. Explicit `interfaces: [x]` names are still validated at
  start-up, and a name that is not there is still a start-up error.
- `logging.output` chooses the audit log destination: `stderr` (default), `stdout`, or `file` with
  `logging.file`. A log file is append-only and mode 0600 (it names source addresses, actions and
  command lines), unbuffered, and not rotated - pair it with logrotate. `SOL_LOG_OUTPUT` and
  `SOL_LOG_FILE` override both fields. A destination that cannot be used fails at start-up.
- The build identifies itself: `sol --version`, `version` and `revision` in `GET /v1/status`, and
  `sol_build_info{version,revision} 1` in `/metrics`. `make build` / `make build-static` stamp the
  version with `git describe`; an unstamped build still reports the toolchain's own metadata (a
  pseudo-version plus the commit, or the module tag), which survives `-s -w -trimpath` - so the
  release pipeline needs no change and a released binary can still say what it is.
- A Chinese README (`README.zh-CN.md`) alongside the English one, with a language switcher in
  both. The code blocks are byte-identical by construction and a test fails when they drift -
  the snippets are claims about the loader, so a translation must not become a second, quietly
  different set of examples; every standalone example in both files is also loaded against the
  real binary.
- SecureOn can be set per interface block and per rule, not only globally: a rule inherits its
  block's password, the block inherits `security.secure_on`, and an explicitly empty
  `secure_on: ""` opts out of the default (it means "no password", which is not the same as
  leaving the key out). Packets are read by trying every configured password, and a rule only
  matches the one it asks for.
- Breaking: a password on a reserved port (7 or 9) is now refused at startup. Those ports take
  plain magic packets, so `security.secure_on` together with `--port 9` used to start and then
  silently never match anything.
- Breaking: `server.interfaces[].secure_on` was rejected with "not implemented yet" and now works.
- A trigger that arrives while the same action is still running is suppressed instead of starting
  a second run of it (the §19.6 open question: no merge-and-wait, because a run can take minutes
  and a control-plane request should not be pinned to it). The identity is the action plus what it
  would actually do - the validated remote arguments, or the raw shell command - so two different
  invocations of one action never suppress each other. It needs no configuration, counts as
  `inflight` (plus `suppressed`) in `/v1/status` and as `sol_inflight_total` in `/metrics`, and
  answers 429 on `/v1/actions/{name}`, `/v1/commands/{id}` and `/v1/exec`.
- The rule conflict check compares source filters by intersection rather than by equality: two
  rules that can both match one address - `10.0.0.0/8` and `10.1.0.0/16` on the same port with
  the same content and scope - are now refused instead of being resolved by whichever rule happens
  to be listed first. Nothing else orders them, since `src_cidrs` contributes a fixed specificity
  bump and not the prefix length. To cover a subnet and leave the rest alone, leave out the
  catch-all rule: an unmatched packet runs nothing. Configs that relied on the old silence are
  rejected at startup with `overlapping rule scopes with matching conditions`.
- Fixed: `ambiguous rules` said which port was involved but not how to resolve it. The message
  now names the differences the validation accepts - disjoint `src_cidrs`, a different port, a
  different `secure_on` password, or a stricter `auth` requirement - and a test loads all four, so
  a hint that stopped working would fail the build instead of sending an operator in circles.
- Fixed: the `ErrNotRoot` message promised `or CAP_SETUID/CAP_SETGID`, which sol never accepted -
  the privilege-drop check is a plain `geteuid() == 0`, so a capability-only setup is refused.
  The message now says `exec user/group requires root`.
- Fixed: a missing packet key, remote command key or raw shell key was reported as "cannot
  resolve the http auth secret" - the resolver is shared by every secret, so the message now says
  "cannot resolve a configured secret" and keeps naming the field that failed
  (`packet_auth.key`, `remote_command_auth.key`, `raw_shell_auth.key`).
- Fixed: a remote command refused by a cooldown or the rate limit answered 500 on
  `POST /v1/commands/{id}` instead of 429, and a raw shell command refused by a guardrail answered
  202 on `POST /v1/exec` without running anything.
- Hot reload can move the listening port set. The new ports are bound before anything is closed,
  so a reload that cannot bind one of them is refused with 409 and the running listener keeps
  serving; on success the added ports start reading at once and the ports that left the set are
  closed. The interface list is refreshed with the reload, so `/v1/interfaces` and the audit log
  follow the machine. `app.ErrReloadRestartRequired` (a port change needed a restart) is gone,
  replaced by `app.ErrReloadBind`.
- Remote command segments can be stamped too (`remote_command_auth.window`), and so can the raw
  shell channel that rides on them (`raw_shell_auth.window`). The same guard as packet
  authentication then accepts a command once inside the window and refuses a captured segment:
  `reason=replay|stale`, counted alongside packets and logged with `channel=command|raw_shell`
  (packets log `channel=packet`). Off by default, both ends must agree, and a reload keeps the
  window.
- **Raw shell channel (`security.allow_raw_shell`, off by default)**: an opt-in that lets a remote
  sender run an arbitrary shell command through `/bin/sh -c`, over UDP
  (`[magic packet][secure_on?][command][HMAC tag]`) or `POST /v1/exec` on the control plane. It
  rides on the remote command channel (`security.allow_remote_commands`) but carries its own HMAC
  key (`raw_shell_auth`), its own non-reserved port set (`raw_shell_ports`, which may not overlap
  `remote_command_ports`), an optional source filter (`raw_shell_src_cidrs`) and an optional
  command allowlist (`raw_shell_allowlist`, entries anchored at both ends). The timeout and the
  privilege drop (`raw_shell_timeout` / `raw_shell_user` / `raw_shell_group`) reuse the exec
  parameters and are resolved at start-up. Every command is logged in full with its exit code,
  the guards (dry-run, cooldown, rate limit) apply, and an enabled channel warns loudly at
  start-up. `raw:shell` is an internal label, not an action name a rule may reference; a channel
  that is half-configured (keys without the opt-in, no port, a bad key, a reserved or shared port,
  an uncompilable allowlist entry) is refused at start-up.
- **`exec` privilege drop**: `user`/`group` (name or id) run the command as that account with
  that account's groups; requires root, and the drop is validated at start-up rather than
  silently skipped.
- **Outbound HTTP actions**: `method`, `url`, `headers`, `body`, `timeout`, `retries`,
  whitelisted interpolation (`{{.Action}}`, `{{.SrcIP}}`, `{{.DstPort}}`, `{{.Arg.x}}`, ...),
  no redirect following, TLS verification, 64 KiB response cap, and `security.url_allowlist`
  with prefix (host+path boundary), `=` exact and `~` regex entries.
- **Remote command channel** (off by default): `commands[].id` whitelist over UDP
  (`<magic packet><id>[:k=v,...]<HMAC-SHA256>`) and `POST /v1/commands/{id}`, per-argument
  `type`/`enum`/`pattern`/`required` validation, optional `user`/`group`, non-reserved ports
  only. Each command is registered as the ordinary action `remote:<id>`.
- **HTTP control plane** (off by default, `127.0.0.1:8080`, authentication mandatory):
  `GET /healthz`, `GET /v1/status`, `GET /v1/rules`, `GET /v1/interfaces`, `GET /metrics`,
  `POST /v1/actions/{name}`, `POST /v1/commands/{id}`, `POST /v1/reload`; bearer, basic and
  mTLS authentication.
- **Hot reload**: `SIGHUP`, `POST /v1/reload` and `server.watch` / `--watch` (polling the
  configuration file) share one implementation, swapping the routing snapshot atomically per
  packet; a rejected reload leaves the running configuration untouched.
- **Guards**: per-action cooldowns (`security.cooldown`, `security.cooldowns.<name>`), a global
  token bucket across every action and trigger (`security.rate_limit`, `security.rate_burst`),
  instance/interface/rule-level `dry_run`, structured audit logging with every decision, and
  `sol ifaces [--json]` to inspect the interface selection. A guarded action is logged, counted
  (`suppressed`, plus `rate_limited` for the bucket) and refused with 429 on the control plane.
- **Sleep action** (`power.sleep`) and the `--default-action` flag.

### Fixed

- **A `${VAR}` inside a YAML comment no longer makes that variable required.** Environment
  substitution ran over the raw file, so commenting out a line such as
  `# secure_on: "${NAS_WOL_PASSWORD}"` made sol refuse to start. Substitution is now per line,
  skips comments (a `#` inside a quoted scalar is still data) and treats block scalar bodies
  (`|`, `>`) as data in full.

### Security

- Reserved ports 7/9 accept only `noop` and only a payload-free magic packet.
- Secrets (HTTP tokens, the remote command HMAC key) come from environment variables or 0600
  files; they are never read from the YAML body.
- The control plane binds localhost by default and has no unauthenticated mode.
- Exec commands run as argv without a shell unless `shell: true` is requested explicitly.
- The raw shell channel is the one place that runs a shell: it is off by default, needs its own
  HMAC key and a dedicated non-reserved port, honours `raw_shell_src_cidrs` and
  `raw_shell_allowlist` (anchored at both ends), logs every command it runs, and warns at
  start-up. Whoever holds that key can run anything the service user can.
- `packet_auth.window` bounds how long a captured authenticated packet stays usable, and the
  seen-tag cache makes a second copy of it a no-op. Without a window the tag alone is proof of the
  key, not of freshness.
- `exec` drops supplementary groups, so a command dropped to `nobody` cannot keep sol's own
  group memberships (a real leak found by the smoke test).

### Documentation

- `docs/routing-design.md` is the authoritative design, including the implemented-vs-planned
  status of every feature with its smoke evidence.
- `README.md` covers installation, CLI, configuration, control plane, privileges and the
  breaking changes; `TODO.md` tracks the remaining work.

## [0.0.2] - previous release

Shutdown-only listener: `sol listen --iface eth0 --port 9` shut the machine down when a
Wake-on-LAN magic packet arrived, with `--dry-run` for observation. No configuration file, no
rule model, no other actions.