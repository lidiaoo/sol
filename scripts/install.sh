#!/bin/sh
# sol 安装脚本 —— 零参数。
#
# 它只做三件事：认出现状 → 生成/读取"怎么跑"的配置（就放在你执行脚本的这个目录：
# ./install.yaml 与 ./sol.yaml）→ 你确认之后才动手。
# 设计文档（本脚本与它是同一份契约）：docs/install-design.md
#
#   sh install.sh              生成配置 → 展示现状 → 问一句 → 执行
#   printf 'y\n' | sh install.sh   CI / 无人值守：管道喂答案
#   没有终端时只生成配置，不执行任何操作（绝不猜"用户大概是想装"）
#
# 环境变量（都有默认值；普通用户不需要设，测试与多实例用）：
#   SOL_INSTALL_ROOT  把系统路径挂到另一个根下（默认 /，DESTDIR 风格）
#   SOL_UNIT_NAME     服务名（Linux 默认 sol.service，macOS 默认 com.lidiaoo.sol）

set -eu

DESIGN="docs/install-design.md"

die() {
	printf '错误: %s\n' "$*" >&2
	exit 1
}

# 内部参数：升权重跑时用来把"你已经答过的答案"带过去。用户不需要、也不应该记这些；
# 它们存在的唯一原因是 sudo 默认会清环境（env 传不过去）。
ACTION_ARG=""
SERVICE_ARG=""
RUN_DIR_ARG=""
for arg in "$@"; do
	case "$arg" in
	--sol-action=*) ACTION_ARG=${arg#--sol-action=} ;;
	--sol-service=*) SERVICE_ARG=${arg#--sol-service=} ;;
	--sol-run-dir=*) RUN_DIR_ARG=${arg#--sol-run-dir=} ;;
	*) die "这个脚本不接受参数（$arg）。所有选择都在生成的配置文件和问答里。" ;;
	esac
done

# ───────────────────────── 输出 ─────────────────────────

bold=""
reset=""
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	bold=$(printf '\033[1m')
	reset=$(printf '\033[0m')
fi

say() { printf '%s\n' "$*"; }
head2() { printf '\n%s%s%s\n' "$bold" "$*" "$reset"; }
warn() { printf '提醒: %s\n' "$*" >&2; }
# ───────────────────────── 平台与路径 ─────────────────────────

OS=$(uname -s)
ARCH=$(uname -m)

case "$OS" in
Linux) SERVICE_KIND=systemd ;;
Darwin) SERVICE_KIND=launchd ;;
*) die "只支持 Linux 与 macOS（Windows 请用 install.ps1）。当前系统：$OS" ;;
esac

# 系统路径挂载点：默认就是 /（正常安装）；测试与 chroot 时可以指到别处。
ROOT=${SOL_INSTALL_ROOT:-}
if [ "$ROOT" = "/" ]; then ROOT=""; fi
if [ -n "$ROOT" ]; then
	[ -d "$ROOT" ] || die "SOL_INSTALL_ROOT=$ROOT 不存在"
	ROOT=$(cd "$ROOT" && pwd)
fi

PREFIX="$ROOT/usr/local"
BINDIR="$PREFIX/bin"
SHAREDIR="$PREFIX/share/sol"
LEDGER="$SHAREDIR/install.json"
HISTORY="$SHAREDIR/install.log"

if [ "$SERVICE_KIND" = "systemd" ]; then
	UNIT_NAME=${SOL_UNIT_NAME:-sol.service}
	UNIT_PATH="$ROOT/etc/systemd/system/$UNIT_NAME"
else
	UNIT_NAME=${SOL_UNIT_NAME:-com.lidiaoo.sol}
	UNIT_PATH="$ROOT/Library/LaunchDaemons/$UNIT_NAME.plist"
fi

UID_NOW=$(id -u)
SUDO=""
if [ -n "$ROOT" ]; then
	: # 自选的根（沙箱 / chroot）：不 sudo，写不进去就报错，不要密码
elif [ "$UID_NOW" != "0" ]; then
	command -v sudo >/dev/null 2>&1 || die "需要 root 权限的步骤要 sudo，但没找到 sudo"
	SUDO="sudo"
fi

# 目录可能还不存在，"能不能直接写"要看最近的已存在祖先。
writable_target() { # writable_target <路径>
	d=$1
	while [ ! -d "$d" ] && [ "$d" != "/" ] && [ -n "$d" ]; do d=$(dirname "$d"); done
	[ -w "$d" ]
}

as_root() {
	if [ -n "$SUDO" ]; then "$SUDO" "$@"; else "$@"; fi
}

# 写系统路径时先试直接写，不行才 sudo（用户自己就是 root 或目录本来就可写时不会白要密码）。
put_root() { # put_root <本地文件> <目标路径> <权限>
	if writable_target "$(dirname "$2")"; then
		cp "$1" "$2" && chmod "$3" "$2"
	else
		as_root cp "$1" "$2" && as_root chmod "$3" "$2"
	fi
}

rm_root() { # rm_root <路径...>
	for p in "$@"; do
		[ -e "$p" ] || continue
		if writable_target "$(dirname "$p")"; then rm -f "$p"; else as_root rm -f "$p"; fi
	done
}

mkdir_root() { # mkdir_root <目录>
	[ -d "$1" ] && return 0
	if writable_target "$(dirname "$1")"; then mkdir -p "$1"; else as_root mkdir -p "$1"; fi
}

