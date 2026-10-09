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
	--sol-unit-name=*) SOL_UNIT_NAME=${arg#--sol-unit-name=} ;;
	--sol-root=*) SOL_INSTALL_ROOT=${arg#--sol-root=} ;;
	*) die "这个脚本不接受参数（${arg}）。所有选择都在生成的配置文件和问答里。" ;;
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

# ───────────────────────── 正在跑的那份 ─────────────────────────

# 我们自己那份二进制现在有几个进程在跑。只认"可执行文件就是它"：名字叫 sol 的别人的进程、
# 以及我自己的脚本，都与它无关（卸载时乱杀名字叫 sol 的进程是另一种事故）。
running_instances() { # running_instances <二进制完整路径>
	bin=$1
	[ -n "$bin" ] && [ -e "$bin" ] || return 0
	case "$(uname -s 2>/dev/null)" in
	Darwin) pgrep -f "^$bin( |\$)" 2>/dev/null || true ;;
	*)
		for d in /proc/[0-9]*; do
			p=${d#/proc/}
			[ "$p" = "$$" ] && continue
			# 比 argv[0]，而不是 readlink exe：cmdline 谁都能读，而 exe 链接在**非 root 读 root
			# 进程**时会被内核拒掉（读出来是空）——"现状"是在未升权时打印的，于是 Linux 上
			# 也看不见正在跑的那份（跟 Windows 上读不到 SYSTEM 进程的 .Path 是同一类错）。
			#
			# 让 head 去读这个文件，而不是用 shell 的 `< 重定向`：进程可能在 glob 展开和读取之间
			# 就退出了，而**重定向失败是 shell 自己报的错**，挂在命令上的 2>/dev/null 压不住它
			# （真机上就冒过两行"/proc/<pid>/cmdline: 没有那个文件或目录"）。命令读文件时，
			# 那个错归命令所有，2>/dev/null 就能压掉，读不到就当空。
			argv0=$(head -c 4096 "$d/cmdline" 2>/dev/null | tr '\0' '\n' | head -n1)
			if [ -n "$argv0" ]; then
				[ "$argv0" = "$bin" ] && printf '%s\n' "$p"
			else
				# hidepid 之类读不到 cmdline 时退回 exe 链接（升权后还能用）
				[ "$(readlink "$d/exe" 2>/dev/null || true)" = "$bin" ] && printf '%s\n' "$p"
			fi
		done
		;;
	esac
}

log_hint() { # 服务日志在哪看：launchd 写文件，systemd 进 journal
	case "$SERVICE_KIND" in
	launchd) printf '%s' "$PREFIX/var/log/sol.log" ;;
	systemd) printf '%s' "journalctl -u $UNIT_NAME -f" ;;
	*) printf '%s' "（$SERVICE_KIND 的日志位置未知）" ;;
	esac
}

# 先 TERM 再 KILL。为什么要停：① 卸载时要删这个 exe，进程活着文件删不掉；
# ② 更要紧的是它一直占着 UDP 端口，下一次安装的预检绑不上，看起来就是"端口占用"。
stop_instances() { # stop_instances <二进制完整路径> <为什么>
	bin=$1
	why=$2
	pids=$(running_instances "$bin" | tr '\n' ' ')
	pids=${pids% }
	[ -n "$pids" ] || return 0
	say "  先停掉还在跑的那份（${why}）：pid $pids"
	for p in $pids; do as_root kill -TERM "$p" 2>/dev/null || true; done
	i=0
	while [ "$i" -lt 10 ]; do
		[ -z "$(running_instances "$bin")" ] && break
		sleep 1
		i=$((i + 1))
	done
	left=$(running_instances "$bin" | tr '\n' ' ')
	left=${left% }
	if [ -n "$left" ]; then
		for p in $left; do as_root kill -KILL "$p" 2>/dev/null || true; done
		act root "强杀没退出的 sol 进程（pid ${left}）" "sudo kill -9 $left"
	else
		act root "停掉还在跑的 sol 进程（pid ${pids}）" "sudo kill $pids"
	fi
	sleep 1
}

