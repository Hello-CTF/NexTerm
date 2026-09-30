#!/usr/bin/env bash
#
# LPK 构建前的准备：产出「Linux 服务端二进制」与「前端 dist」。
#
# 由 `lzc-cli project build` 按 lzc-build.yml 的 `buildscript` 调用，
# 在**开发机**上跑（不是盒子）。也可以手工跑，幂等。
#
# ── Rust 部分怎么编（这是本脚本最需要解释的一件事）────────────────────────
#
# 产物必须是 **x86-64 的 Linux 二进制**（微服盒子是 Intel，见下），
# 而开发机可能是 Apple Silicon —— 于是有三条路，本脚本走第 3 条：
#
#   1. macOS → Linux 交叉编译（不在容器里）
#      依赖树里有 sqlite（C）、aws-lc-sys（C + AVX 汇编），要凑一整套 sysroot，
#      慢且脆。**不采用**。
#
#   2. 容器跑 `--platform linux/amd64`（qemu 模拟）
#      看起来最省事，实测**不可靠**：在 2 vCPU / 4 GiB 的 Docker 虚拟机里编
#      aws-lc-sys 时，cc-rs 会报 `status code exit status: 4`，
#      失败的文件一会儿是 `mlkem_..._avx2_asm.S`、一会儿是 `rsaz-3k-avx512.S`。
#      **把并发压到 1 也一样**，而同一条 cc 命令单独跑是 exit 0 ——
#      也就是说不是汇编器不支持、不是代码问题，是模拟执行本身不稳。
#      **不采用**。
#
#   3. **arm64 容器（原生执行）+ Debian 交叉工具链编到 x86_64** ← 本脚本的做法
#      gcc 自己是 arm64 原生二进制（不做模拟），只有它*产出*的目标是 x86_64。
#      `CC_x86_64_unknown_linux_gnu` 指过去即可，cargo 侧用 `--target`。
#      速度与原生构建相当，且不受 Docker 虚拟机规格影响。
#
# 同一架构的机器（amd64 主机 / amd64 CI）走同一条代码路径：`--target` 等于宿主 triple，
# `CC_*` 不设置、cc-rs 用系统 cc，等价于原生编译 —— 不需要第二套分支。

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
CONTENT="$ROOT/lazycat/content"
OUT_BIN="$HERE/nexterm-server"

# ── 目标架构 ──────────────────────────────────────────────────────────
#
# 懒猫微服的盒子是 **x86-64**（LC-02 用 Intel Core i5-1155G7，LZCOS 基于 Debian 12）。
# 别和同厂「懒猫 AI 算力舱 X5-T4000/T5000」搞混 —— 那台是 Arm Neoverse + Ubuntu，另一条产品线。
#
# ⚠️ `LZC_ARCH` 只覆盖**二进制**这一半。镜像那一半在 `image/Dockerfile` 里，
# 靠 `ARG TARGETARCH` 锁定；`lzc-cli` 不支持传 `--build-arg`，所以**两处只能手动对齐**。
# 改这里时同步改 Dockerfile 的默认值，改完两边一起提交。
# ⚠️ 这里有两套命名，**不能共用同一个变量**（踩过）：
#     · CROSS_PREFIX    —— 编译程序的**前缀**，用下划线：`x86_64-linux-gnu-gcc`
#     · GCC_CROSS_PKG   —— apt 的**包名**，用连字符：`gcc-x86-64-linux-gnu`
#   写错成 `gcc-x86_64-linux-gnu` 的报错是 `E: Unable to locate package`，
#   看着像「源里没有」，其实是名字拼错。
LZC_ARCH="${LZC_ARCH:-amd64}"
case "$LZC_ARCH" in
  amd64)
    ELF_ARCH="x86-64"
    TARGET_TRIPLE="x86_64-unknown-linux-gnu"
    CROSS_PREFIX="x86_64-linux-gnu"
    GCC_CROSS_PKG="gcc-x86-64-linux-gnu"
    LIBC_CROSS_PKG="libc6-dev-amd64-cross"
    ;;
  arm64)
    ELF_ARCH="aarch64"
    TARGET_TRIPLE="aarch64-unknown-linux-gnu"
    CROSS_PREFIX="aarch64-linux-gnu"
    GCC_CROSS_PKG="gcc-aarch64-linux-gnu"
    LIBC_CROSS_PKG="libc6-dev-arm64-cross"
    ;;
  *)
    echo "[lzc-build] ✗ LZC_ARCH 只能是 amd64 或 arm64，收到：$LZC_ARCH" >&2
    exit 1
    ;;
esac

# 变量名要给 cargo/cc 用，得是下划线式与全大写式两种拼法。
TRIPLE_UNDER="${TARGET_TRIPLE//-/_}"
TRIPLE_UPPER="$(printf '%s' "$TRIPLE_UNDER" | tr '[:lower:]' '[:upper:]')"