# 生成的文件就放在**你执行脚本的那个目录**：./install.yaml 与 ./sol.yaml。
# 找得到、看得见、改完重跑就行；服务定义里用的是绝对路径，所以不踩"服务有自己的家目录"那个坑。
RUN_DIR=${RUN_DIR_ARG:-${SOL_INSTALL_RUN_DIR:-$(pwd)}}
USER_CONFIG="$RUN_DIR/install.yaml"

# ───────────────────────── 升权 ─────────────────────────

# 已经带答案升过权了（子进程）：不再问，直接做。
ELEVATED_ACTION=${ACTION_ARG:-${SOL_INSTALL_ACTION:-}}

# 要写系统路径 / 注册服务这一步需要 root。与其半路一条条 sudo（密码问一半、失败还被吞掉），
# 不如动手之前一次性升权：把已经问到的答案带过去，用 sudo 重跑一遍自己，同一个目录。
escalate() { # escalate <install|uninstall>
	action=$1
	[ "$UID_NOW" = "0" ] && return 0
	[ -n "$ROOT" ] && return 0 # 自选的根（沙箱 / chroot）：自己的地盘，不升权
	needs=no
	case "$action" in
	uninstall) needs=yes ;; # 卸载要动系统单元与落点
	install)
		writable_target "$BINDIR" || needs=yes
		writable_target "$SHAREDIR" || needs=yes
		[ "$SERVICE_CHOSEN" = true ] && needs=yes
		;;
	esac
	[ "$needs" = yes ] || return 0

	# 管道执行（curl | sh）没有可重跑的脚本文件，只能退回逐条 sudo。
	self="$SCRIPT_DIR/$(basename "$0")"
	if [ ! -f "$self" ]; then
		warn "这一步要 root，但脚本是管道执行的、没法自己升权：下面需要权限的步骤会逐条 sudo。"
		return 0
	fi
	[ -n "$SUDO" ] || die "这一步需要 root（落点 $BINDIR、服务 $UNIT_NAME）。请用 root 角色重跑，或把落点换到你自己有写权限的目录。"

	say ""
	say "这一步要写 $BINDIR 并注册 $SERVICE_KIND 服务，需要 root。"
	say "我用 sudo 重新执行一遍自己：同一个目录、你刚才的答案带过去，不会再问一遍。"
	if [ "$SERVICE_CHOSEN" = true ]; then svc=yes; else svc=no; fi
	# exec 会顶掉当前进程：先放锁（子进程会自己重新拿），否则父进程的 trap 永远不会跑。
	rm -rf "$LOCK_DIR"
	exec "$SUDO" "$self" "--sol-action=$action" "--sol-service=$svc" "--sol-run-dir=$RUN_DIR"
}

# ───────────────────────── 小工具 ─────────────────────────

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "找不到 sha256sum / shasum"
	fi
}

# run_limited <秒> <命令...>：跑一会儿就杀掉。用来判断 sol 是"起来了"还是"被拒了"。
# 输出（stdout+stderr）留到 $TMPDIR_OUT，返回值：0 = 还在跑（起来了），1 = 自己退出了。
TMPDIR_OUT=""
run_limited() {
	secs=$1
	shift
	run_limited_out=$(mktemp)
	TMPDIR_OUT=$run_limited_out
	"$@" >"$run_limited_out" 2>&1 &
	pid=$!
	i=0
	while [ "$i" -lt "$secs" ]; do
		sleep 1
		if ! kill -0 "$pid" 2>/dev/null; then
			wait "$pid" 2>/dev/null || true
			return 1
		fi
		i=$((i + 1))
	done
	kill "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
	return 0
}

# ───────────────────────── 锁 ─────────────────────────

LOCK_DIR="${TMPDIR:-/tmp}/sol-install.lock"
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
	owner=$(cat "$LOCK_DIR/pid" 2>/dev/null || true)
	if [ -n "$owner" ] && kill -0 "$owner" 2>/dev/null; then
		die "另一个安装/卸载正在进行（pid $owner，$LOCK_DIR）。确定没有的话：rm -rf $LOCK_DIR"
	fi
	warn "发现上次留下的锁（pid ${owner:-未知} 已经不在了），接管它。"
	rm -rf "$LOCK_DIR"
	mkdir "$LOCK_DIR" 2>/dev/null || die "拿不到锁：$LOCK_DIR"
fi
echo $$ >"$LOCK_DIR/pid"
trap 'rm -rf "$LOCK_DIR"; [ -n "$TMPDIR_OUT" ] && rm -f "$TMPDIR_OUT"' EXIT INT TERM

# ───────────────────────── 认出现状 ─────────────────────────

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

# ① 脚本旁边 ② 当前目录 ③ PATH。指到同一个文件的多次命中只算一次。
find_local_binary() {
	for c in "$SCRIPT_DIR/sol" "$PWD/sol"; do
		[ -f "$c" ] && [ -x "$c" ] && {
			printf '%s\n' "$c"
			return 0
		}
	done
	old_ifs=$IFS
	IFS=:
	for d in $PATH; do
		[ -n "$d" ] || d=.
		if [ -x "$d/sol" ]; then
			IFS=$old_ifs
			printf '%s\n' "$d/sol"
			return 0
		fi
	done
	IFS=$old_ifs
	return 1
}

LOCAL_BIN=""
if find_local_binary >/dev/null 2>&1; then LOCAL_BIN=$(find_local_binary); fi

# PATH 上所有命中的 sol，报告里说清哪个生效。
PATH_SOLS=""
old_ifs=$IFS
IFS=:
for d in $PATH; do
	[ -n "$d" ] || d=.
	[ -x "$d/sol" ] && PATH_SOLS="$PATH_SOLS$d/sol
