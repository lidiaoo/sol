#!/bin/sh
# 打发布包：三平台六架构的 sol + 安装脚本 + README，做成 README 里那些链接认识的名字。
#
#   scripts/release.sh v0.1.0            # 出 dist/：sol-v0.1.0-linux-amd64.tar.gz 等
#   scripts/release.sh                   # 版本号取 git describe（干净树才允许）
#   scripts/release.sh v0.1.0-rc1 --allow-dirty
#   scripts/release.sh v0.1.0 --platforms "linux/amd64 darwin/arm64"
#
# 为什么要有它（CI 已经用 goreleaser 了）：CI 只在有人点"publish release"时才跑，而且要联网拉
# goreleaser；本地想验证"这套脚本 + 这个二进制能不能一起用"，得能在自己机器上出同样的包。
# 两边的产物必须一致——`.goreleaser.yml` 的 name_template/files 与这里的名字、内容一一对应，
# 改一边就改另一边，别让 README 里的下载链接指向 404。
#
# 包里的布局（解包后一个目录，进目录直接跑安装脚本，它就是"执行目录"）：
#   sol 或 sol.exe          该平台的二进制
#   install.sh / install.ps1 / install.cmd   零参数安装脚本
#   README.md / README.zh-CN.md / CHANGELOG.md / LICENSE
#   sol-example.yaml        示例配置（安装脚本没配置时会自己生成一份，这份是给人读的）
#   sol.schema.json         配置 schema（编辑器补全用）
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

VERSION=""
ALLOW_DIRTY=no
PLATFORMS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

while [ $# -gt 0 ]; do
	case "$1" in
	--allow-dirty) ALLOW_DIRTY=yes ;;
	--platforms) shift; [ $# -gt 0 ] || { echo "错误: --platforms 后面要给 'os/arch ...'" >&2; exit 2; }; PLATFORMS=$1 ;;
	-h|--help) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	-*) echo "错误: 不认识的参数 $1（-h 看用法）" >&2; exit 2 ;;
	*) [ -z "$VERSION" ] || { echo "错误: 版本号给了两次" >&2; exit 2; }; VERSION=$1 ;;
	esac
	shift
done

say() { printf '%s\n' "$*"; }
die() { printf '错误: %s\n' "$*" >&2; exit 1; }

command -v git >/dev/null 2>&1 || die "需要 git（判断工作区是否干净、取版本号）"
GO=${GO:-go}
command -v "$GO" >/dev/null 2>&1 || die "找不到 $GO（用 GO=/path/to/go 指定）"

# 版本号：要么显式给，要么取最近的 tag；带 -dirty 的树默认拒绝打包（发布不该是脏树）。
# 只把**已跟踪文件的改动**算脏：未跟踪的 .idea/、dist/ 之类不该拦住发布（不然每台机器都"脏"）。
DIRTY=no
[ -n "$(git status --porcelain --untracked-files=no 2>/dev/null || true)" ] && DIRTY=yes
if [ -z "$VERSION" ]; then
	VERSION=$(git describe --tags --always --dirty 2>/dev/null || true)
	[ -n "$VERSION" ] || die "取不到版本号：给一个，例如 scripts/release.sh v0.1.0"
fi
case "$VERSION" in
*-dirty) DIRTY=yes ;;
esac
if [ "$DIRTY" = yes ] && [ "$ALLOW_DIRTY" != yes ]; then
	die "工作区是脏的（或版本号带 -dirty）：发布包不该来自脏树。要么先提交，要么加 --allow-dirty 明确接受。"
fi

