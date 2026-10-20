#!/bin/sh
# sol 安装脚本 —— 零参数。
#
# 它只做三件事：认出现状 → 生成/读取"怎么跑"的配置 → 你确认之后才动手。
# 装完只有一个目录：**用户目录下的 sol**（三端一个约定）——
#   Linux / macOS  ~/sol/   sol  install.yaml  sol.yaml  install.json  install.log  sol.log
#   Windows        %USERPROFILE%\sol\   sol.exe  install.yaml  sol.yaml  install.json  install.log  sol.log
# 跟你在哪个目录执行脚本无关。设计文档（本脚本与它是同一份契约）：docs/install-design.md
#
#   sh install.sh              生成配置 → 展示现状 → 问一句 → 执行
#   printf 'y\n' | sh install.sh   CI / 无人值守：管道喂答案
#   没有终端时只生成配置，不执行任何操作（绝不猜"用户大概是想装"）
#
# 环境变量（都有默认值；普通用户不需要设，测试与多实例用）：
#   SOL_INSTALL_ROOT  把系统路径挂到另一个根下（默认 /，DESTDIR 风格）
#   SOL_UNIT_NAME     服务名（Linux 默认 sol.service，macOS 默认 com.lidiaoo.sol）
#   SOL_RUN_DIR       落点目录本身（默认 ~/sol；sudo 下 HOME 变成 /root，所以升权时显式传）
#   SOL_INSTALL_RUN_DIR / --run-dir  同 SOL_RUN_DIR（历史名字，留着兼容）
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
# uname -s 给的是 Linux / Darwin（首字母大写）。这里顺手留一份小写版给下面的 case 用：
# 两处都写死大写（或都写死小写），后代改一处漏一处就会在某平台上永远命不中默认分支——
# 真的发生过：模板选取那里写成小写，Linux 上直接 die"不认识的系统 Linux"。
OS_RAW=$(uname -s)
ARCH=$(uname -m)
case "$OS_RAW" in
Linux) SERVICE_KIND=systemd ;;
Darwin) SERVICE_KIND=launchd ;;
*) die "只支持 Linux 与 macOS（Windows 请用 install.ps1）。当前系统：$OS_RAW" ;;
esac
OS=$(printf '%s' "$OS_RAW" | tr 'A-Z' 'a-z')   # linux / darwin：后面按平台挑文件都用它
# 系统路径挂载点：默认就是 /（正常安装）；测试与 chroot 时可以指到别处。
ROOT=${SOL_INSTALL_ROOT:-}
if [ "$ROOT" = "/" ]; then ROOT=""; fi
if [ -n "$ROOT" ]; then
	[ -d "$ROOT" ] || die "SOL_INSTALL_ROOT=$ROOT 不存在"
	ROOT=$(cd "$ROOT" && pwd)
fi
# 落点（BINDIR / SHAREDIR / LEDGER / HISTORY / STATE_DIR / LOG_FILE）全部挂在用户目录下的 sol 里，
# 而用户目录要等下面的"平台与路径"第二段才算得出来，所以那一段挪到 RUN_DIR 之后。
# 这里只定服务定义的位置——它天然在系统目录（systemd / launchd 只认那几个地方）。
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
	launchd) printf '%s' "$LOG_FILE" ;;
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
# 落点只有一个：**当前用户目录下的 sol**，三端一个约定（Windows 是 %USERPROFILE%\sol）：「sol」
#   Linux / macOS  ~/sol/   sol  install.yaml  sol.yaml  install.json  install.log  sol.log
#   Windows        %USERPROFILE%\sol\   sol.exe  install.yaml  sol.yaml  install.json  install.log  sol.log
# 二进制、配置、台账、日志全在同一个目录里：卸载就是删这一个目录，"东西都装到哪去了"只有一句答案。
# 不按执行目录走：在下载目录里跑一次就在那儿多出一份，还各自有各自的令牌，谁也说不清算哪份。
# 服务定义**不在**这里：systemd / launchd 只认 /etc/systemd/system 与 /Library/LaunchDaemons，
# 那一步仍然要 root。服务里跑的是绝对路径，所以不踩"服务有自己的家目录"那个坑。
# 挂了自选的根（SOL_INSTALL_ROOT）时，用户目录默认也挂到根下，沙箱与多实例测试不会碰到真实的家目录。
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
HOME_NOW=${HOME:-/root}
# 落点里那几个固定名字：只在这里写一次。报告、报错文案、服务定义、打包自检都引用它们，
# 免得同一个 "sol.yaml" 在文件里散成十几处字面量、改一处漏一处。
DIR_NAME="sol"            # 用户目录下那个目录（~/sol；设了自选根时是 <根>/root/sol）
INSTALL_CONFIG_NAME="install.yaml"   # 安装配置："sol 怎么跑"
RUN_CONFIG_NAME="sol.yaml"           # 运行配置：sol 自己读的那份
LEDGER_NAME="install.json"           # 安装台账：装了什么、装到哪、上一版是什么
HISTORY_NAME="install.log"           # 完整动作清单，带等价命令
LOG_NAME="sol.log"                   # sol 自己的运行日志（服务把 stdout 送进这里）
case "$ROOT" in
'') HOME_NOW_DEFAULT=$HOME_NOW ;;
*) HOME_NOW_DEFAULT="$ROOT/root" ;;
esac
# --run-dir / SOL_INSTALL_RUN_DIR 原来只改 install.yaml 的目录、运行配置另算；现在两者是同一个
# 目录（用户目录下的 sol），所以它直接决定整个落点，别再分出第二个位置。
if [ -n "${RUN_DIR_ARG:-}" ]; then
	RUN_DIR=$RUN_DIR_ARG
elif [ -n "${SOL_RUN_DIR:-}" ]; then
	RUN_DIR=$SOL_RUN_DIR
else
	RUN_DIR="${HOME_NOW_DEFAULT%/}/$DIR_NAME"