"
done
IFS=$old_ifs
PATH_SOLS=$(printf '%s' "$PATH_SOLS" | sed '/^$/d')

binary_version() { # binary_version <路径>
	"$1" --version 2>/dev/null | head -n1
}

# 台账（脚本自己写、自己读；只读我们写进去的那几个字段）
ledger_field() { # ledger_field <字段名>
	[ -f "$LEDGER" ] || return 0
	sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$LEDGER" | head -n1
}

LEDGER_EXISTS=no
[ -f "$LEDGER" ] && LEDGER_EXISTS=yes
LEDGER_VERSION=$(ledger_field installed_version)
LEDGER_SHA=$(ledger_field sha256)
LEDGER_BINARY=$(ledger_field binary)
LEDGER_SERVICE_KIND=$(ledger_field kind)
LEDGER_SERVICE_NAME=$(ledger_field name)
LEDGER_CREATED_UNIT=$(sed -n 's/.*"created_unit"[[:space:]]*:[[:space:]]*\(true\|false\).*/\1/p' "$LEDGER" 2>/dev/null | head -n1)
LEDGER_PREV_VERSION=$(sed -n 's/.*"previous".*"installed_version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$LEDGER" 2>/dev/null | head -n1)
[ -n "$LEDGER_PREV_VERSION" ] || LEDGER_PREV_VERSION=$(sed -n '/"previous"/,/}/s/.*"installed_version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$LEDGER" 2>/dev/null | head -n1)

# 服务现在是什么状态（只读，不动它）
service_state() {
	case "$SERVICE_KIND" in
	systemd)
		# SOL_INSTALL_ROOT 下写出来的是"另一台机器的根"，本机 systemd 的状态与它无关。
		if [ -n "$ROOT" ]; then
			printf 'unregistered'
			return 0
		fi
		if command -v systemctl >/dev/null 2>&1; then
			if systemctl is-active --quiet "$UNIT_NAME" 2>/dev/null; then
				printf 'running'
			elif [ -f "$UNIT_PATH" ] || systemctl is-enabled --quiet "$UNIT_NAME" 2>/dev/null; then
				printf 'installed'
			else
				printf 'absent'
			fi
		else
			printf 'unknown'
		fi
		;;
	launchd)
		if command -v launchctl >/dev/null 2>&1; then
			if launchctl print "system/$UNIT_NAME" >/dev/null 2>&1; then printf 'running'; else printf 'absent'; fi
		else
			printf 'unknown'
		fi
		;;
	esac
}

SERVICE_STATE=$(service_state)

# 包管理器装的？只报不管。
MANAGED_BY=""
if [ "$SERVICE_KIND" = "launchd" ] && command -v brew >/dev/null 2>&1; then
	brew list --formula 2>/dev/null | grep -qx sol && MANAGED_BY="brew"
fi

# ───────────────────────── 安装配置（只有 run.args）─────────────────────────

DEFAULT_CONFIG_PATH="$RUN_DIR/sol.yaml"
DEFAULT_ARGS="listen --config $DEFAULT_CONFIG_PATH"          # 给人看、给报告用
DEFAULT_ARGS_YAML="listen, --config, $DEFAULT_CONFIG_PATH"   # 写进文件：YAML 列表要逗号

writable_target "$RUN_DIR" || die "在这个目录里写不了文件：$USER_CONFIG 与 $RUN_DIR/sol.yaml 要生成在这儿。
换一个你有写权限的目录（比如 ~）再跑一次。"

write_user_config() {
	mkdir -p "$(dirname "$USER_CONFIG")"
	cat >"$USER_CONFIG" <<EOF
# 由 install.sh 生成：sol 怎么跑。改这里，然后重新跑一遍脚本。
run:
  args: [$DEFAULT_ARGS_YAML]
EOF
}

# 把 args 解析成一行一个 token（只支持本脚本自己写出的那种写法，不猜别的）。
parse_args() {
	sed -n '/^[[:space:]]*args:/p' "$USER_CONFIG" |
		sed 's/^[[:space:]]*args:[[:space:]]*//' |
		tr -d '[]' |
		tr ',' '\n' |
		sed 's/^[[:space:]]*//; s/[[:space:]]*$//' |
		sed '/^$/d'
}

CONFIG_GENERATED=no
if [ ! -f "$USER_CONFIG" ]; then
	write_user_config
	CONFIG_GENERATED=yes
fi

OLD_IFS_SAVE=$IFS
IFS='
'
ARGS=$(parse_args)
IFS=$OLD_IFS_SAVE
[ -n "$ARGS" ] || die "$USER_CONFIG 里没有 run.args。修的写法：run:{ args: [$DEFAULT_ARGS_YAML] }"

# 校验每个 token：子命令 + 真 flag 集合（向二进制问，别抄一份会漂移的清单）
validate_args() {
	[ -n "$LOCAL_BIN" ] || return 0
	first=$(printf '%s\n' "$ARGS" | head -n1)
	[ "$first" = "listen" ] || die "run.args 第一个词必须是 listen（当前是 $first）"
	known=$("$LOCAL_BIN" listen --help 2>/dev/null | grep -oE -- '--[a-zA-Z][a-zA-Z-]*' | sort -u)
	for tok in $ARGS; do
		case "$tok" in
		-*) printf '%s\n' "$known" | grep -qx -- "$tok" || die "run.args 里有 sol listen 不认识的参数：$tok（见 $LOCAL_BIN listen --help）" ;;
		esac
	done
}
validate_args