DIST="$ROOT/dist"
STAGE="$DIST/stage"
# 清的是**内容**而不是 dist 目录本身：Windows 上只要有个 shell 把 dist 当工作目录，
# 删目录就会 "Device or resource busy"，而清内容照样能跑。
mkdir -p "$DIST"
rm -rf "$STAGE" 2>/dev/null || true
rm -f "$DIST"/*.tar.gz "$DIST"/*.zip "$DIST"/checksums.txt "$DIST"/install.sh "$DIST"/install.ps1 "$DIST"/install.cmd 2>/dev/null || true
mkdir -p "$STAGE"

# 脏标记同样盖章，口径与上面那套一致（只算已跟踪改动）：工具链自带的 vcs.modified 连未跟踪文件也算，
# 而出包过程本身就在写 dist/stage/...，不盖的话"干净树"出出来的包也会自称 -dirty。
DIRTY_STAMP=false
[ "$DIRTY" = yes ] && DIRTY_STAMP=true
LDFLAGS="-s -w -X github.com/lidiaoo/sol/internal/buildinfo.version=$VERSION -X github.com/lidiaoo/sol/internal/buildinfo.dirty=$DIRTY_STAMP"

say "版本    $VERSION"
say "落点    $DIST"
say "平台    $PLATFORMS"
say ""

for p in $PLATFORMS; do
	os=${p%%/*}
	arch=${p##*/}
	[ "$os" != "$p" ] || die "平台写法要是 os/arch，收到：$p"
	name="sol-$VERSION-$os-$arch"
	dir="$STAGE/$name"
	mkdir -p "$dir"

	bin=sol
	[ "$os" = windows ] && bin=sol.exe

	printf '  %-24s ' "$name"
	# -o 用**相对路径**：go 是 native 程序，MSYS 的 /e/... 它不认（会"成功"却没写出文件）。
	# shellcheck disable=SC2086
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch $GO build -trimpath -ldflags "$LDFLAGS" -o "dist/stage/$name/$bin" . \
		|| die "给 $os/$arch 编译失败"
	chmod +x "$dir/$bin"

	# 跟二进制放进同一个目录：解包后这个目录就是"执行目录"，安装脚本零参数跑起来就知道该干什么。
	cp scripts/install.sh scripts/install.ps1 scripts/install.cmd "$dir/"
	chmod +x "$dir/install.sh"
	cp README.md README.zh-CN.md LICENSE "$dir/"
	[ -f CHANGELOG.md ] && cp CHANGELOG.md "$dir/"
	[ -f example/sol-example.yaml ] && cp example/sol-example.yaml "$dir/"
	[ -f schema/sol.schema.json ] && cp schema/sol.schema.json "$dir/"
	# 技能目录：Hermes 的 sol-install（README 里也指向它）。整棵拷进去，别挑文件。
	if [ -d skills ]; then
		mkdir -p "$dir/skills"
		cp -R skills/. "$dir/skills/"
	fi

	size=$(wc -c <"$dir/$bin" | tr -d ' ')
	say "ok（二进制 $size 字节）"
done

say ""
say "打包…"
cd "$STAGE"
for d in */; do
	name=${d%/}
	case "$name" in
	*windows-*) fmt=zip ;;
	*) fmt=tar.gz ;;
	esac
	if [ "$fmt" = zip ]; then
		if command -v zip >/dev/null 2>&1; then
			zip -qr "$DIST/$name.zip" "$name"
		elif command -v powershell >/dev/null 2>&1; then
			# git-bash 上常常没有 zip：用系统自带的 Compress-Archive——它只认 Windows 路径，
			# 所以先 cygpath -w 转一下（没有 cygpath 就直接把路径给它，多半是纯 Windows 环境）。
			wsrc=$STAGE/$name; wdst=$DIST/$name.zip
			if command -v cygpath >/dev/null 2>&1; then wsrc=$(cygpath -w "$wsrc"); wdst=$(cygpath -w "$wdst"); fi
			powershell -NoProfile -Command "Compress-Archive -Path '$wsrc' -DestinationPath '$wdst' -Force" >/dev/null
		else
			die "要 .zip 但没有 zip 也没有 powershell：装个 zip，或把 windows 从 --platforms 里去掉"
		fi
		out="$name.zip"
	else
		tar -czf "$DIST/$name.tar.gz" "$name"
		out="$name.tar.gz"
	fi
	printf '  %-34s %s\n' "$out" "$(wc -c <"$DIST/$out" | tr -d ' ') 字节"
done
cd "$ROOT"