fi
RUN_DIR=${RUN_DIR%/}   # 结尾的斜杠会让 $RUN_DIR/sol 变成 //sol（不影响用，只是难看）
# 落点全在这一个目录里。BINDIR / SHAREDIR 是历史名字（以前是 /usr/local/bin 与 /usr/local/share/sol），
# 保留是为了少改几处引用；它们现在都等于 RUN_DIR。
STATE_DIR=$RUN_DIR
BINDIR=$RUN_DIR
SHAREDIR=$RUN_DIR
LEDGER="$RUN_DIR/$LEDGER_NAME"
HISTORY="$RUN_DIR/$HISTORY_NAME"
LOG_FILE="$RUN_DIR/$LOG_NAME"
DEST_BIN="$RUN_DIR/sol"
USER_CONFIG="$RUN_DIR/$INSTALL_CONFIG_NAME"
# 早先的版本把二进制和台账放在系统落点（/usr/local/bin/sol、/usr/local/share/sol/install.json），
# 配置还有过 /usr/local/share/sol/sol.yaml。现在全挪进用户目录了，这几个只用于「认出旧安装、搬走、别留双份」。
LEGACY_BINDIR="$ROOT/usr/local/bin"
LEGACY_SHAREDIR="$ROOT/usr/local/share/sol"
LEGACY_LEDGER="$LEGACY_SHAREDIR/$LEDGER_NAME"
LEGACY_BIN="$LEGACY_BINDIR/sol"
# 运行配置的默认落点。**定义在这里**（不是后面"安装配置"那一节）：升权前那段"目录写不了"的
# 报错文案要用它，晚定义的变量在 die 里只会打出空字符串。
DEFAULT_CONFIG_PATH="$RUN_DIR/$RUN_CONFIG_NAME"
# 早先的安装把两份配置放在**执行目录**里（后来 sol.yaml 又搬到了系统落点）；这几个只用于把老内容搬过来。
LEGACY_USER_CONFIG="$(pwd)/$INSTALL_CONFIG_NAME"
LEGACY_RUN_CONFIG="$(pwd)/$RUN_CONFIG_NAME"
# 运行配置的模板：跟脚本放在一起的 sol.yaml（发布包里和 install.sh 同目录，开发树里在 scripts/ 下）。
# 执行目录里那份 sol.yaml 若与它一字不差，说明只是随包示例（令牌还是占位符），不搬。
SHIPPED_RUN_CONFIG=""
for cand in "$SCRIPT_DIR/$RUN_CONFIG_NAME" "$SCRIPT_DIR/scripts/$RUN_CONFIG_NAME"; do
	if [ -f "$cand" ]; then SHIPPED_RUN_CONFIG=$cand; break; fi
done
# 去 CR 的读法：下面几处比对、以及后面解析 args 都要用。**定义在第一次调用之前**——
# 函数体里的调用虽然延迟绑定，但顶层现在就调了 same_as_shipped，那时它还不会被定义。
_strip_cr() { tr -d '\r'; }
# 内容比对走两步：cmp 快（同一次解包出来的两份完全一样），不行再按文本比（容忍 CRLF 差异）。
same_as_shipped() { # same_as_shipped <候选文件> <模板文件>
	[ -f "$1" ] && [ -f "$2" ] || return 1
	cmp -s "$1" "$2" 2>/dev/null && return 0
	[ "$(_strip_cr <"$1")" = "$(_strip_cr <"$2")" ]
}
RUN_COPY_FROM=""
if [ ! -f "$DEFAULT_CONFIG_PATH" ]; then
	for cand in "$LEGACY_RUN_CONFIG" "$LEGACY_SHAREDIR/$RUN_CONFIG_NAME" "$ROOT/var/lib/sol/$RUN_CONFIG_NAME"; do
		[ -f "$cand" ] || continue
		# 执行目录里那份若是包自带的模板，说明它只是示例（令牌还是占位符），别拿它当配置。
		[ "$cand" = "$LEGACY_RUN_CONFIG" ] && [ -n "$SHIPPED_RUN_CONFIG" ] && same_as_shipped "$cand" "$SHIPPED_RUN_CONFIG" && continue
		RUN_COPY_FROM=$cand
		break
	done
fi
# ───────────────────────── 升权 ─────────────────────────
# 已经带答案升过权了（子进程）：不再问，直接做。
ELEVATED_ACTION=${ACTION_ARG:-${SOL_INSTALL_ACTION:-}}
# 现在落点（二进制、配置、台账、日志）全在用户目录下的 sol 里，写它**不需要 root**；
# 还要 root 的只剩"系统级服务定义"这一步（/etc/systemd/system、/Library/LaunchDaemons 只认 root）。
# 与其半路一条条 sudo（密码问一半、失败还被吞掉），不如动手之前一次性升权：把答案带过去重跑。
escalate() { # escalate <install|uninstall>
	action=$1
	[ "$UID_NOW" = "0" ] && return 0
	[ -n "$ROOT" ] && return 0 # 自选的根（沙箱 / chroot）：自己的地盘，不升权
	needs=no
	case "$action" in
	install)
		# SERVICE_WANTED 才是"问题的答案"；SERVICE_CHOSEN 要到 do_install 里才同步，
		# 在这个时点读它永远还是 false（会算出"不需要 root"，或者给子进程传错答案）。
		[ "$SERVICE_WANTED" = true ] && needs=yes
		;;
	uninstall)
		# 只有"确实有一份系统级服务定义要摘"时才需要 root；只是删用户目录里的文件不用。
		if [ "$LEDGER_CREATED_UNIT" = true ] || [ -f "$UNIT_PATH" ]; then needs=yes; fi
		;;
	esac
	[ "$needs" = yes ] || return 0
	# 管道执行（curl | sh）没有可重跑的脚本文件，只能退回逐条 sudo。
	self="$SCRIPT_DIR/$(basename "$0")"
	if [ ! -f "$self" ]; then
		warn "这一步要 root，但脚本是管道执行的、没法自己升权：下面需要权限的步骤会逐条 sudo。"
		return 0
	fi
	[ -n "$SUDO" ] || die "这一步需要 root（服务定义 ${UNIT_PATH}）。请用 root 角色重跑，或者装的时候不选服务（只把文件放进 $RUN_DIR）。"
	say ""
	say "这一步要写 $UNIT_PATH 并注册 $SERVICE_KIND 服务，需要 root（你的文件都在 $RUN_DIR 里，已经写好了）。"
	say "我用 sudo 重新执行一遍自己：同一处配置、你刚才的答案带过去，不会再问一遍。"
	if [ "$SERVICE_WANTED" = true ]; then svc=yes; else svc=no; fi
	# exec 会顶掉当前进程：先放锁（子进程会自己重新拿），否则父进程的 trap 永远不会跑。
	rm -rf "$LOCK_DIR"
	exec "$SUDO" env "SOL_RUN_DIR=$RUN_DIR" "$self" "--sol-action=$action" "--sol-service=$svc" "--sol-run-dir=$STATE_DIR" \
		"--sol-unit-name=$UNIT_NAME" "--sol-root=$ROOT"
}
# 落点写不进去（HOME 只读、或别人家的目录）时先说清楚，而不是跑到一半才死在 mkdir 上。
if [ -z "$ROOT" ] && ! writable_target "$RUN_DIR"; then
	die "在这个目录里写不了文件：$RUN_DIR（sol、$INSTALL_CONFIG_NAME、$RUN_CONFIG_NAME、台账、日志都放这儿）。