# 运行配置（sol 自己认的那份）与它里面的端口，用于权限告警
RUN_CONFIG=""
prev=""
for tok in $ARGS; do
	if [ "$prev" = "--config" ]; then RUN_CONFIG=$tok; fi
	prev=$tok
done
[ -n "$RUN_CONFIG" ] || RUN_CONFIG="$DEFAULT_CONFIG_PATH"

# sol.yaml：最小可用的一份（一条 noop 规则，只记日志不做事）。没有才写，绝不覆盖。
RUNTIME_CONFIG_GENERATED=no
ensure_runtime_config() {
	[ -f "$RUN_CONFIG" ] && return 0
	runtime_tmp=$(mktemp)
	cat >"$runtime_tmp" <<'YAML'
# 由安装脚本生成的最小配置：一条 noop 规则——匹配到只记日志，什么也不做。
# 端口、动作按需改；改完重跑安装脚本（或重启服务）即可。
version: 1
rules:
  - match: { ports: [10010], content: { kind: none } }
    action: noop
YAML
	mkdir_root "$(dirname "$RUN_CONFIG")"
	put_root "$runtime_tmp" "$RUN_CONFIG" 0644
	rm -f "$runtime_tmp"
	RUNTIME_CONFIG_GENERATED=yes
}

ports_below_1024() {
	[ -f "$RUN_CONFIG" ] || return 1
	nums=$(sed -n 's/^[[:space:]]*ports:[[:space:]]*\[\([0-9, ]*\)\].*/\1/p' "$RUN_CONFIG" | tr ',' ' ')
	for n in $nums; do
		[ "$n" -lt 1024 ] 2>/dev/null && return 0
	done
	return 1
}

# ───────────────────────── 现状／配置展示 ─────────────────────────

show_state() {
	head2 "== 现状 =="
	if [ -n "$LOCAL_BIN" ]; then
		say "二进制      $LOCAL_BIN（$(binary_version "$LOCAL_BIN")，sha256 $(sha256 "$LOCAL_BIN" | cut -c1-12)…）"
	else
		say "二进制      没有找到可用的 sol：脚本旁边、当前目录、\$PATH 里都没有"
	fi
	if [ -n "$PATH_SOLS" ]; then
		say "PATH 上的 sol"
		printf '%s\n' "$PATH_SOLS" | while IFS= read -r p; do
			if [ "$p" = "$LOCAL_BIN" ]; then say "  $p  ← 生效"; else say "  $p"; fi
		done
	fi
	if [ "$LEDGER_EXISTS" = yes ]; then
		say "已安装      $LEDGER_VERSION（由安装脚本安装，$LEDGER）"
	else
		say "已安装      无台账（从没被这个脚本装过；下面按你已有的二进制处理）"
		if [ -f "$UNIT_PATH" ]; then
			warn "有单元文件但没有台账（$UNIT_PATH）：不是我装的，我不动它。
      要手工清掉：
        sudo systemctl disable --now $UNIT_NAME
        sudo rm $UNIT_PATH
        sudo systemctl daemon-reload"
		fi
	fi
	case "$(service_state)" in
	running) say "服务        $SERVICE_KIND，在运行（$UNIT_NAME）" ;;
	installed) say "服务        $SERVICE_KIND，已安装但没在运行（$UNIT_NAME）" ;;
	absent) say "服务        没有（$UNIT_NAME 不存在）" ;;
	unregistered) say "服务        SOL_INSTALL_ROOT 下只写单元文件，本机 systemd 里没有它" ;;
	*) say "服务        状态未知（没有 systemctl / launchctl？）" ;;
	esac
	[ -n "$MANAGED_BY" ] && say "包管理器    这个 sol 是 $MANAGED_BY 装的（升级会覆盖它）"
	if [ "$UID_NOW" = "0" ]; then
		say "权限        root（写系统路径与注册服务不需要再升权）"
	elif [ -n "$ROOT" ]; then
		say "权限        普通用户 + SOL_INSTALL_ROOT=$ROOT（自己的地盘，不会 sudo）"
	else
		say "权限        普通用户（要写系统路径/注册服务时，动手前会用 sudo 重跑一遍自己）"
	fi
	if [ -f "$RUN_CONFIG" ]; then
		if [ "$RUNTIME_CONFIG_GENERATED" = yes ]; then
			say "运行配置    $RUN_CONFIG（刚生成的最小配置：一条 noop 规则，按需改）"
		else
			say "运行配置    $RUN_CONFIG（sol 会读它）"
		fi
	else
		say "运行配置    还不存在：$RUN_CONFIG"
	fi
}

show_config() {
	head2 "== 安装配置 =="
	say "$USER_CONFIG"
	say ""
	sed 's/^/  /' "$USER_CONFIG"
	say ""
	if [ "$CONFIG_GENERATED" = yes ]; then
		say "（刚生成的。改它，然后重新跑脚本；脚本不会覆盖已存在的这份文件。）"
	fi
}

# ───────────────────────── 问答 ─────────────────────────

# 问答从 stdin 读：终端里跑就是键盘，`printf 'y\n' | sh install.sh` 就是那行答案，
# `curl | sh` 时 stdin 是脚本本身、读到的不是 y，于是什么都不会执行——三个场景都安全。
READ_EOF=no

read_answer() { # read_answer <提示>：提示一律写 stderr，答案才走 stdout。
	printf '%s ' "$1" >&2
	if ! read -r ans; then
		READ_EOF=yes
		ans=""
	fi
	if [ ! -t 0 ]; then printf '\n' >&2; fi
	ans=$(printf '%s' "$ans" | tr '[:upper:]' '[:lower:]')
}