# 升级/预检前把**我们装的那个服务**先停下来。
# 为什么光 kill 进程不够：systemd 的 Restart=、launchd 的 KeepAlive 会在毫秒级把它拉回来，端口
# 立刻又被占上——预检第二次照样绑不上，升级就卡在"端口占用"上（真机上就是这么死的）。
# 用 stop 而不是 disable：万一预检还是不过，restore_service 一 start 就回到原样，开机自启也没丢。
suspend_service() {
	[ -n "$ROOT" ] && return 0
	[ "$LEDGER_CREATED_UNIT" = true ] || return 0
	case "$SERVICE_KIND" in
	systemd)
		command -v systemctl >/dev/null 2>&1 || return 0
		as_root systemctl stop "$UNIT_NAME" >/dev/null 2>&1 || true
		act root "停掉我们装的服务（升级前；不停它会被自动拉起，端口一直占着）" "sudo systemctl stop $UNIT_NAME"
		;;
	launchd)
		command -v launchctl >/dev/null 2>&1 || return 0
		as_root launchctl bootout "system/$UNIT_NAME" >/dev/null 2>&1 || true
		act root "摘掉我们装的 launchd 任务（升级前）" "sudo launchctl bootout system/$UNIT_NAME"
		;;
	esac
	sleep 1
}

# 预检失败时把刚才我们停掉的服务按原样起回去：单元/配置/二进制都还没动过，所以起回来就是原样。
# SOL_INSTALL_ROOT 下不碰本机服务管理器（那是别人的地盘）。
restore_service() {
	[ -n "$ROOT" ] && return 0
	[ -f "$UNIT_PATH" ] || return 0
	case "$SERVICE_KIND" in
	systemd)
		as_root systemctl start "$UNIT_NAME" >/dev/null 2>&1 || true
		act root "把刚才停掉的服务起回来" "sudo systemctl start $UNIT_NAME"
		;;
	launchd)
		as_root launchctl bootstrap system "$UNIT_PATH" >/dev/null 2>&1 || true
		act root "把刚才停掉的任务起回来" "sudo launchctl bootstrap system $UNIT_PATH"
		;;
	esac
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
		# SERVICE_WANTED 才是"问题的答案"；SERVICE_CHOSEN 要到 do_install 里才同步，
		# 在这个时点读它永远还是 false（会算出"不需要 root"，或者给子进程传错答案）。
		[ "$SERVICE_WANTED" = true ] && needs=yes
		;;
	esac
	[ "$needs" = yes ] || return 0

	# 管道执行（curl | sh）没有可重跑的脚本文件，只能退回逐条 sudo。
	self="$SCRIPT_DIR/$(basename "$0")"
	if [ ! -f "$self" ]; then
		warn "这一步要 root，但脚本是管道执行的、没法自己升权：下面需要权限的步骤会逐条 sudo。"
		return 0
	fi
	[ -n "$SUDO" ] || die "这一步需要 root（落点 ${BINDIR}、服务 ${UNIT_NAME}）。请用 root 角色重跑，或把落点换到你自己有写权限的目录。"

	say ""
	say "这一步要写 $BINDIR 并注册 $SERVICE_KIND 服务，需要 root。"
	say "我用 sudo 重新执行一遍自己：同一个目录、你刚才的答案带过去，不会再问一遍。"
	if [ "$SERVICE_WANTED" = true ]; then svc=yes; else svc=no; fi
	# exec 会顶掉当前进程：先放锁（子进程会自己重新拿），否则父进程的 trap 永远不会跑。
	rm -rf "$LOCK_DIR"
	exec "$SUDO" "$self" "--sol-action=$action" "--sol-service=$svc" "--sol-run-dir=$RUN_DIR" \
		"--sol-unit-name=$UNIT_NAME" "--sol-root=$ROOT"
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
# 输出（stdout+stderr）留到 ${TMPDIR_OUT}，返回值：0 = 还在跑（起来了），1 = 自己退出了。
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
	# 只看 pid 活着是不够的：pid 会被复用，一个八竿子打不着的进程就能把锁"顶"住（真发生过）。
	# 所以还要看它到底在不在跑这只脚本。
	owner_cmd=$( [ -n "$owner" ] && ps -p "$owner" -o args= 2>/dev/null || true )
	case "$owner_cmd" in
	*install.sh*)
		die "另一个安装/卸载正在进行（pid ${owner}，${LOCK_DIR}）。确定没有的话：rm -rf $LOCK_DIR"
		;;
	esac
	if [ -n "$owner_cmd" ]; then
		warn "锁里的 pid ${owner} 还活着，但它跑的不是本脚本（pid 被复用了），接管它。"
	else
		warn "发现上次留下的锁（pid ${owner:-未知} 已经不在了），接管它。"
	fi
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
	[ "$first" = "listen" ] || die "run.args 第一个词必须是 listen（当前是 ${first}）"
	known=$("$LOCAL_BIN" listen --help 2>/dev/null | grep -oE -- '--[a-zA-Z][a-zA-Z-]*' | sort -u)
	for tok in $ARGS; do
		case "$tok" in
		-*) printf '%s\n' "$known" | grep -qx -- "$tok" || die "run.args 里有 sol listen 不认识的参数：${tok}（见 $LOCAL_BIN listen --help）" ;;
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