改一下 HOME（或者它的属主/权限），或者用 --run-dir 指定一个能写的目录再跑一次。"
fi
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
# ① 脚本旁边 ② 当前目录 ③ 已经装好的那份（$RUN_DIR/sol） ④ PATH。
# ③ 是为了「只重跑安装脚本、不重新下载包」也能升级：解压出来的包装过之后，包早就删了。
# 指到同一个文件的多次命中只算一次。
find_local_binary() {
	for c in "$SCRIPT_DIR/sol" "$PWD/sol" "$DEST_BIN"; do
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
# DEFAULT_CONFIG_PATH 在"平台与路径"一节就定好了（升权前的报错文案要用它），这里不再重算——
# 同一个路径定义两次，早晚会有一处忘改。$INSTALL_TEMPLATE_NAME / $RUN_CONFIG_NAME 同理。
DEFAULT_ARGS="listen --config $DEFAULT_CONFIG_PATH"          # 给人看、给报告用
DEFAULT_ARGS_YAML="listen, --config, $DEFAULT_CONFIG_PATH"   # 写进模板与报错提示：YAML 列表要逗号
# scripts/sol.yaml 跟脚本放在一起（开发树里在 scripts/ 下，发布包里被摊平到脚本旁边）。
TEMPLATE_CONFIG="$SCRIPT_DIR/$RUN_CONFIG_NAME"
[ -f "$TEMPLATE_CONFIG" ] || TEMPLATE_CONFIG="$SCRIPT_DIR/scripts/$RUN_CONFIG_NAME"
# 本平台的安装配置模板：随包带的那一份（发布包里和 install.sh 同目录，开发树里在 scripts/ 下）。
# 三个平台各一份、各自维护，注释和写法可以不一样；脚本只读自己那平台的，不认识的平台不猜。
case "$OS" in
linux) INSTALL_TEMPLATE_NAME="install-linux.yaml" ;;
darwin) INSTALL_TEMPLATE_NAME="install-macos.yaml" ;;
*) INSTALL_TEMPLATE_NAME="" ;;
esac
INSTALL_TEMPLATE=""
[ -n "$INSTALL_TEMPLATE_NAME" ] || die "不认识的系统 ${OS}：不知道怎么装。Linux 与 macOS 都用 install.sh，Windows 用 install.cmd。"
for cand in "$SCRIPT_DIR/$INSTALL_TEMPLATE_NAME" "$SCRIPT_DIR/scripts/$INSTALL_TEMPLATE_NAME"; do
	[ -f "$cand" ] && { INSTALL_TEMPLATE=$cand; break; }
done
[ -n "$INSTALL_TEMPLATE" ] || die "找不到本平台的安装配置模板 ${INSTALL_TEMPLATE_NAME}（应该跟 install.sh 放在一起）。重新下载一份发布包再装。"
# 解析 args 的几件工具必须先定义、再被调用：shell 跟 PowerShell 一样，函数是**执行到那一行**才存在的。
# 这里原来的顺序反了（膨胀成 args_of_file 在 490 行被调用、parse_args_of 却定义在 526 行），
# 于是 Linux 上必然报 "parse_args_of: command not found" → "这是坏包，重新下载"。整段提到前面。
parse_args_of() { # parse_args_of <文件>：args 的两端空格抹平后，一行一个 token
	sed -n '/^[[:space:]]*args:/p' "$1" |
		sed 's/^[[:space:]]*args:[[:space:]]*//' |
		tr -d '[]' |
		tr ',' '\n' |
		sed 's/^[[:space:]]*//; s/[[:space:]]*$//' |
		sed '/^$/d'
}
# 模板里的 args 一行是 YAML 流式列表，带引号；解析它和解析用户那份必须是同一套规则，所以共用
# parse_args_of——模板和用户配置的写法是同一套，没有第二套。读出来之后去掉外面的引号、展开
# ~ / $HOME（模板里这么写，换台机器、换用户都对），再跟默认值比。
# 注意 ~ 展开成 HOME_NOW_DEFAULT（= $HOME，设了 SOL_INSTALL_ROOT 时是 <根>/root），不是裸 $HOME：
# 默认落点 DEFAULT_CONFIG_PATH 就是按 HOME_NOW_DEFAULT 算的，两边用不同的家目录会在 chroot/沙箱里
# 比对失败，报出一句莫名其妙的"模板是坏包，重新下载"。
expand_args() {
	_home_for_tilde=${HOME_NOW_DEFAULT:-$HOME}
	sed -e 's/^"\(.*\)"$/\1/' -e "s/^'\\(.*\\)'\$/\\1/" |
		sed -e "s#^~/#$_home_for_tilde/#" -e "s#^~\$#$_home_for_tilde#" |
		sed -e "s#\$HOME#$_home_for_tilde#g"
}
# 一个文件里 args 的规整结果（一行 token，空格分隔）：读模板与判断"是不是示例"共用这一条规则。
args_of_file() { parse_args_of "$1" | expand_args | tr '\n' ' ' | sed 's/ *$//'; }
user_config_args() { args_of_file "$INSTALL_TEMPLATE"; }
# 模板必须写得能被本脚本读懂：读不出 args，或者 args 跟本脚本算出来的默认值对不上，都算坏包。
[ "$(user_config_args)" = "$DEFAULT_ARGS" ] ||
	die "本平台的安装配置模板 ${INSTALL_TEMPLATE} 里的 args 对不上（读到：$(user_config_args)）。这是坏包，重新下载。"