ask() { # ask <提示> <默认(y|n)>
	prompt=$1
	def=$2
	READ_EOF=no
	if [ "$def" = y ]; then read_answer "$prompt [Y/n]"; else read_answer "$prompt [y/N]"; fi
	# 读不到答案（EOF、curl | sh）绝不算同意：默认值只在有人真的按了回车时才算数。
	[ "$READ_EOF" = yes ] && return 1
	[ -n "$ans" ] || ans=$def
	case "$ans" in
	y | yes) return 0 ;;
	*) return 1 ;;
	esac
}

ask_choice() { # ask_choice <提示> -> 打印用户选的一个词
	READ_EOF=no
	read_answer "$1"
	# 同样：没人的话就什么也别做，当作"退出"
	if [ "$READ_EOF" = yes ]; then
		printf 'n'
		return 0
	fi
	[ -n "$ans" ] || ans="apply"
	printf '%s' "$ans"
}

# ───────────────────────── 执行：动作清单 ─────────────────────────

ACTIONS="" # 完整动作清单 → $HISTORY

act() { # act <是否root: root|user> <说明> [等价命令]
	ACTIONS="$ACTIONS
[$1] $2"
	# 只写真的能照抄的命令；没有就不写（写个假的比不写更糟）。
	if [ -n "${3:-}" ]; then
		ACTIONS="$ACTIONS
        \$ $3"
	fi
}

precheck() { # 用同一个二进制 + 同一份配置起一次（dry-run：匹配了也不会真做事）
	[ -n "$LOCAL_BIN" ] || return 0
	if [ ! -f "$RUN_CONFIG" ]; then
		warn "运行配置 $RUN_CONFIG 还不存在，没法预检；服务起来后会立刻退出（sol 拒绝在没有规则时启动）"
		return 1
	fi
	# shellcheck disable=SC2086
	if run_limited 3 "$LOCAL_BIN" $ARGS; then
		say "预检        通过（sol 用这份配置起来了 3 秒，匹配了也不会真做事：listen 是 dry-run）"
		return 0
	fi
	err=$(cat "$TMPDIR_OUT" 2>/dev/null | head -n3)
	say "预检        被拒绝："
	printf '%s\n' "$err" | sed 's/^/            /'
	return 1
}

write_ledger() { # write_ledger <incomplete: true|false> [prevVersion] [prevSha]
	binary=${DEST_BIN:-$BINDIR/sol}
	sha=""
	[ -f "$binary" ] && sha=$(sha256 "$binary")
	cat >"$LEDGER_TMP" <<EOF
{
  "schema": 1,
  "method": "script",
  "installed_version": "$(binary_version "$binary" | sed 's/.*version //')",
  "installed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "prefix": "$BINDIR",
  "binary": "$binary",
  "sha256": "$sha",
  "service": {
    "kind": "$SERVICE_KIND",
    "name": "$UNIT_NAME",
    "unit_path": "$UNIT_PATH",
    "created_unit": $SERVICE_CHOSEN,
    "created_user": false,
    "cap": "$(ports_below_1024 && printf 'CAP_NET_BIND_SERVICE' || printf '')"
  },
  "firewall_rules": [],
  "config_paths": ["$RUN_CONFIG"],
  "log_paths": [],
  "previous": { "installed_version": "$2", "sha256": "$3" },
  "incomplete": $1
}
EOF
	if [ -f "$LEDGER" ] && [ -w "$LEDGER" ]; then
		cp "$LEDGER_TMP" "$LEDGER"
	else
		mkdir_root "$SHAREDIR"
		put_root "$LEDGER_TMP" "$LEDGER" 0644
	fi
}

append_history() { # append_history <动作>
	tmp=$(mktemp)
	{
		printf '== %s %s ==\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1"
		printf '%s\n' "$ACTIONS"
	} >"$tmp"
	if [ -f "$HISTORY" ] && [ -w "$HISTORY" ]; then
		cat "$tmp" >>"$HISTORY"
	else
		mkdir_root "$SHAREDIR"
		if [ -n "$SUDO" ] && [ ! -w "$SHAREDIR" ]; then
			as_root tee -a "$HISTORY" <"$tmp" >/dev/null
		else
			cat "$tmp" >>"$HISTORY" 2>/dev/null || as_root tee -a "$HISTORY" <"$tmp" >/dev/null
		fi
	fi
	rm -f "$tmp"
}

# ───────────────────────── 执行：装/升级 ─────────────────────────

SERVICE_CHOSEN=false
DEST_BIN=""
NOOP=no

# 现在这份配置对应的服务定义。**只有这一个渲染器**：写盘和"比有没有变"都用它，
# 否则两个渲染器一漂移，重跑时就永远认为"变了"，每次都白重启一遍服务。
render_unit() {
	case "$SERVICE_KIND" in
	systemd)
		{
			printf '[Unit]\n'
			printf 'Description=SoL listener\n'
			printf 'After=network-online.target\n\n'
			printf '[Service]\n'
			printf 'ExecStart=%s %s\n' "$BINDIR/sol" "$(printf '%s' "$ARGS" | tr '\n' ' ' | sed 's/ $//')"
			printf 'Restart=always\n'
			printf '# 要用保留端口 7/9 或 <1024 端口时取消注释（并保证配置里的端口确实是那些）：\n'
			printf '# AmbientCapabilities=CAP_NET_BIND_SERVICE\n'
			printf '# CapabilityBoundingSet=CAP_NET_BIND_SERVICE\n'
			printf '# User=sol\n\n'
			printf '[Install]\n'
			printf 'WantedBy=multi-user.target\n'
		}
		;;
	launchd)
		{
			printf '<plist version="1.0">\n<dict>\n'
			printf '  <key>Label</key><string>%s</string>\n' "$UNIT_NAME"
			printf '  <key>ProgramArguments</key>\n  <array>\n'
			printf '    <string>%s</string>\n' "$BINDIR/sol"
			printf '%s\n' "$ARGS" | while IFS= read -r a; do printf '    <string>%s</string>\n' "$a"; done
			printf '  </array>\n'
			printf '  <key>RunAtLoad</key><true/>\n'
			printf '  <key>KeepAlive</key><true/>\n'
			printf '  <key>StandardErrorPath</key><string>%s/var/log/sol.log</string>\n' "$PREFIX"
			printf '</dict>\n</plist>\n'
		}
		;;
	esac
}