# sol.yaml：一份开箱即用的配置（纯包关机 / magic+"reboot" 重启 / magic+"sleep" 睡眠）。
# 没有才写，绝不覆盖。
RUNTIME_CONFIG_GENERATED=no
ensure_runtime_config() {
	[ -f "$RUN_CONFIG" ] && return 0
	runtime_tmp=$(mktemp)
	cat >"$runtime_tmp" <<'YAML'
# 纯包 -> 关机；magic+"reboot" -> 重启；magic+"sleep" -> 睡眠
version: 1
server:
  interfaces: []
  # HTTP 控制面（默认关闭）：要用就把 enabled 改成 true，并给它一个令牌——令牌来自环境变量或
  # 0600 文件，绝不写进这个文件。三端的设置办法与 curl 用法见 README 的 Control plane 一节。
  http:
    enabled: false
    listen: 127.0.0.1:8080
    # auth: { type: bearer, token_env: SOL_TOKEN }
rules:
  - match: { ports: [11], content: { kind: none } }
    action: power.shutdown
  - match: { ports: [12], content: { kind: none } }
    action: power.reboot
  - match: { ports: [10], content: { kind: none } }
    action: power.sleep
# 重复包保护（默认就开着，可关）：电源动作各 5s 冷却；刚开机 / 刚唤醒 5s 内不执行电源动作。
# 想关掉：settle 写 0、把对应动作写 0s。
security:
  settle: 5s
  cooldowns: { power.sleep: 5s, power.shutdown: 5s, power.reboot: 5s }
YAML
	mkdir_root "$(dirname "$RUN_CONFIG")"
	put_root "$runtime_tmp" "$RUN_CONFIG" 0644
	rm -f "$runtime_tmp"
	RUNTIME_CONFIG_GENERATED=yes
}

# 从运行配置里取出端口。两种写法都要认：
#   - match: { ports: [10010] }     （行内，示例与 README 用的就是这种）
#   ports:                          （块写法）
#     - 10
# 只认行首 `ports:` 会让行内写法静默失效——低端口警告和防火墙规则就都没了。
all_ports() {
	[ -f "$RUN_CONFIG" ] || return 0
	sed -n 's/.*ports:[[:space:]]*\[\([0-9, ]*\)\].*/\1/p; s/^[[:space:]]*-[[:space:]]*\([0-9][0-9]*\)[[:space:]]*$/\1/p' "$RUN_CONFIG" |
		tr ',' '\n' | tr -s ' \n' '\n' | grep -v '^$' | sort -u
}

ports_below_1024() {
	for n in $(all_ports); do
		[ "$n" -lt 1024 ] 2>/dev/null && return 0
	done
	return 1
}

# ───────────────────────── 现状／配置展示 ─────────────────────────

version_tag() { # version_tag <二进制路径> -> v0.0.0-…（去掉 "sol version" 前缀与后面的 sha 括注）
	[ -n "$1" ] || return 0
	binary_version "$1" 2>/dev/null | sed 's/.*version //; s/ .*//'
}

state_cn() { # state_cn <service_state 的值>：内部词不给用户看
	case "$1" in
	running) printf '在运行' ;;
	installed) printf '已安装、未运行' ;;
	absent) printf '没有安装' ;;
	unregistered) printf '沙箱里没注册' ;;
	*) printf '状态未知' ;;
	esac
}