echo "[lzc-build] 仓库根: $ROOT"
# ⚠️ 变量展开后面跟中文字符时**必须写 ${VAR}**：`$LZC_ARCH（` 会让 bash 把全角括号的
# 首字节当成变量名的一部分，报 `LZC_ARCH?: unbound variable`（已在真机上踩到一次）。
echo "[lzc-build] 目标架构: ${LZC_ARCH}（${TARGET_TRIPLE}）"

# ── 要不要交叉工具链 ──────────────────────────────────────────────────
#
# 用 Docker daemon 的架构（= 容器会以哪种架构原生执行）和自己的目标比。
# 相同 ⇒ 什么都不用装（amd64 机器上的常规路径）；不同 ⇒ 装 Debian 的交叉工具链。
#
# `CC_*` 只在需要交叉时才设：同架构下留空，cc-rs 会用系统 `cc`（本来就是对的架构）。
DAEMON_ARCH="$(docker version --format '{{.Server.Arch}}' 2>/dev/null || echo unknown)"
CROSS_ENV=()
if [ "$DAEMON_ARCH" != "$LZC_ARCH" ]; then
  echo "[lzc-build] Docker daemon 是 ${DAEMON_ARCH}，目标是 ${LZC_ARCH} ⇒ 用 Debian 交叉工具链（容器内原生执行，不做 qemu 模拟）"
  CROSS_ENV=(
    -e "CC_${TRIPLE_UNDER}=${CROSS_PREFIX}-gcc"
    -e "AR_${TRIPLE_UNDER}=${CROSS_PREFIX}-ar"
    -e "CARGO_TARGET_${TRIPLE_UPPER}_LINKER=${CROSS_PREFIX}-gcc"
  )
else
  echo "[lzc-build] Docker daemon 架构与目标一致（${LZC_ARCH}），原生编译"
fi