do_install() {
	[ -n "$LOCAL_BIN" ] || die "没有可用的 sol 二进制：把 sol（或 sol.exe）放在脚本旁边或 \$PATH 里，再跑一次"

	new_ver=$(binary_version "$LOCAL_BIN")
	new_sha=$(sha256 "$LOCAL_BIN")
	DEST_BIN="$BINDIR/sol"

	# 你说"应用"，但确实没有任何变化 → 不折腾服务，也不写历史。
	if [ "$LEDGER_EXISTS" = yes ] && [ "$new_sha" = "$LEDGER_SHA" ]; then
		unit_same=yes
		if [ "$SERVICE_WANTED" = true ]; then
			[ -f "$UNIT_PATH" ] || unit_same=no
			if [ "$unit_same" = yes ]; then
				unit_render=$(mktemp)
				render_unit >"$unit_render"
				cmp -s "$unit_render" "$UNIT_PATH" || unit_same=no
				rm -f "$unit_render"
			fi
		fi
		state_now=$(service_state)
		fresh=no
		case "$state_now" in
		running) fresh=yes ;;
		unregistered) fresh=yes ;; # SOL_INSTALL_ROOT 下本来就不注册服务
		esac
		if [ "$unit_same" = yes ] && [ "$fresh" = yes ]; then
			say ""
			say "已是最新，无需操作：二进制没变（sha256 $new_sha 与台账一致），服务定义没变，服务在运行。"
			NOOP=yes
			return 0
		fi
	fi

	# 台账先写（第一个副作用之前），中途失败也留得下"装到哪一步"。
	LEDGER_TMP=$(mktemp)
	SERVICE_CHOSEN=$SERVICE_WANTED
	write_ledger true "$LEDGER_VERSION" "$LEDGER_SHA"
	act user "写安装台账（先写 incomplete，中途失败也留痕）" ""

	# 1) 预检：不通过就不动服务、不换二进制（旧版本原样继续跑）
	precheck || die "预检没通过，什么都没动。改完配置再跑一次。"

	# 2) 停服务（Linux：Restart=always 没停就替换，会在替换瞬间被拉起来一次）
	state=$(service_state)
	if [ "$state" = "running" ] || [ "$state" = "installed" ]; then
		case "$SERVICE_KIND" in
		systemd)
			as_root systemctl disable --now "$UNIT_NAME" >/dev/null 2>&1 || true
			act root "停用服务（替换二进制前）" "sudo systemctl disable --now $UNIT_NAME"
			;;
		launchd)
			as_root launchctl bootout "system/$UNIT_NAME" >/dev/null 2>&1 || true
			act root "卸载 launchd 任务（替换二进制前）" "sudo launchctl bootout system/$UNIT_NAME"
			;;
		esac
	fi

	# 3) 放二进制：原子替换；旧的留 sol.bak
	mkdir_root "$BINDIR"
	if [ -f "$DEST_BIN" ] && [ "$(sha256 "$DEST_BIN")" = "$new_sha" ]; then
		say "二进制      已经就是这份（sha256 相同），不动"
	else
		if [ -f "$DEST_BIN" ]; then
			old_ver=$(binary_version "$DEST_BIN")
			as_root cp "$DEST_BIN" "$DEST_BIN.bak" 2>/dev/null || cp "$DEST_BIN" "$DEST_BIN.bak"
			act root "保留旧二进制（回滚用）" "sudo cp $DEST_BIN $DEST_BIN.bak"
			if [ -n "$old_ver" ] && [ "$old_ver" != "$new_ver" ]; then
				say "二进制      换版本：$old_ver → $new_ver"
			fi
		fi
		new_tmp=$(mktemp)
		cp "$LOCAL_BIN" "$new_tmp"
		chmod 0755 "$new_tmp"
		if [ -w "$BINDIR" ]; then
			mv "$new_tmp" "$DEST_BIN"
			act user "放置二进制（原子替换）" "mv <新二进制> $DEST_BIN"
		else
			as_root mv "$new_tmp" "$DEST_BIN"
			as_root chmod 0755 "$DEST_BIN"
			act root "放置二进制（原子替换）" "sudo mv <新二进制> $DEST_BIN"
		fi
		rm -f "$new_tmp"
		if [ "$SERVICE_KIND" = "launchd" ] && command -v xattr >/dev/null 2>&1; then
			# 下载来的二进制带 quarantine 标记，不清掉首次运行会被 Gatekeeper 拦住。
			xattr -d com.apple.quarantine "$DEST_BIN" 2>/dev/null || true
			act user "清 quarantine 标记（macOS）" "xattr -d com.apple.quarantine $DEST_BIN"
		fi
	fi

	# 4) 服务定义（只有选了服务才写）
	if [ "$SERVICE_CHOSEN" = true ]; then
		write_service
	fi

	# 5) 台账 + 历史（这次是完整的）
	write_ledger false "$LEDGER_VERSION" "$LEDGER_SHA"
	act user "更新安装台账（incomplete=false）" ""
	append_history "install"

	# 6) 回读：服务真起来了吗、跑的是不是新版
	RUNNING_VER=""
	case "$SERVICE_KIND" in
	systemd)
		if [ "$SERVICE_CHOSEN" = true ]; then
			if ! as_root systemctl is-active --quiet "$UNIT_NAME" 2>/dev/null; then
				warn "服务没有进入 active。看日志：sudo journalctl -u $UNIT_NAME -n 30"
			else
				RUNNING_VER="running"
			fi
		fi
		;;
	launchd)
		if [ "$SERVICE_CHOSEN" = true ]; then
			if ! launchctl print "system/$UNIT_NAME" >/dev/null 2>&1; then
				warn "launchd 任务没有在跑。看日志：$PREFIX/var/log/sol.log"
			else
				RUNNING_VER="running"
			fi
		fi
		;;
	esac
}