banner() {
	ver=$(version_tag "$LOCAL_BIN")
	where=$( [ -n "$ROOT" ] && printf 'SOL_INSTALL_ROOT 沙箱' || printf '%s / %s' "$(uname -s 2>/dev/null)" "$SERVICE_KIND" )
	head2 "sol 安装脚本  |  ${ver:-没有找到 sol}  |  $where"
	say "改配置、重跑这个脚本，就是改 sol 的跑法。先报现状，再问你要做什么。"
}

# 选项写成编号列表，而不是一行用斜杠串起来的 回车/u/r/n——那行既难读也难记。
show_menu() {
	head2 "[选项]"
	say "  1  按配置应用（按 install.yaml 里的 run.args 装或升级）"
	say "  2  卸载（摘掉服务，按台账删掉自己建的东西）"
	say "  3  重新生成 install.yaml"
	say "  4  退出，什么都不改"
	say ""
}

show_state() {
	head2 "[现状]"
	if [ -n "$LOCAL_BIN" ]; then
		say "  二进制      $LOCAL_BIN"
		fits=$(binary_fits_host "$LOCAL_BIN")
		[ -n "$fits" ] && warn "${fits}——这份装不起来，去下本机架构的那个包。"
		say "  版本        $(version_tag "$LOCAL_BIN")（sha256 $(sha256 "$LOCAL_BIN" | cut -c1-12)…）"
	else
		say "  二进制      没有找到可用的 sol：脚本旁边、当前目录、\$PATH 里都没有"
	fi
	if [ -n "$PATH_SOLS" ]; then
		say "  PATH 上的 sol"
		printf '%s\n' "$PATH_SOLS" | while IFS= read -r p; do
			if [ "$p" = "$LOCAL_BIN" ]; then say "    $p  ← 生效"; else say "    $p"; fi
		done
	fi
	if [ "$LEDGER_EXISTS" = yes ]; then
		inc=$(ledger_field incomplete)
		[ -n "$LEDGER_VERSION" ] || LEDGER_VERSION=版本未知
		if [ "$inc" = true ]; then
			say "  已安装      ${LEDGER_VERSION}（上次没装完——再跑一次会补上）"
		else
			say "  已安装      ${LEDGER_VERSION}（由安装脚本安装）"
		fi
		say "  台账        $LEDGER"
	else
		say "  已安装      无台账（从没被这个脚本装过；下面按你已有的二进制处理）"
		if [ -f "$UNIT_PATH" ]; then
			warn "有单元文件但没有台账（${UNIT_PATH}）：不是我装的，默认不动它——想接管就选 1（会问你要不要覆盖）。
      要手工清掉：
        sudo systemctl disable --now $UNIT_NAME
        sudo rm $UNIT_PATH
        sudo systemctl daemon-reload"
		fi
	fi
	case "$(service_state)" in
	running) say "  服务        ${SERVICE_KIND}，在运行（${UNIT_NAME}）" ;;
	installed) say "  服务        ${SERVICE_KIND}，已安装但没在运行（${UNIT_NAME}）" ;;
	absent) say "  服务        没有（$UNIT_NAME 不存在）" ;;
	unregistered) say "  服务        SOL_INSTALL_ROOT 下只写单元文件，本机 systemd 里没有它" ;;
	*) say "  服务        状态未知（没有 systemctl / launchctl？）" ;;
	esac
	say "  日志        $(log_hint)"
	inst_bin=$(ledger_field binary)
	[ -n "$inst_bin" ] || inst_bin="$BINDIR/sol"
	inst_n=$(running_instances "$inst_bin" | wc -l | tr -d ' ')
	if [ "$inst_n" != "0" ]; then
		say "  进程        有 $inst_n 个在跑：${inst_bin}（要用到端口时会先停掉它）"
	else
		say "  进程        没有在跑"
	fi
	[ -n "$MANAGED_BY" ] && say "  包管理器    这个 sol 是 $MANAGED_BY 装的（升级会覆盖它）"
	if [ "$UID_NOW" = "0" ]; then
		say "  权限        root（写系统路径与注册服务不需要再升权）"
	elif [ -n "$ROOT" ]; then
		say "  权限        普通用户 + SOL_INSTALL_ROOT=${ROOT}（自己的地盘，不会 sudo）"
	else
		say "  权限        普通用户（要写系统路径/注册服务时，动手前会用 sudo 重跑一遍自己）"
	fi
	if [ -f "$RUN_CONFIG" ]; then
		if [ "$RUNTIME_CONFIG_GENERATED" = yes ]; then
			say "  运行配置    ${RUN_CONFIG}（刚生成的示例配置：三条规则，按需改）"
		else
			say "  运行配置    ${RUN_CONFIG}（sol 会读它）"
		fi
	else
		say "  运行配置    还不存在：$RUN_CONFIG"
	fi
}