# ── 1. 基线镜像断言（只检查，不拉取）──────────────────────────────────
#
# 基线由 `Dockerfile` 的 `FROM` 指向**懒猫官方源**，由**盒子侧**在构建镜像时解析
# （`lzc-build.yml` 里的 `builder: remote`）。开发机**拉不到**那个源——它只有微服
# 自己有凭证，本机拿社区账号做 HTTP Basic 一律 401（细节见 lzc-build.yml 注释）。
# 所以这里**不做 `docker pull`**：从前那一步是为 `builder: local` 服务的，已不需要。
#
# 但这正是最容易**静默退化**的地方：`FROM` 一旦被改回 Docker Hub 的 tag
# （比如 `debian:bookworm-slim`），构建**照样成功**，只是整个基线层被全量内嵌，
# LPK 从 ~16 MiB 变成 ~43 MiB —— 不看体积根本发现不了。所以在这里断一道。
BASE_REF="$(grep -E '^FROM ' "$HERE/Dockerfile" | tail -1 | awk '{print $NF}')"
echo "[lzc-build] 1/4 基线镜像: ${BASE_REF}"
case "$BASE_REF" in
  registry.lazycat.cloud/*) ;;
  *)
    echo "[lzc-build] ⚠️ FROM 不是懒猫官方源引用 ⇒ 上游匹配会失败，**整个基线层将被全量内嵌**" >&2
    echo "[lzc-build]    修复：先跑  lzc-cli appstore copy-image <基线> --arch ${LZC_ARCH}" >&2
    echo "[lzc-build]    再把 FROM 改成它打印出的 registry.lazycat.cloud/... 引用。" >&2
    echo "[lzc-build]    （不是致命错误：LPK 仍能构建安装，只是会大 ~27 MiB。）" >&2
    ;;
esac

# ── 2. 前端 ────────────────────────────────────────────────────────────
echo "[lzc-build] 2/4 构建前端（pnpm build → dist/）"
cd "$ROOT"

# 依赖已装就跳过 install。
#   · 全新机器 / CI 上 node_modules 不存在 ⇒ 走 install，按 lockfile 锁版本；
#   · 开发机上已经装好了 ⇒ 跳过，省掉一次几分钟的全量安装。
# 这不是偷懒：pnpm 的 install 会在 `~/Library/pnpm/store/v11/projects/` 建一个
# 「项目注册」软链，在没有 home 写权限 / 受限沙箱里会被直接拒绝，让整个打包
# 卡死在第一步（症状是 `EEXIST ... symlink .../projects/<hash>`，看着像依赖坏了，
# 其实和依赖无关）。需要强制重装时设 `NEXTERM_FORCE_INSTALL=1`。
if [ -n "${NEXTERM_FORCE_INSTALL:-}" ] || [ ! -d node_modules ]; then
  if [ -f pnpm-lock.yaml ]; then
    pnpm install --frozen-lockfile
  else
    pnpm install
  fi
else
  echo "[lzc-build]     node_modules 已存在，跳过 pnpm install（强制重装：NEXTERM_FORCE_INSTALL=1）"
fi

# `--config.verify-deps-before-run=false`：pnpm 在 `run` 前会做一次依赖校验，
# 判定「过期」时会**自己再跑一次 install** —— 那等于在打包中途改依赖状态，
# 而且会踩上面同一个软链问题。依赖由上一段显式管，这里不要再有隐式安装。
pnpm --config.verify-deps-before-run=false run build

rm -rf "$CONTENT/web"
mkdir -p "$CONTENT"
cp -R "$ROOT/dist" "$CONTENT/web"
echo "[lzc-build]     前端就位: $CONTENT/web"

# 平台注入脚本随包提供（`lzc-manifest.yml` 的 `injects` 用 `file://` 引它）。
#
# 为什么必须打进包、不能用 CDN：注入的 `src: file:///lzcapp/pkg/content/...`
# 是由平台读**包内**文件后注入 HTML 的，没有网络去取远端 URL。
#
# 为什么单独放 `lazycat/injects/` 而不是直接丢进 `lazycat/content/`：
# `content/` 整个在 .gitignore 里（每次构建重生成），放进去这份脚本就没有版本记录了。
# 它是**第三方产物**，必须能被 review 到版本与来源（见同目录的 README）。
# 只拷 `.js`：同目录的 README 是给仓库读者看的来源说明，没必要进包。
rm -rf "$CONTENT/lazycat-injects"
mkdir -p "$CONTENT/lazycat-injects"
cp "$HERE"/../injects/*.js "$CONTENT/lazycat-injects/"
echo "[lzc-build]     注入脚本就位: $CONTENT/lazycat-injects"

# ── 2. 服务端二进制 ────────────────────────────────────────────────────
#
# CARGO_TARGET_DIR 指到仓库内的 target-linux/<arch>/（已在 .gitignore 里）：
# 默认的 target/ 会被 macOS 的产物占着，混用会让 cargo 反复重编。
# 单独一个 cargo registry 卷：容器里的 root 和宿主用户不同，共用宿主
# ~/.cargo 会有一堆权限问题。
#
# ⚠️ **注意这里没有 `--platform`**：容器按 daemon 的原生架构跑（快且稳），
# 跨架构靠 `--target` + 交叉 gcc 解决。见文件头第 3 条。
echo "[lzc-build] 3/4 构建服务端二进制（${TARGET_TRIPLE}，rust:1-bookworm，可能需要几分钟）"
docker run --rm \
  -v "$ROOT":/src \
  -v nexterm-cargo-registry:/usr/local/cargo/registry \
  -w /src \
  -v "$HERE/pick-target.sh:/usr/local/bin/pick-target.sh:ro" \
  -e CARGO_TARGET_DIR="/src/target-linux/$LZC_ARCH" \
  -e "NEXTERM_BUILD_TARGET=$TARGET_TRIPLE" \
  -e "NEXTERM_CROSS_PREFIX=$CROSS_PREFIX" \
  -e "NEXTERM_GCC_CROSS_PKG=$GCC_CROSS_PKG" \
  -e "NEXTERM_LIBC_CROSS_PKG=$LIBC_CROSS_PKG" \
  "${CROSS_ENV[@]}" \
  rust:1-bookworm \
  bash /usr/local/bin/pick-target.sh

install -m 0755 "$ROOT/target-linux/$LZC_ARCH/$TARGET_TRIPLE/release/nexterm-server" "$OUT_BIN"
echo "[lzc-build]     二进制就位: $OUT_BIN"

# ── 3. 自检 ────────────────────────────────────────────────────────────
#
# 这几条都是「**不报错的**静默失败」，事后极难查，所以在打包前就断掉：
#   · 二进制架构不对（arm64 产物搬去 x86-64 盒子 ⇒ Exec format error，
#     而 LPK 照样能构建、能安装，只是启动即崩）
#   · 二进制是 macOS 的（同上）
#   · 前端 index.html 找不到（打开就是 404，而且没有任何日志说为什么）
echo "[lzc-build] 4/4 自检"
FILE_OUT="$(file -b "$OUT_BIN" || true)"
case "$FILE_OUT" in
  *ELF*"$ELF_ARCH"*) ;;
  *)
    echo "[lzc-build] ✗ 产出不是 Linux/${ELF_ARCH} 的 ELF：$FILE_OUT" >&2
    echo "[lzc-build]   目标架构是 LZC_ARCH=${LZC_ARCH}（${TARGET_TRIPLE}）。" >&2
    exit 1
    ;;
esac
if [ ! -f "$CONTENT/web/index.html" ]; then
  echo "[lzc-build] ✗ $CONTENT/web/index.html 不存在：前端没构建成功？" >&2
  exit 1
fi

echo "[lzc-build] 完成。接下来：lzc-cli project build"