write_service() {
	case "$SERVICE_KIND" in
	systemd | launchd)
		mkdir_root "$(dirname "$UNIT_PATH")"
		unit_tmp=$(mktemp)
		render_unit >"$unit_tmp"
		put_root "$unit_tmp" "$UNIT_PATH" 0644
		rm -f "$unit_tmp"
		act root "写服务定义（从 run.args 生成）" "sudo cp <服务定义> $UNIT_PATH"
		;;
	esac
	case "$SERVICE_KIND" in
	systemd)
		if [ -n "$ROOT" ]; then
			warn "SOL_INSTALL_ROOT=$ROOT：单元文件写好了，但不注册服务（systemd 看不见它）。"
		elif command -v systemctl >/dev/null 2>&1; then
			as_root systemctl daemon-reload >/dev/null 2>&1 || true
			as_root systemctl enable --now "$UNIT_NAME" >/dev/null 2>&1 ||
				warn "systemctl enable --now $UNIT_NAME 失败，手动跑一遍看原因"
			act root "启用并启动服务" "sudo systemctl enable --now $UNIT_NAME"
		else
			warn "没有 systemctl（容器里？）：单元文件已写好，进了 systemd 的系统再 enable"
		fi
		;;
	launchd)
		if command -v launchctl >/dev/null 2>&1; then
			as_root launchctl bootout "system/$UNIT_NAME" >/dev/null 2>&1 || true
			as_root launchctl bootstrap system "$UNIT_PATH" >/dev/null 2>&1 ||
				warn "launchctl bootstrap 失败，手动跑一遍看原因"
			act root "加载 launchd 任务" "sudo launchctl bootstrap system $UNIT_PATH"
		fi
		;;
	esac
}

# ───────────────────────── 执行：卸载 ─────────────────────────

do_uninstall() {
	del_configs=no
	[ "$LEDGER_EXISTS" = yes ] || die "没有台账（$LEDGER），拒绝瞎删。
能看到的是：$( [ -n "$LOCAL_BIN" ] && printf '二进制 %s；' "$LOCAL_BIN" )$( [ -f "$UNIT_PATH" ] && printf '单元 %s；' "$UNIT_PATH" )
手工删除就是删这两样，再 systemctl disable --now $UNIT_NAME。"

	case "$SERVICE_KIND" in
	systemd)
		as_root systemctl disable --now "$UNIT_NAME" >/dev/null 2>&1 || true
		as_root systemctl reset-failed "$UNIT_NAME" >/dev/null 2>&1 || true
		act root "停用并禁用服务" "sudo systemctl disable --now $UNIT_NAME"
		rm_root "$UNIT_PATH"
		[ -f "$UNIT_PATH" ] || act root "删单元文件" "sudo rm $UNIT_PATH"
		as_root systemctl daemon-reload >/dev/null 2>&1 || true
		act root "重载 systemd" "sudo systemctl daemon-reload"
		;;
	launchd)
		as_root launchctl bootout "system/$UNIT_NAME" >/dev/null 2>&1 || true
		act root "卸载 launchd 任务（先 bootout 再删，否则 KeepAlive 会拉回来）" "sudo launchctl bootout system/$UNIT_NAME"
		rm_root "$UNIT_PATH"
		act root "删 plist" "sudo rm $UNIT_PATH"
		;;
	esac
	bin=$(ledger_field binary)
	[ -n "$bin" ] || bin="$BINDIR/sol"
	if [ -f "$bin" ]; then
		rm_root "$bin" "$bin.bak"
		act root "删二进制与 sol.bak" "sudo rm $bin $bin.bak"
	fi

	# 配置与日志默认留着；问了才删
	del_configs=no
	if ask "配置文件也一起删掉吗？" n; then del_configs=yes; fi
	if [ "$del_configs" = yes ]; then
		if [ -f "$USER_CONFIG" ]; then
			rm -f "$USER_CONFIG"
			act user "删安装配置" "rm $USER_CONFIG"
		fi
		if [ -f "$RUN_CONFIG" ]; then
			rm_root "$RUN_CONFIG"
			act root "删运行配置" "sudo rm $RUN_CONFIG"
		fi
	else
		say "配置留着：$USER_CONFIG、$RUN_CONFIG"
		say "（要删：rm $USER_CONFIG $RUN_CONFIG）"
		act user "保留配置（未删）" "# 配置与日志保留"
	fi

	append_history "uninstall"
	rm_root "$LEDGER"
	act root "删台账" "sudo rm $LEDGER"
}