show_config() {
	head2 "[安装配置]"
	say "  $USER_CONFIG"
	say ""
	sed 's/^/    /' "$USER_CONFIG"
	say ""
	if [ "$CONFIG_GENERATED" = yes ]; then
		say "  （刚生成的。改它，然后重新跑脚本；脚本不会覆盖已存在的这份文件。）"
	fi
}

# ───────────────────────── 问答 ─────────────────────────

# 问答从 stdin 读：终端里跑就是键盘，`printf 'y\n' | sh install.sh` 就是那行答案，
# `curl | sh` 时 stdin 是脚本本身、读到的不是 y，于是什么都不会执行——三个场景都安全。
READ_EOF=no
# 脚本是文件还是喂进来的（curl | sh）：$0 是 sh/dash 而不是路径，就说明没有文件可重跑。
PIPED=yes
[ -f "$0" ] && PIPED=no   # `sh install.sh` 时 $0 可能只是 "install.sh"，所以看"是不是文件"，不看斜杠
PIPED_HINT=no

read_answer() { # read_answer <提示>：提示一律写 stderr，答案才走 stdout。
	printf '%s ' "$1" >&2
	if [ "$PIPED" = yes ] && [ ! -t 0 ]; then
		# `curl … | sh`：stdin 里是脚本本身，读它只会读到源码，永远读不到你。
		# 去 /dev/tty 问。问不到（真没有终端）就不猜——绝不替你按回车。
		if [ -r /dev/tty ] && read -r ans </dev/tty 2>/dev/null; then
			printf '\n' >&2
		else
			READ_EOF=yes
			ans=""
			if [ "$PIPED_HINT" = no ]; then
				printf '  ⚠ 管道执行（curl | sh）时问不到你。先把脚本存成文件再跑：\n    curl -fsSL <install.sh 的地址> -o install.sh && sh install.sh\n' >&2
				PIPED_HINT=yes
			fi
		fi
	elif ! read -r ans; then
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

ask_choice() { # ask_choice <提示> -> 打印一个词：apply | uninstall | regenerate | quit
	# 只认序号（回车＝1），同时容忍单字母的旧习惯；看不懂就重问，绝不猜。
	i=0
	while [ "$i" -lt 5 ]; do
		READ_EOF=no
		read_answer "$1 [1]"
		# 没人的话（EOF、管道）就什么也别做，当作"退出"
		if [ "$READ_EOF" = yes ]; then printf 'quit'; return 0; fi
		[ -n "$ans" ] || ans=1
		case "$ans" in
		1 | apply) printf 'apply'; return 0 ;;
		2 | u | uninstall) printf 'uninstall'; return 0 ;;
		3 | r | regenerate) printf 'regenerate'; return 0 ;;
		4 | n | no | q | quit) printf 'quit'; return 0 ;;
		*) printf '  请输入 1-4 之间的序号（回车＝1）。\n' >&2 ;;
		esac
		i=$((i + 1))
	done
	printf 'quit'
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

# ── 架构对不上：提前说人话 ──
# 下错架构的包（比如在 Intel Mac 上解了 darwin-arm64）真跑起来的报错是 "bad CPU type in executable"
# 这种一句话，用户看不懂。读 ELF / Mach-O 头里的 machine 字段就能提前判断。

binary_arch() { # binary_arch <文件>：x86_64 / arm64 / 空（不是认识的 ELF / Mach-O）
	f=$1
	[ -f "$f" ] || return 0
	magic=$(od -An -tx1 -N4 "$f" 2>/dev/null | tr -d ' \n')
	case "$magic" in
	7f454c46) # ELF：e_machine 在偏移 18，2 字节小端
		em=$(od -An -tx1 -j18 -N2 "$f" 2>/dev/null | tr -d ' \n')
		case "$em" in
		3e00) printf '%s' x86_64 ;;
		b700) printf '%s' arm64 ;;
		*) printf '' ;;
		esac
		;;
	cffaedfe|cefaedfe) # Mach-O 64 位小端：cputype 在偏移 4
		ct=$(od -An -tx1 -j4 -N4 "$f" 2>/dev/null | tr -d ' \n')
		case "$ct" in
		07000001) printf '%s' x86_64 ;;
		0c000001) printf '%s' arm64 ;;
		*) printf '' ;;
		esac
		;;
	*) printf '' ;;
	esac
}