# 产物体检：每条二进制必须是对应平台该有的格式与架构（交叉编译最怕"编出来了但不是那个东西"），
# 包里那几个脚本必须与仓库里的逐字节一致（不然包里就是另一套安装逻辑）。
say ""
say "体检…"
if command -v file >/dev/null 2>&1; then
	for p in $PLATFORMS; do
		os=${p%%/*}; arch=${p##*/}
		dir="$STAGE/sol-$VERSION-$os-$arch"
		bin=sol; [ "$os" = windows ] && bin=sol.exe
		desc=$(file -b "$dir/$bin" 2>/dev/null || true)
		want_os=""
		case "$os" in
		linux) want_os="ELF 64-bit" ;;
		darwin) want_os="Mach-O 64-bit" ;;
		windows) want_os="PE32+" ;;
		esac
		# file 报架构的写法各平台/各版本都不同（Linux 出 x86-64、macOS 出 x86_64、Windows 出 ARM64…），
		# 所以每个平台给一组"任一种常见写法都算"的正则，别去猜某一种拼写。
		want_arch=""
		case "$os/$arch" in
		linux/amd64) want_arch="x86-64|x86_64|AMD64" ;;
		darwin/amd64) want_arch="x86_64|x86-64|AMD64" ;;
		linux/arm64|darwin/arm64|windows/arm64) want_arch="aarch64|arm64|AArch64|ARM64" ;;
		windows/amd64) want_arch="x86-64|x86_64|AMD64" ;;
		esac
		case "$desc" in
		*"$want_os"*) : ;;
		*) die "$p 的二进制格式不对（期望含 \"$want_os\"）：$desc" ;;
		esac
		if [ -n "$want_arch" ]; then
			printf '%s' "$desc" | grep -Eq "$want_arch" || die "$p 的架构不对（期望 $want_arch）：$desc"
		fi
		printf '  %-24s %s\n' "$p" "$desc"
	done
else
	say "  （这台机器没有 file 命令：跳过二进制的格式/架构核对）"
fi
for name in install.sh install.ps1 install.cmd; do
	first=$(ls -d "$STAGE"/sol-"$VERSION"-*/ 2>/dev/null | head -1)
	[ -n "$first" ] || break
	cmp -s "scripts/$name" "${first}$name" || die "包里的 $name 与仓库里的不一致（打包环节动过它？）"
done
say "  包里脚本与仓库逐字节一致：install.sh / install.ps1 / install.cmd"
if [ -d skills ]; then
	for sk in skills/*/; do
		[ -d "$sk" ] || continue
		skn=${sk%/}; skn=${skn#skills/}
		[ -f "$first/skills/$skn/SKILL.md" ] || die "包里有 skills 目录但缺 $skn/SKILL.md（打包环节漏了？）"
	done
	say "  技能目录已带上：$(ls skills | tr '\n' ' ')"
fi

# README 里的一行安装指向 releases/latest/download/install.sh（install.ps1 / install.cmd 同理），
# 所以这些脚本在 dist/ 根上也要有一份单独的资产——不然那个地址就是 404。
cp scripts/install.sh scripts/install.ps1 scripts/install.cmd "$DIST/"

# 校验和（名字与 goreleaser 的 checksum 配置一致：checksums.txt）。
(
	cd "$DIST"
	for f in *.tar.gz *.zip; do
		[ -e "$f" ] || continue
		if command -v sha256sum >/dev/null 2>&1; then sha256sum "$f"; else shasum -a 256 "$f"; fi
	done >checksums.txt
)
say ""
say "校验和      $DIST/checksums.txt"
say "散装资产    install.sh / install.ps1 / install.cmd（给 releases/latest/download/ 用）"

# 自检：宿主平台的二进制真的能跑（跨平台的没法在本机执行，交给 CI/真机）。
host_os=$(uname -s 2>/dev/null | tr 'A-Z' 'a-z')
case "$host_os" in
linux*) HOST_PLATFORM="linux/$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')" ;;
darwin*) HOST_PLATFORM="darwin/$(uname -m | sed 's/x86_64/amd64/; s/arm64/arm64/')" ;;
mingw*|msys*|cygwin*) HOST_PLATFORM="windows/amd64" ;;
*) HOST_PLATFORM="" ;;
esac
if [ -n "$HOST_PLATFORM" ]; then
	hb="sol"; [ "${HOST_PLATFORM%%/*}" = windows ] && hb="sol.exe"
	hdir="$STAGE/sol-$VERSION-${HOST_PLATFORM%%/*}-${HOST_PLATFORM##*/}"
	if [ -x "$hdir/$hb" ]; then
		say ""
		say "自检        $("$hdir/$hb" --version 2>&1 | head -1)"
	fi
fi

say ""
say "下一步："
say "  git tag $VERSION && git push origin $VERSION        # 打 tag（CI 会在 release 创建后自动出同一套包）"
say "  gh release create $VERSION dist/* --title $VERSION  # 手动发也行（没有 gh 就用网页上传 dist/ 里的文件）"