# ───────────────────────── 报告 ─────────────────────────

report() { # report <install|uninstall|none>
	case "$1" in
	install)
		head2 "== 完成 =="
		say "$(binary_version "$DEST_BIN" 2>/dev/null || echo '（读不到版本）') -> $DEST_BIN"
		if [ -f "$DEST_BIN.bak" ]; then say "（旧版本留在 $DEST_BIN.bak，回滚就是把它换回去）"; fi
		say ""
		say "安装配置   $USER_CONFIG        改这里，然后重跑脚本"
		say "运行配置   $RUN_CONFIG$( [ "$RUNTIME_CONFIG_GENERATED" = yes ] && printf '（刚生成的最小配置：一条 noop 规则，按需改）' )"
		say "台账       $LEDGER"
		say "历史       $HISTORY（完整动作清单，带等价命令）"
		say ""
		say "校验"
		say "  $DEST_BIN --version   -> $(binary_version "$DEST_BIN" 2>/dev/null)"
		if [ -n "$ROOT" ]; then
			say "  （SOL_INSTALL_ROOT 下不注册服务；单元文件在 $UNIT_PATH）"
		elif [ "$SERVICE_CHOSEN" = true ]; then
			case "$SERVICE_KIND" in
			systemd) say "  systemctl is-active $UNIT_NAME   -> $( (as_root systemctl is-active "$UNIT_NAME" 2>/dev/null) || echo '未知')" ;;
			launchd) say "  launchctl print system/$UNIT_NAME   -> $(launchctl print "system/$UNIT_NAME" >/dev/null 2>&1 && echo 'running' || echo '未知')" ;;
			esac
		fi
		say ""
		say "接下来"
		if [ -n "$ROOT" ]; then
			say "  （SOL_INSTALL_ROOT 下没注册服务，本机 systemctl 里找不到它）"
		else
			case "$SERVICE_KIND" in
			systemd) say "  systemctl status $UNIT_NAME" ;;
			launchd) say "  launchctl print system/$UNIT_NAME" ;;
			esac
		fi
		say "  重跑安装脚本可以随时看现状（它会先报再问；答 n 退出，什么都不改）"
		;;
	uninstall)
		head2 "== 已卸载 =="
		if [ -n "$ROOT" ]; then
			say "服务       SOL_INSTALL_ROOT 下没注册过（单元文件：$UNIT_PATH）"
		else
			say "服务       $(service_state)（$UNIT_NAME）"
		fi
		say "二进制     $([ -f "$BINDIR/sol" ] && echo "还在：$BINDIR/sol" || echo "已删除：$BINDIR/sol")"
		if [ "$del_configs" = yes ]; then
			say "配置       已按你的选择删除"
		else
			say "配置       保留：$USER_CONFIG、$RUN_CONFIG"
			say "           要删：rm $USER_CONFIG $RUN_CONFIG"
		fi
		say "历史       $HISTORY"
		;;
	none)
		head2 "== 什么都没做 =="
		say "配置在那里：$USER_CONFIG"
		say "改完重跑脚本即可；想看那么做的动作清单说一声——这次的没执行，也就没写历史。"
		;;
	esac
}

# ───────────────────────── 主流程 ─────────────────────────

ensure_runtime_config

# 已经被升权重跑（带答案进来的）：不重复展示、也不再问，直接执行。
if [ -n "$ELEVATED_ACTION" ]; then
	SERVICE_CHOSEN=false
	[ "${SERVICE_ARG:-${SOL_INSTALL_SERVICE:-no}}" = yes ] && SERVICE_CHOSEN=true
	case "$ELEVATED_ACTION" in
	install)
		say "（已用 sudo 升权执行：安装）"
		do_install
		report install
		;;
	uninstall)
		say "（已用 sudo 升权执行：卸载）"
		do_uninstall
		report uninstall
		;;
	*) die "未知的 SOL_INSTALL_ACTION：$ELEVATED_ACTION" ;;
	esac
	exit 0
fi

show_state
show_config


if [ "$LEDGER_EXISTS" = yes ]; then
	choice=$(ask_choice "检测到已安装 ${LEDGER_VERSION:-?}（$(service_state)）。
请选择：[回车 = 按配置应用 / u = 卸载 / r = 重新生成配置 / n = 退出]")
	case "$choice" in
	u | uninstall)
		escalate uninstall
		do_uninstall
		report uninstall
		;;
	r)
		rm -f "$USER_CONFIG"
		write_user_config
		CONFIG_GENERATED=yes
		say "已重新生成：$USER_CONFIG（重新跑一次脚本就会按它执行）"
		exit 0
		;;
	n | no | q)
		report none
		;;
	*)
		if [ "$LEDGER_CREATED_UNIT" = true ]; then SERVICE_WANTED=true; else SERVICE_WANTED=false; fi
		escalate install
		do_install
		if [ "$NOOP" = yes ]; then
			say "（什么都没改。想强制重写服务定义就选 r 重新生成配置，或先卸载。）"
			exit 0
		fi
		report install
		;;
	esac
	exit 0
fi

# 未安装
SERVICE_WANTED=false
if ask "把 sol 装成 $SERVICE_KIND 服务（开机自启、后台常驻）？" y; then SERVICE_WANTED=true; fi
if ! ask "执行吗？" y; then
	report none
	exit 0
fi

escalate install
do_install
if [ "$NOOP" = yes ]; then
	say "（什么都没改。想强制重写服务定义就选 r 重新生成配置，或先卸载。）"
	exit 0
fi
report install