host_arch() { # 统一成 x86_64 / arm64
	m=$(uname -m 2>/dev/null || printf '')
	case "$m" in
	x86_64|amd64) printf '%s' x86_64 ;;
	arm64|aarch64) printf '%s' arm64 ;;
	*) printf '%s' "$m" ;;
	esac
}

binary_fits_host() { # '' = 对得上；否则给一句能直接念给用户听的原因
	ba=$(binary_arch "$1")
	[ -n "$ba" ] || return 0
	ha=$(host_arch)
	[ "$ba" = "$ha" ] && return 0
	printf '这份二进制是 %s 的，本机是 %s（多半是下错架构的包了）' "$ba" "$ha"
}

precheck() { # 用同一个二进制 + 同一份配置起一次（dry-run：匹配了也不会真做事）
	[ -n "$LOCAL_BIN" ] || return 0
	# 架构对不上就别装了：真跑起来只会给一句 "bad CPU type" 之类的天书。
	fits=$(binary_fits_host "$LOCAL_BIN")
	if [ -n "$fits" ]; then
		die "${fits}。
  $LOCAL_BIN
本机架构：$(host_arch)。发布包是 sol-<版本>-$(uname -s | tr 'A-Z' 'a-z')-$(host_arch).tar.gz——别拿错；真要跨架构用，就把 install.yaml 的 run.args 指向本机架构那份二进制。"
	fi
	if [ ! -f "$RUN_CONFIG" ]; then
		warn "运行配置 $RUN_CONFIG 还不存在，没法预检；服务起来后会立刻退出（sol 拒绝在没有规则时启动）"
		return 1
	fi
	# shellcheck disable=SC2086
	if run_limited 3 "$LOCAL_BIN" $ARGS; then
		say "预检        通过（sol 用这份配置起来了 3 秒，匹配了也不会真做事：listen 是 dry-run）"
		return 0
	fi
	# 有用的那行在末尾（比如 bind: permission denied），所以打印**尾部**而不是开头几行：
	# 只给前 3 行时，报出来的全是 "using interface" 之类的提示，真正的原因被藏在下面。
	say "预检        被拒绝（sol 用这份配置起来又退出了）："
	tail -n 15 "$TMPDIR_OUT" 2>/dev/null | sed 's/^/            /'
	say "            （要改端口/接口就编辑上面那份运行配置，改完重跑这个脚本）"
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
  "log_paths": [$( [ "$SERVICE_KIND" = launchd ] && printf '"%s"' "$PREFIX/var/log/sol.log" )],
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

# 所有路径都要用到的变量一律先有值：升权分支（root）不走"未安装"那几个赋值语句，
# 少一个就是 set -u 当场炸死（而且只在有 root 的那条路上炸，很难撞见）。
SERVICE_CHOSEN=false
SERVICE_WANTED=false
DEST_BIN=""
NOOP=no
del_configs=no

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
			# launchd 要日志文件的目录先存在，否则任务起不来（这是 macOS 上最容易踩的一脚）。
			printf '  <key>StandardOutPath</key><string>%s/var/log/sol.log</string>\n' "$PREFIX"
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

	# 别人的服务定义默认不覆盖——但**要问一句**：用户明确说要接管，就该能接管（死路最糟）。
	if [ "$SERVICE_WANTED" = true ] && [ -f "$UNIT_PATH" ] && [ "$LEDGER_CREATED_UNIT" != true ]; then
		say ""
		warn "已经有一份 ${UNIT_NAME}（${UNIT_PATH}），但它不是这个脚本建的（没有台账，或台账说不是我们建的）。默认不碰别人建的东西。"
		if ask "要我接管它吗？接管后按这份 install.yaml 覆盖它的定义，卸载时也会一起摘掉" n; then
			say "      好，接管它（历史里会写明是接管来的，不是脚本新建的）。"
		else
			die "没有接管，什么都没动。手工清掉：sudo systemctl disable --now $UNIT_NAME && sudo rm $UNIT_PATH && sudo systemctl daemon-reload；或换个名字装：SOL_UNIT_NAME=sol-mine.service sh $0"
		fi
	fi

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

	# 1) 预检：不通过就不动服务、不换二进制（旧版本原样继续跑）。
	#    例外：升级时几乎必然"绑不上端口"——因为正在跑的那份是我们自己的二进制，占着同一个端口。
	#    那就先停它再试一次；还不过就把它按原样起回去，不让你白白少一个正在跑的服务。
	if ! precheck; then
		bin_now=$(ledger_field binary)
		[ -n "$bin_now" ] || bin_now="$DEST_BIN"
		if [ -n "$(running_instances "$bin_now")" ]; then
			say ""
			say "预检绑不上端口，而我们有份 sol 正在跑——多半就是它占着。先停掉它再试一次："
			suspend_service
			stop_instances "$bin_now" "它占着端口，而预检要绑同一个端口"
			if ! precheck; then
				restore_service
				die "预检没通过（停掉旧的再试也一样）。刚才停掉的那份我已经按原样起回去了；改完配置再跑一次。"
			fi
		else
			die "预检没通过，什么都没动。改完配置再跑一次。"
		fi
	fi

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
		if [ "$SERVICE_KIND" = launchd ]; then
			# launchd 在启动任务之前要求日志文件所在目录已存在，否则任务直接起不来。
			mkdir_root "$PREFIX/var/log"
			act root "建日志目录（launchd 需要它存在）" "sudo mkdir -p $PREFIX/var/log"
		fi
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
			warn "SOL_INSTALL_ROOT=${ROOT}：单元文件写好了，但不注册服务（systemd 看不见它）。"
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
	if [ "$LEDGER_EXISTS" != yes ]; then
		# 没有台账不等于"不能清"：默认不瞎删，但把"会动什么"摆出来问一句——用户说要，就清。
		say ""
		warn "没有台账（${LEDGER}）：这个脚本没在这台机器上装过 sol。默认我不瞎删。"
		say "      按默认落点，会动的是："
		[ -f "$UNIT_PATH" ] && say "        · 单元 ${UNIT_PATH}"
		[ -n "$LOCAL_BIN" ] && say "        · 执行目录里那份 ${LOCAL_BIN}"
		say "        · ${BINDIR}/sol、防火墙规则（如果加过）、以及 PATH 里那一行"
		if ask "要我按上面的默认落点强制清理吗" n; then
			say "      好，强制清理（历史里会记成无台账的强制清理）。"
		else
			die "没有清理，什么都没动。手工删上面那几样，再 sudo systemctl disable --now ${UNIT_NAME}。"
		fi
	fi

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
	# 先停进程，再删东西：正在跑的 exe 删不掉（文件锁），而且它会一直占着端口——
	# 下次安装的预检就会以"端口占用"失败，看起来像是装不上。
	bin=$(ledger_field binary)
	[ -n "$bin" ] || bin="$BINDIR/sol"
	stop_instances "$bin" "卸载要删掉它，也不能让它继续占着端口"
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
		say "配置留着：${USER_CONFIG}、$RUN_CONFIG"
		say "（要删：rm $USER_CONFIG ${RUN_CONFIG}）"
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
		head2 "[完成]"
		say "  版本        $(version_tag "$DEST_BIN" || echo '（读不到版本）')"
		say "  二进制      $DEST_BIN"
		if [ -f "$DEST_BIN.bak" ]; then say "  （旧版本留在 $DEST_BIN.bak，回滚就是把它换回去）"; fi
		say ""
		say "  安装配置    $USER_CONFIG        改这里，然后重跑脚本"
		say "  运行配置    $RUN_CONFIG$( [ "$RUNTIME_CONFIG_GENERATED" = yes ] && printf '（刚生成的示例配置：三条规则，按需改）' )"
		say "  台账        $LEDGER"
		say "  服务日志    $(log_hint)"
		say "  历史        ${HISTORY}（完整动作清单，带等价命令）"
		say ""
		say "  校验"
		say "    $DEST_BIN --version   -> $(version_tag "$DEST_BIN")"
		if [ -n "$ROOT" ]; then
			say "    （SOL_INSTALL_ROOT 下不注册服务；单元文件在 ${UNIT_PATH}）"
		elif [ "$SERVICE_CHOSEN" = true ]; then
			case "$SERVICE_KIND" in
			systemd) say "    systemctl is-active $UNIT_NAME   -> $( (as_root systemctl is-active "$UNIT_NAME" 2>/dev/null) || echo '未知')" ;;
			launchd) say "    launchctl print system/$UNIT_NAME   -> $(launchctl print "system/$UNIT_NAME" >/dev/null 2>&1 && echo 'running' || echo '未知')" ;;
			esac
		fi
		say ""
		say "  接下来"
		if [ -n "$ROOT" ]; then
			say "    （SOL_INSTALL_ROOT 下不注册服务：单元文件是 ${UNIT_PATH}，本机服务管理器里找不到它）"
		else
			case "$SERVICE_KIND" in
			systemd) say "    systemctl status $UNIT_NAME" ;;
			launchd) say "    launchctl print system/$UNIT_NAME" ;;
			esac
		fi
		say "    重跑安装脚本可以随时看现状（它会先报再问；选 4 退出，什么都不改）"
		;;
	uninstall)
		head2 "[已卸载]"
		if [ -n "$ROOT" ]; then
			say "  服务        SOL_INSTALL_ROOT 下没注册过（单元文件：${UNIT_PATH}）"
		else
			say "  服务        $(state_cn "$(service_state)")（${UNIT_NAME}）"
		fi
		say "  二进制      $([ -f "$BINDIR/sol" ] && echo "还在：$BINDIR/sol" || echo "已删除：$BINDIR/sol")"
		if [ "$del_configs" = yes ]; then
			say "  配置        已按你的选择删除"
		else
			say "  配置        保留：${USER_CONFIG}、$RUN_CONFIG"
			say "              要删：rm $USER_CONFIG $RUN_CONFIG"
		fi
		say "  历史        $HISTORY"
		;;
	none)
		head2 "[退出]"
		say "  什么都没做，也没写历史。配置在那里：$USER_CONFIG"
		say "  改完重跑脚本即可。"
		;;
	esac
}

