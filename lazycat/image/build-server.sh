#!/usr/bin/env bash
#
# LPK 构建前的准备：产出「Linux 服务端二进制」与「前端 dist」。
#
# 由 `lzc-cli project build` 按 lzc-build.yml 的 `buildscript` 调用，
# 在**开发机**上跑（不是盒子）。也可以手工跑，幂等。
#
# ── Go 部分怎么编 ─────────────────────────────────────────────────────────
#
# 产物是 **Linux 的静态 ELF**（CGO_ENABLED=0），微服盒子是 x86-64（见下）。
# Go 的交叉编译是工具链内建能力：设 GOOS/GOARCH 即可，**不需要容器、
# 不需要 qemu、不需要交叉 gcc** —— 整条 docker 编译链路因此不存在。
#
# 编译统一走共享交付入口（scripts/build.mjs，版本唯一来源 wails.json）：
#
#   node scripts/build.mjs frontend                 # 前端 → dist/
#   node scripts/build.mjs server --release \
#       --os=linux --arch=<amd64|arm64>             # 服务端 → target/go-build/
#
# 该入口已经强制了发布门禁（-mod=readonly -trimpath -buildvcs=false、
# CGO_ENABLED=0、版本注入与 wails.json 一致），这里不再重复实现，只做
# 消费与自检。产物命名与 scripts/pack-linux-server.sh 的默认输入一致。
#
# 同一架构的机器（amd64 主机 / amd64 CI）与异构机器（Apple Silicon）走
# 同一条代码路径 —— Go 交叉编译没有「原生/交叉」的分支。

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
# 服务端发布矩阵是 linux/amd64 + linux/arm64（与共享交付入口一致）；
# 这里默认 amd64（盒子），arm64 留给 Arm 形态的设备验证。
LZC_ARCH="${LZC_ARCH:-amd64}"
case "$LZC_ARCH" in
  amd64) ELF_ARCH="x86-64" ;;
  arm64) ELF_ARCH="aarch64" ;;
  *)
    echo "[lzc-build] ✗ LZC_ARCH 只能是 amd64 或 arm64，收到：$LZC_ARCH" >&2
    exit 1
    ;;
esac

echo "[lzc-build] 仓库根: $ROOT"
# ⚠️ 变量展开后面跟中文字符时**必须写 ${VAR}**：`$LZC_ARCH（` 会让 bash 把全角括号的
# 首字节当成变量名的一部分，报 `LZC_ARCH?: unbound variable`（已在真机上踩到一次）。
echo "[lzc-build] 目标架构: ${LZC_ARCH}（linux/${LZC_ARCH}，Go 交叉编译）"

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
echo "[lzc-build] 2/4 构建前端（node scripts/build.mjs frontend → dist/）"
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

# 共享交付入口内部跑 `pnpm exec vite build`；它**不做**依赖安装与隐式校验，
# 依赖状态由上一段显式管，这里也不要有隐式安装。
node scripts/build.mjs frontend

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
#
# ⚠️ 下面这条 cp 被 `scripts/verify-manifest-injects.py` 按字面正则断言
# （manifest 的 file:// 引用、这里的落点、仓库源文件三者必须一致），改写法要同步改校验。
rm -rf "$CONTENT/lazycat-injects"
mkdir -p "$CONTENT/lazycat-injects"
cp "$HERE"/../injects/*.js "$CONTENT/lazycat-injects/"
echo "[lzc-build]     注入脚本就位: $CONTENT/lazycat-injects"

# ── 3. 服务端二进制 ────────────────────────────────────────────────────
#
# 产物：target/go-build/nexterm-server-linux-${LZC_ARCH}
# （与 scripts/pack-linux-server.sh 的默认输入同一路径约定）。
#
# Go 交叉编译不需要容器：GOOS/GOARCH 由共享入口设置，CGO_ENABLED=0 产出
# 静态 ELF。构建缓存就是宿主 Go 的常规缓存，不需要单独的 registry 卷。
echo "[lzc-build] 3/4 构建服务端二进制（linux/${LZC_ARCH}，node scripts/build.mjs server，可能需要几分钟）"
node scripts/build.mjs server --release --os=linux --arch="$LZC_ARCH"

GO_BIN="$ROOT/target/go-build/nexterm-server-linux-${LZC_ARCH}"
install -m 0755 "$GO_BIN" "$OUT_BIN"
echo "[lzc-build]     二进制就位: $OUT_BIN"

# ── 4. 自检 ────────────────────────────────────────────────────────────
#
# 这几条都是「**不报错的**静默失败」，事后极难查，所以在打包前就断掉：
#   · 二进制架构不对（arm64 产物搬去 x86-64 盒子 ⇒ Exec format error，
#     而 LPK 照样能构建、能安装，只是启动即崩）
#   · 二进制不是静态链接（盒子基线里没有 glibc 之外的运行库保证；
#     共享入口已强制 CGO_ENABLED=0，这里复核最终结果）
#   · 前端 index.html 找不到（打开就是 404，而且没有任何日志说为什么）
echo "[lzc-build] 4/4 自检"
FILE_OUT="$(file -b "$OUT_BIN" || true)"
case "$FILE_OUT" in
  *ELF*"$ELF_ARCH"*) ;;
  *)
    echo "[lzc-build] ✗ 产出不是 Linux/${ELF_ARCH} 的 ELF：$FILE_OUT" >&2
    echo "[lzc-build]   目标架构是 LZC_ARCH=${LZC_ARCH}。" >&2
    exit 1
    ;;
esac
case "$FILE_OUT" in
  *statically\ linked*) ;;
  *)
    echo "[lzc-build] ✗ 产出不是静态链接：$FILE_OUT" >&2
    echo "[lzc-build]   服务端必须 CGO_ENABLED=0（共享交付入口已强制，这里不应发生）。" >&2
    exit 1
    ;;
esac
if command -v go >/dev/null 2>&1; then
  if ! go version -m "$OUT_BIN" | grep -q 'CGO_ENABLED=0'; then
    echo "[lzc-build] ✗ go version -m 里没有 CGO_ENABLED=0 标记" >&2
    exit 1
  fi
fi
if [ ! -f "$CONTENT/web/index.html" ]; then
  echo "[lzc-build] ✗ $CONTENT/web/index.html 不存在：前端没构建成功？" >&2
  exit 1
fi

echo "[lzc-build] 完成。接下来：lzc-cli project build"