write_user_config() {
	mkdir -p "$(dirname "$USER_CONFIG")"
	cp "$INSTALL_TEMPLATE" "$USER_CONFIG"
	# 落点不是标准位置（SOL_RUN_DIR / 自选根 / 沙箱测试）时，模板里那句 ~/sol/sol.yaml 展开出来
	# 指向的是**这个用户的标准落点**，跟这份配置实际所在的位置对不上——预检当场读不到文件。
	# 所以一律改写成本机真实的绝对路径：以后无论谁去展开（脚本、服务定义、sudo 下的另一个 HOME），
	# 指的都是同一份文件。标准安装时两边本来就相等，这段不产生任何改动。
	# 目标路径就是 DEFAULT_CONFIG_PATH（sol.yaml 的真实落点，已经算进了 SOL_RUN_DIR / 自选根），
	# 不是解析出来的 $RUN_CONFIG——那个变量要等这份文件写出来之后才能解析，这时候还不存在。
	#
	# 这里必须比**字面量**，不能比 expand_args 展开后的结果：模板写的是 ~/sol/sol.yaml，展开后
	# 恰好等于默认落点，看着"已经对了"；可 sol 自己不展开 ~（resolvePath 原样返回），它拿到这个
	# 参数就是去开一个叫 "~" 的目录，当场 decode config 失败。所以只要文件里写的不是那条绝对
	# 路径，就改写成绝对路径——标准安装时这一行改动前后只差一个 ~ 的展开，语义不变。
	_literal_want="listen --config $DEFAULT_CONFIG_PATH"
	_literal_now=$(parse_args_of "$USER_CONFIG" | tr '\n' ' ' | sed 's/ *$//')
	if [ "$_literal_now" != "$_literal_want" ]; then
		# 只换 args 那一行的取值，别的字节一个都不动（注释、行尾、你自己的改动）。
		awk -v a="  args: [$DEFAULT_ARGS_YAML]" '
			!done && /^[[:space:]]*args:/ { print a; done = 1; next }
			{ print }
		' "$USER_CONFIG" >"$USER_CONFIG.tmp" && mv "$USER_CONFIG.tmp" "$USER_CONFIG"
	fi
}
# 这份内容和"我们现在会生成的那份"是否一样（忽略 CRLF）。老脚本写的注释措辞不同，所以分两步：
# ① 逐字相同；② 去掉注释与空行后只剩 run: 和那一行 args，且 args 与默认值一致。两条都不成立，
# 说明它承载了你的改动（改过 --config、加过别的键），那是真老配置，照搬。
is_stock_user_config() { # is_stock_user_config <文件>
	[ -f "$1" ] || return 1
	# ① 逐字相同（同一次生成出来的，最常见）
	[ "$(_strip_cr <"$1")" = "$(_strip_cr <"$INSTALL_TEMPLATE")" ] && return 0
	# ② 措辞可能不同（老脚本写的注释不一样）：只要"去掉注释和空行之后只剩下 run: 和那一行 args"，
	#    且 args 跟我们默认的一模一样，就认为它没有承载任何用户意图。
	_rest=$(_strip_cr <"$1" | sed -e 's/[[:space:]]*$//' | grep -v '^#' | grep -v '^[[:space:]]*$' || true)
	[ "$(printf '%s\n' "$_rest" | grep -cvE '^(run:|[[:space:]]+args:[[:space:]]*\[.*\])$' || true)" = "0" ] || return 1
	# 模板里的路径写成 ~/sol/sol.yaml，用户那份多半已展开成绝对路径：两边都走同一条规整规则再比。
	[ "$(args_of_file "$1")" = "$DEFAULT_ARGS" ]
}
# 把 args 解析成一行一个 token（只支持本脚本自己写出的那种写法，不猜别的）。
parse_args() { parse_args_of "$USER_CONFIG"; }
# 搬运（老位置 → 新位置）要在升权之前做：这两份文件属于**当前用户**，用 sudo 写会变成 root 的。
# 老内容原样拷过来（令牌、注释、你改过的规则都跟着走），绝不覆盖新位置上已有的文件。
CONFIG_GENERATED=no
LEGACY_USER_COPIED=""
if [ ! -f "$USER_CONFIG" ] && [ -f "$LEGACY_USER_CONFIG" ]; then
	if is_stock_user_config "$LEGACY_USER_CONFIG"; then
		: # 内容跟默认生成的那份一样（只有默认 args）：那是示例，不搬
	elif mkdir -p "$STATE_DIR" && cp "$LEGACY_USER_CONFIG" "$USER_CONFIG" 2>/dev/null; then
		LEGACY_USER_COPIED=$LEGACY_USER_CONFIG
	else
		warn "没能把 $LEGACY_USER_CONFIG 拷到 $USER_CONFIG，按新位置重新生成。"
	fi
	CONFIG_GENERATED=yes