# ───────────────────────── 主流程 ─────────────────────────

ensure_runtime_config

# 已经被升权重跑（带答案进来的）：不重复展示、也不再问，直接执行。
if [ -n "$ELEVATED_ACTION" ]; then
	SERVICE_CHOSEN=false
	SERVICE_WANTED=false
	if [ "${SERVICE_ARG:-${SOL_INSTALL_SERVICE:-no}}" = yes ]; then
		SERVICE_CHOSEN=true
		SERVICE_WANTED=true
	fi
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

banner
show_state
show_config


if [ "$LEDGER_EXISTS" = yes ]; then
	say "检测到已安装 ${LEDGER_VERSION:-?}（服务：$(state_cn "$(service_state)")）。"
	show_menu
	choice=$(ask_choice "请选择")
	case "$choice" in
	uninstall)
		escalate uninstall
		do_uninstall
		report uninstall
		;;
	regenerate)
		rm -f "$USER_CONFIG"
		write_user_config
		CONFIG_GENERATED=yes
		say "已重新生成：${USER_CONFIG}（重新跑一次脚本就会按它执行）"
		exit 0
		;;
	quit)
		report none
		;;
	*)
		if [ "$LEDGER_CREATED_UNIT" = true ]; then
			SERVICE_WANTED=true
		elif [ -n "$ROOT" ] || [ "$SERVICE_KIND" = none ]; then
			SERVICE_WANTED=false
		else
			# 台账没说建过单元：可能当时你选了"不装服务"，也可能上次没跑完（单元不在）。
			# 那就再问一次——否则"我想装服务"在这条路上永远没机会说出口。
			SERVICE_WANTED=false
			if ask "把 sol 装成 $SERVICE_KIND 服务（开机自启、后台常驻）？" y; then SERVICE_WANTED=true; fi
		fi
		escalate install
		do_install
		if [ "$NOOP" = yes ]; then
			say "（什么都没改。想强制重写服务定义就选 3 重新生成配置，或先卸载。）"
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
	say "（什么都没改。想强制重写服务定义就选 3 重新生成配置，或先卸载。）"
	exit 0
fi
report install