fi
# 老位置的内容在升权之前就原样搬过来了（见"升权"一节前面那段）；这里只剩"还没有就写一份"。
[ -f "$USER_CONFIG" ] || { write_user_config; CONFIG_GENERATED=yes; }
[ -n "$LEGACY_USER_COPIED" ] && say "安装配置   沿用 $LEGACY_USER_COPIED（原样拷到 $USER_CONFIG；你另外指定的不受影响）"
# 搬过来的那份可能写着 ~/sol/sol.yaml（老模板就是教人这么写的）。**必须展开成绝对路径**：
# 这段文本最后是当命令行参数交给 sol 的，shell 不参与展开，sol 自己也不展开 ~（resolvePath 原样
# 返回）——它只会去开一个名字叫 "~" 的目录。真机上这么干过一次，结果在当时的执行目录里多出一个
# 字面量 ~/sol/sol.yaml，而"服务在跑、配置却读不到"。
normalize_args_tilde() { # normalize_args_tilde <文件>
	[ -f "$1" ] || return 0
	grep -q '^[[:space:]]*args:.*~/' "$1" || return 0
	_home_for_args=${HOME_NOW_DEFAULT:-$HOME}
	sed -i.bak "s#^\([[:space:]]*args:.*[ \[,]\)~/#\1${_home_for_args}/#g" "$1" && rm -f "$1.bak"
}
normalize_args_tilde "$USER_CONFIG"
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
# 老版本的默认值指的是执行目录里的 sol.yaml（后来是系统落点）。默认值跟着位置走，
# 所以只认这几样"我们以前替你写的"取值：认出来就改写成现在的位置，文件原样搬过去，改动不丢；
# 你自己另行指定的路径一律不动——那是有意为之的。
case "$RUN_CONFIG" in
/*) RUN_CONFIG_ABS=$RUN_CONFIG ;;
*) RUN_CONFIG_ABS="$(pwd)/$RUN_CONFIG" ;;
esac
# 比对的串里**只能**放"我们以前写过的默认落点"。这里原来把 $RUN_CONFIG 自己也算进去了
# （$LEGACY_RUN_CONFIG_ABS 就是它），等于自比自——永远命中，于是你在 install.yaml 里写的
# 任何 --config 都会被悄悄改回默认落点，注释里那句"你另外指定的路径不动"根本没兑现。
LEGACY_DEFAULTS=" $DEFAULT_CONFIG_PATH $LEGACY_RUN_CONFIG $LEGACY_SHAREDIR/$RUN_CONFIG_NAME $ROOT/var/lib/$DIR_NAME/$RUN_CONFIG_NAME $ROOT/etc/sol/$RUN_CONFIG_NAME "
case "$LEGACY_DEFAULTS" in
*" $RUN_CONFIG "* | *" $RUN_CONFIG_ABS "*)
	# 文件本身在升权之前就搬好了（上面那段），这里只把 install.yaml 里的默认值改写成新位置。
	old_run_config=$RUN_CONFIG
	RUN_CONFIG=$DEFAULT_CONFIG_PATH
	# 只把那个值整段换成新路径：参数顺序、你自己加的 flag（--watch 之类）都不碰。
	_old_re=$(printf '%s' "$old_run_config" | sed 's/[.[\*^$#]/\\&/g')
	ARGS=$(printf '%s\n' "$ARGS" | sed "s#^${_old_re}\$#$RUN_CONFIG#")
	# 同一件事落到文件上：只换那一行里的这个值，别把整行重写成只剩 --config 一个参数。
	sed -i.bak "s#^\([[:space:]]*args:[[:space:]]*\[.*\)${_old_re}\(.*\]\)#\1${RUN_CONFIG}\2#" "$USER_CONFIG" &&
		rm -f "$USER_CONFIG.bak"
	say "运行配置   改到用户目录：$RUN_CONFIG$( [ -n "$RUN_COPY_FROM" ] && printf '（老位置那份原样拷过去）' )；你另外指定的路径不受影响。"
	;;
esac
# sol.yaml：一份开箱即用的配置（纯包关机 / magic+"reboot" 重启 / magic+"sleep" 睡眠）。
# 没有才写，绝不覆盖。
RUNTIME_CONFIG_GENERATED=no
RUNTIME_CONFIG_COPIED=no
# 控制面令牌：32 字节随机数的十六进制（64 个字符）。openssl 最省事，没有就退回 /dev/urandom。
# 两条路都得真拿到随机数——都失败时宁可在这里停下来，也不写一份空令牌的配置让服务起不来。
new_control_token() {
	token=""
	if command -v openssl >/dev/null 2>&1; then
		token=$(openssl rand -hex 32)
	elif [ -r /dev/urandom ] && command -v od >/dev/null 2>&1; then
		token=$(od -An -tx1 -N 32 /dev/urandom | tr -d ' \n')
	fi
	[ -n "$token" ] || die "生成控制面令牌失败：这台机器上既没有 openssl，也读不到 /dev/urandom。"
	printf '%s' "$token"
}
# 模板里的令牌是个占位符：就地换成真的随机数（配置里不留一个谁都猜得到的密码）。
fill_template_token() { # fill_template_token <文件> <令牌>
	tok_tmp="$1.token.tmp"
	sed "s/^\([[:space:]]*auth:[[:space:]]*{[[:space:]]*type:[[:space:]]*bearer,[[:space:]]*token:[[:space:]]*\).*\([[:space:]]*}\)/\1$2\2/" "$1" >"$tok_tmp" &&
		cat "$tok_tmp" >"$1" && rm -f "$tok_tmp"
}
ensure_runtime_config() {
	[ -f "$RUN_CONFIG" ] && return 0
	# ① 老位置那份（你改过令牌、规则）原样搬过来——谁也无权冲掉。
	# ② 没有老文件才用模板：包自带的脚本边上的 sol.yaml（注释最全），没有就用下面内置的那份。
	# 这两条只对默认那份生效；你另外指定的 --config 路径照旧只用下面的内置模板。
	if [ "$RUN_CONFIG" = "$DEFAULT_CONFIG_PATH" ]; then
		if [ -n "$RUN_COPY_FROM" ]; then
			# 用户目录里那份必须是当前用户的（服务以 root 跑，读得到），所以不是 root 写。
			mkdir -p "$RUN_DIR" 2>/dev/null || mkdir_root "$RUN_DIR"
			if cp "$RUN_COPY_FROM" "$RUN_CONFIG" 2>/dev/null && chmod 600 "$RUN_CONFIG" 2>/dev/null; then
				RUNTIME_CONFIG_GENERATED=yes
				RUNTIME_CONFIG_COPIED=yes
				RUN_COPIED_FROM=$RUN_COPY_FROM
				return 0
			fi
			warn "没能把 $RUN_COPY_FROM 拷到 $RUN_CONFIG，按新位置重新生成。"
		elif [ -f "$TEMPLATE_CONFIG" ]; then
			mkdir -p "$RUN_DIR" 2>/dev/null || mkdir_root "$RUN_DIR"
			runtime_tmp=$(mktemp)
			if cp "$TEMPLATE_CONFIG" "$runtime_tmp" 2>/dev/null &&
				fill_template_token "$runtime_tmp" "$(new_control_token)" &&
				cp "$runtime_tmp" "$RUN_CONFIG" 2>/dev/null; then
				chmod 600 "$RUN_CONFIG" 2>/dev/null || true
				rm -f "$runtime_tmp"
				RUNTIME_CONFIG_GENERATED=yes
				RUNTIME_CONFIG_FROM=$TEMPLATE_CONFIG
				return 0
			fi
			rm -f "$runtime_tmp"
			warn "没能用 $TEMPLATE_CONFIG 生成 $RUN_CONFIG，改用内置模板。"
		fi
	fi
	# 这份配置里有一个**明文令牌**（下面随机生成，服务拿它守住控制面），所以文件写完就是
	# 0600：别的本地用户读不到它，也就调不动这里的关机/重启。
	CONTROL_TOKEN=$(new_control_token)
	runtime_tmp=$(mktemp)
	cat >"$runtime_tmp" <<YAML
# 纯包 -> 关机；magic+"reboot" -> 重启；magic+"sleep" -> 睡眠
version: 1
server:
  interfaces: []
  # 改这份文件后秒级自动生效（改坏了也不会顶掉正在跑的那份）。
  watch: 5s
  # HTTP 控制面（默认开启）：令牌三选一——token 就直接写在下面（这里已经是随机生成的一串）、
  # token_env 指环境变量、token_file 指一个 0600 文件。三端的设置办法与 curl 用法见 README 的
  # Control plane 一节；不要控制面就把 enabled 改成 false。
  http:
    enabled: true
    listen: 127.0.0.1:8080
    auth: { type: bearer, token: ${CONTROL_TOKEN} }
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
	# 默认落在用户目录里，不需要 root；真写不进去（HOME 不可写之类）再请一次 sudo。
	if [ "$(dirname "$RUN_CONFIG")" = "$RUN_DIR" ] && writable_target "$RUN_DIR"; then
		install -m 600 "$runtime_tmp" "$RUN_CONFIG" || put_root "$runtime_tmp" "$RUN_CONFIG" 0600
	else
		put_root "$runtime_tmp" "$RUN_CONFIG" 0600
	fi
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
		say "  二进制      没有找到可用的 sol：脚本旁边、当前目录、${DEST_BIN}、\$PATH 里都没有"
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
	[ -n "$inst_bin" ] || inst_bin="$DEST_BIN"
	inst_n=$(running_instances "$inst_bin" | wc -l | tr -d ' ')
	if [ "$inst_n" != "0" ]; then
		say "  进程        有 $inst_n 个在跑：${inst_bin}（要用到端口时会先停掉它）"
	else
		say "  进程        没有在跑"
	fi
	[ -n "$MANAGED_BY" ] && say "  包管理器    这个 sol 是 $MANAGED_BY 装的（升级会覆盖它）"
	if [ "$UID_NOW" = "0" ]; then
		say "  权限        root（注册服务不需要再升权）"
	elif [ -n "$ROOT" ]; then
		say "  权限        普通用户 + SOL_INSTALL_ROOT=${ROOT}（自己的地盘，不会 sudo）"
	else
		say "  权限        普通用户（落点在自己目录里、不用提权；只有装系统级服务时才在动手前 sudo 重跑一遍）"
	fi
	if [ -f "$RUN_CONFIG" ]; then
		if [ "$RUNTIME_CONFIG_GENERATED" = yes ]; then
			say "  运行配置    ${RUN_CONFIG}（沿用了 ${RUN_COPIED_FROM}，原样拷过来的）"
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
	binary=${DEST_BIN:-$RUN_DIR/sol}
	sha=""
	[ -f "$binary" ] && sha=$(sha256 "$binary")
	cat >"$LEDGER_TMP" <<EOF
{
  "schema": 1,
  "method": "script",
  "installed_version": "$(binary_version "$binary" | sed 's/.*version //')",
  "installed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "prefix": "$RUN_DIR",
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
  "log_paths": [$( [ "$SERVICE_KIND" = launchd ] && printf '"%s"' "$LOG_FILE" )],
  "previous": { "installed_version": "$2", "sha256": "$3" },
  "incomplete": $1
}
EOF
	if [ -f "$LEDGER" ] && [ -w "$LEDGER" ]; then
		cp "$LEDGER_TMP" "$LEDGER"
	else
		mkdir_root "$RUN_DIR"
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
		mkdir_root "$RUN_DIR"
		if [ -n "$SUDO" ] && [ ! -w "$RUN_DIR" ]; then
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
# DEST_BIN 不在这里置空：它早在路径那一段就等于 $RUN_DIR/sol 了。置空会让卸载报告打出
# "已删除：" 后面一片空白（卸载路径不走 do_install，没人再给它赋值）。
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
			printf 'ExecStart=%s %s\n' "$DEST_BIN" "$(printf '%s' "$ARGS" | tr '\n' ' ' | sed 's/ $//')"
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
			printf '    <string>%s</string>\n' "$DEST_BIN"
			printf '%s\n' "$ARGS" | while IFS= read -r a; do printf '    <string>%s</string>\n' "$a"; done
			printf '  </array>\n'
			printf '  <key>RunAtLoad</key><true/>\n'
			printf '  <key>KeepAlive</key><true/>\n'
			# launchd 要日志文件的目录先存在，否则任务起不来（这是 macOS 上最容易踩的一脚）。
			# 日志跟二进制同目录：整个 sol 目录都在用户目录下，服务以 root 跑也写得到（root 无视权限）。
			printf '  <key>StandardOutPath</key><string>%s</string>\n' "$LOG_FILE"
			printf '  <key>StandardErrorPath</key><string>%s</string>\n' "$LOG_FILE"
			printf '</dict>\n</plist>\n'
		}
		;;
	esac
}
# 呼出文件管理器，跳到运行配置所在目录——先问，问过才开；没有图形界面、SSH 里、管道执行、
# 沙箱或无人回答时只报路径，绝不在这里等人（这里的任何阻塞都会变成"脚本卡死"）。
open_config_dir() { # open_config_dir <目录> <文件>
	dir=$1
	file=$2
	say ""
	say "  配置文件    $file"
	say "     刚生成的这份就是 sol 的跑法：端口、动作、允许的来源网段都在里面。默认是"
	say "     纯包关机 / magic+reboot 重启 / magic+sleep 睡眠，按需改。"
	say "     改完不用重装：配置里有 watch，秒级自动生效；改坏了也不会顶掉正在跑的那份。"
	if [ -n "${SOL_INSTALL_NO_OPEN:-}" ] || [ -n "$ROOT" ] || [ ! -t 0 ]; then
		say "     目录：$dir"
		return 0
	fi
	sys=$(uname -s 2>/dev/null || echo unknown)
	opener=""
	case "$sys" in
	Darwin) opener=open ;;
	*) for c in xdg-open nautilus dolphin thunar nemo caja pcmanfm; do
		if command -v "$c" >/dev/null 2>&1; then opener=$c; break; fi
	done ;;
	esac
	if [ -z "$opener" ] || [ -n "${SSH_CONNECTION:-}${SSH_TTY:-}" ] ||
		{ [ "$sys" != Darwin ] && [ -z "${DISPLAY:-}" ] && [ -z "${WAYLAND_DISPLAY:-}" ]; }; then
		say "     （这里没有图形界面：自己开 $dir 改它）"
		return 0
	fi
	if ask "要现在打开文件夹，去改这份配置吗？" y; then
		"$opener" "$dir" >/dev/null 2>&1 &
		say "     已打开：$dir"
	else
		say "     目录：$dir"
	fi
}
# 老版本把二进制放在 /usr/local/bin/sol、台账放在 /usr/local/share/sol/install.json。落点已经收进
# 用户目录了，机器上留着那份旧二进制只会造成"命令行走的是一份、服务跑的是另一份"——搬过来时顺手
# 清掉它。只认台账里记下的路径和这个历史默认值，别的一律不碰（别人装的 sol 跟我们无关）。
# 挂了自选根时**照样清**：那时 LEGACY_BIN 就是 <根>/usr/local/bin/sol，删的仍在根里面，
# 而且沙箱测试只有走这条路才碰得到这段代码。
migrate_legacy_bin() {
	# 候选有两个，两个都要看：台账里记着的那份（换过落点、或手工装到别处过），以及历史落点上那份。
	# 只按台账判断会漏掉最常见的一种状态——台账已经写成新落点、老落点却还留着一份（实测过）。
	for old in "${LEDGER_BINARY:-}" "$LEGACY_BIN"; do
		[ -n "$old" ] || continue
		[ "$old" = "$DEST_BIN" ] && continue
		[ -f "$old" ] || continue
		[ -f "$DEST_BIN" ] || continue # 新的那份还没就位：不能先把旧的删了，否则机器上一份都不剩
		# 只删"我们装过的那份"：台账里记着它，或者老台账（安装证据）就在历史落点上。
		# 两条都不成立就别动——那是别人放在 /usr/local/bin 里的 sol，只提醒一句。
		case "$old" in
		"$LEDGER_BINARY") : ;;
		*) [ -f "$LEGACY_LEDGER" ] || {
			say "提示        $old 也是一份 sol，但台账里没有它的记录（不是这个脚本装的），没动它"
			continue
		} ;;
		esac
		stop_instances "$old" "老落点那份要搬走，不能让它继续占着端口"
		rm_root "$old" "$old.bak"
		act root "清掉老落点里的二进制" "sudo rm $old $old.bak"
		say "二进制      老落点那份已清掉：$old（现在只有 $DEST_BIN）"
	done
}
# 老的台账（/usr/local/share/sol/install.json）也是同一件事：新台账写在 $RUN_DIR 里，
# 旧的那份留着会让下一次运行读到"上一次装在 /usr/local/bin"这种过期信息。
migrate_legacy_ledger() {
	[ -f "$LEGACY_LEDGER" ] || return 0
	[ "$LEGACY_LEDGER" = "$LEDGER" ] && return 0
	rm_root "$LEGACY_LEDGER"
	act root "清掉老落点里的台账" "sudo rm $LEGACY_LEDGER"
	rm_root "$LEGACY_SHAREDIR/$HISTORY_NAME"
	act root "清掉老落点里的历史" "sudo rm $LEGACY_SHAREDIR/$HISTORY_NAME"
	# 老落点目录空了就删掉（/usr/local/share/sol 是我们建的，删了不留空壳）。用 rmdir：它只删空目录，
	# 目录里万一冒出别人的东西会失败——比 rm -rf 安全，也就不会删到不该删的。
	# /usr/local/bin 不删：那是系统的目录，里面还有别人的东西。
	if [ -d "$LEGACY_SHAREDIR" ] && [ -z "$(ls -A "$LEGACY_SHAREDIR" 2>/dev/null)" ]; then
		if writable_target "$LEGACY_SHAREDIR"; then rmdir "$LEGACY_SHAREDIR"; else as_root rmdir "$LEGACY_SHAREDIR"; fi
		act root "删掉空了的老落点目录" "sudo rmdir $LEGACY_SHAREDIR"
	fi
}
do_install() {
	[ -n "$LOCAL_BIN" ] ||
		die "没有可用的 sol 二进制：把 sol（或 sol.exe）放在脚本旁边、\$PATH 里，或者已经装在 ${DEST_BIN}，再跑一次"
	new_ver=$(binary_version "$LOCAL_BIN")
	new_sha=$(sha256 "$LOCAL_BIN")
	DEST_BIN="$RUN_DIR/sol"
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
			# 但"老落点还留着一份"这件事得处理掉：不然机器上一直有两个 sol，命令行走哪份看 PATH。
			migrate_legacy_bin
			migrate_legacy_ledger
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
	mkdir_root "$RUN_DIR"
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
		if [ -w "$RUN_DIR" ]; then
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
	# 3.5) 老系统落点里那份（/usr/local/bin/sol）搬走：不然机器上会有两个 sol，PATH 上谁先生效看运气。
	migrate_legacy_bin
	# 4) 服务定义（只有选了服务才写）
	if [ "$SERVICE_CHOSEN" = true ]; then
		write_service
	fi
	# 5) 台账 + 历史（这次是完整的）
	write_ledger false "$LEDGER_VERSION" "$LEDGER_SHA"
	act user "更新安装台账（incomplete=false）" ""
	# 老落点的残留台账在读完、写完新台账之后再清：先清会让上面那几个 LEDGER_* 变成"没装过"。
	migrate_legacy_ledger
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
				warn "launchd 任务没有在跑。看日志：$LOG_FILE"
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
			# 日志就在 $RUN_DIR 里（跟二进制同目录），安装时已经把落点目录建好了，这里只是兜底。
			mkdir_root "$RUN_DIR"
			act root "确认日志目录存在（launchd 需要它）" "sudo mkdir -p $RUN_DIR"
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
		[ -n "$LOCAL_BIN" ] && say "        · 安装包里那份 ${LOCAL_BIN}"
		say "        · ${DEST_BIN}、防火墙规则（如果加过）"
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
	[ -n "$bin" ] || bin="$DEST_BIN"
	stop_instances "$bin" "卸载要删掉它，也不能让它继续占着端口"
	if [ -f "$bin" ]; then
		rm_root "$bin" "$bin.bak"
		act root "删二进制与 sol.bak" "sudo rm $bin $bin.bak"
	fi
	# 配置与日志默认留着；问了才删。它们现在是**你自己的**文件（就在用户目录里），用 rm 而不是 sudo rm：
	# 走 root 删出来的残留属主会变成 root，下次普通用户想改都改不了。
	del_configs=no
	if ask "配置文件也一起删掉吗？（$INSTALL_CONFIG_NAME、$RUN_CONFIG_NAME）" n; then del_configs=yes; fi
	if [ "$del_configs" = yes ]; then
		if [ -f "$USER_CONFIG" ]; then
			rm -f "$USER_CONFIG"
			act user "删安装配置" "rm $USER_CONFIG"
		fi
		if [ -f "$RUN_CONFIG" ]; then
			rm -f "$RUN_CONFIG"
			act user "删运行配置" "rm $RUN_CONFIG"
		fi
	else
		say "配置留着：${USER_CONFIG}、$RUN_CONFIG"
		say "（要删：rm $USER_CONFIG $RUN_CONFIG）"
		act user "保留配置（未删）" "# 配置与日志保留"
	fi
	append_history "uninstall"
	# 台账最后删（它记着上面这些路径）。删完顺手说一句目录里还剩什么——用户问的最多的就是
	# "到底还有没有东西留在别处"，答案现在只有一个目录。
	rm -f "$LEDGER" 2>/dev/null || true
	act user "删台账" "rm $LEDGER"
	_leftover=$(ls -A "$RUN_DIR" 2>/dev/null | tr '\n' ' ' || true)
	_leftover=${_leftover% }
	if [ -z "$_leftover" ]; then
		rmdir "$RUN_DIR" 2>/dev/null || true
		act user "删掉空的落点目录" "rmdir $RUN_DIR"
	else
		say "落点目录里还剩：$_leftover（在 $RUN_DIR；不要了就连目录一起删：rm -rf $RUN_DIR）"
	fi
}
# ───────────────────────── 报告 ─────────────────────────
report() { # report <install|uninstall|none>
	case "$1" in
	install)
		head2 "[完成]"
		say "  版本        $(version_tag "$DEST_BIN" || echo '（读不到版本）')"
		say "  落点        $RUN_DIR（二进制、配置、台账、日志都在这一个目录里）"
		say "  二进制      $DEST_BIN"
		if [ -f "$DEST_BIN.bak" ]; then say "  （旧版本留在 $DEST_BIN.bak，回滚就是把它换回去）"; fi
		say ""
		say "  安装配置    $USER_CONFIG        改这里，然后重跑脚本"
		say "  运行配置    $RUN_CONFIG$( [ -n "${RUN_COPIED_FROM:-}" ] && printf '（沿用 %s，原样拷过来的）' "$RUN_COPIED_FROM" || printf '（新生成的示例配置：三条规则 + 随机令牌，按需改）' )"
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
		say "  二进制      $([ -f "$DEST_BIN" ] && echo "还在：$DEST_BIN" || echo "已删除：$DEST_BIN")"
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
		open_config_dir "$(dirname "$RUN_CONFIG")" "$RUN_CONFIG"
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
open_config_dir "$(dirname "$RUN_CONFIG")" "$RUN_CONFIG"
escalate install
do_install
if [ "$NOOP" = yes ]; then
	say "（什么都没改。想强制重写服务定义就选 3 重新生成配置，或先卸载。）"
	exit 0
fi
report install
