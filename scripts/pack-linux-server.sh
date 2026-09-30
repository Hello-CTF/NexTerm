#!/usr/bin/env bash
#
# 打 Linux 服务端的两个发布产物（tar.gz）：
#
#   NexTerm-<版本>-linux-amd64.tar.gz              LinuxServer（浏览器版）
#   NexTerm-onlyServer-<版本>-linux-amd64.tar.gz   只做资产同步（命令行配置）
#
# 本脚本**只打包，不编译**（编译在 CI 或本机构建里做）。两个输入：
#   target/release/nexterm-server   服务端二进制（Linux/amd64）
#   dist/                           前端构建产物（只有 LinuxServer 需要）
#
# 为什么要分开成一个脚本而不是写在 workflow 里：CI 一次往返十几分钟，
# 而「tar 里少了个文件 / 名字拼错 / 权限不对」这类问题本地跑一遍就能发现 ——
# 也能在发版前先出一份给自己装上验一遍。
#
# 用法：
#   bash scripts/pack-linux-server.sh [--out DIR] [--bin PATH] [--web PATH]
#
# 手动构建这两个输入（在 Linux 上；macOS 上产出的是 Mach-O，会被本脚本的
# 架构自检拦下）：
#   pnpm install && pnpm exec vite build
#   cargo build --release --no-default-features --features server --bin nexterm-server

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/target/release-assets"
BIN="$ROOT/target/release/nexterm-server"
WEB="$ROOT/dist"

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --bin) BIN="$2"; shift 2 ;;
    --web) WEB="$2"; shift 2 ;;
    -h|--help) sed -n '2,25p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

# 版本号从 crate 取（与桌面端同一处，发版时改的三个地方之一）。
VERSION="$(sed -n 's/^version = "\(.*\)"/\1/p' "$ROOT/src-tauri/Cargo.toml" | head -1)"
[ -n "$VERSION" ] || { echo "从 src-tauri/Cargo.toml 读不到版本号" >&2; exit 1; }

echo "版本   : $VERSION"
echo "二进制 : $BIN"
echo "前端   : $WEB"
echo "输出   : $OUT"

# ── 输入自检 ────────────────────────────────────────────────────────────
# 一条条硬断言。少一样都别打出一个「装上去才发现不对」的包。
[ -f "$BIN" ] || {
  echo "✗ 找不到服务端二进制：$BIN" >&2
  echo "  请先构建：cargo build --release --no-default-features --features server --bin nexterm-server" >&2
  exit 1
}
[ -f "$WEB/index.html" ] || {
  echo "✗ 前端产物不对：$WEB/index.html 不存在（请先 pnpm exec vite build）" >&2
  exit 1
}

# 架构必须真的是「Linux + x86-64」。⚠️ 这条在 macOS 上跑必然失败 —— 那是对的：
# 拿 Mach-O 打出来的包，用户 `systemctl start` 只会得到一句
# 「Exec format error」，而那时已经发到 Release 上了。
FILE_INFO="$(file -b "$BIN")"
case "$FILE_INFO" in
  *ELF*64-bit* ) ;;
  *) echo "✗ 不是 ELF 二进制：$FILE_INFO" >&2; exit 1 ;;
esac
case "$FILE_INFO" in
  *x86-64*|*x86_64* ) ;;
  *) echo "✗ 不是 x86-64（amd64）：$FILE_INFO" >&2; exit 1 ;;
esac
[ -x "$BIN" ] || { echo "✗ 二进制没有执行位：$BIN" >&2; exit 1; }

# GNU tar 与 bsdtar 的「把所有者写成 root」参数名不一样（--owner vs --uid）。
# 统一一下，让本脚本在开发机（macOS）与 CI（Linux）产出同一份结构。
if tar --version 2>/dev/null | grep -qi 'gnu tar'; then
  TAR_OWNER=(--owner=0 --group=0 --numeric-owner)
else
  TAR_OWNER=()
fi

# ⚠️ 打包必须走这个函数，不能直接写 `tar "${TAR_OWNER[@]}"`：
# macOS 自带的是 bash 3.2，`set -u` 下对**空数组**做 `"${arr[@]}"` 会报
# `unbound variable`（bash 4.4 才修）。`${arr[@]+"${arr[@]}"}` 是 3.2 上唯一稳的写法。
tar_cz() {
  tar ${TAR_OWNER[@]+"${TAR_OWNER[@]}"} -czf "$1" -C "$2" "$3"
}

rm -rf "$OUT"
mkdir -p "$OUT"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi
}

# ── ① LinuxServer ──────────────────────────────────────────────────────
LS_NAME="NexTerm-$VERSION-linux-amd64"
LS_DIR="$STAGE/$LS_NAME"
mkdir -p "$LS_DIR"
install -m 0755 "$BIN" "$LS_DIR/nexterm-server"
cp -r "$WEB" "$LS_DIR/web"
install -m 0644 "$ROOT/deploy/systemd/nexterm-server.service" "$LS_DIR/nexterm-server.service"
install -m 0644 "$ROOT/LICENSE" "$LS_DIR/LICENSE"
cat > "$LS_DIR/nexterm.env.example" <<'ENVEOF'
# 复制到 /etc/nexterm/nexterm.env 并 chmod 0600。
# 凭据库根密钥：至少 8 位，换掉它 = 换掉整个凭据库（已有密码解不开）。
# 不设也能启动，但密码类资产不可用、同步不带凭据。
NEXTERM_MASTER_KEY=
ENVEOF
cat > "$LS_DIR/README.md" <<'MDEOF'
# NexTerm LinuxServer @VERSION@（linux/amd64）

浏览器版：SSH / SFTP / 文件 / Docker / 数据库 / AI，装进一个浏览器窗口。
浏览器打开 `http://<地址>:8080` 即用（首次启动会自动生成同步令牌，界面「设置 →
资产同步」里能看）。

## 装

```bash
sudo useradd --system --home /var/lib/nexterm --shell /usr/sbin/nologin nexterm
sudo install -d -o nexterm -g nexterm /opt/nexterm /etc/nexterm /var/lib/nexterm
sudo install -m 0755 nexterm-server /opt/nexterm/nexterm-server
sudo cp -r web /opt/nexterm/web

# 凭据库根密钥（>=8 位）。不设也能起，但密码类资产与「带凭据同步」会不可用。
printf 'NEXTERM_MASTER_KEY=%s\n' "$(openssl rand -base64 32)" | sudo tee /etc/nexterm/nexterm.env >/dev/null
sudo chmod 0600 /etc/nexterm/nexterm.env

sudo install -m 0644 nexterm-server.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now nexterm-server
```

## ⛔ 对外之前先读这段

**本服务没有登录页。** `/rpc` 与浏览器界面**没有任何自身鉴权** —— 懒猫微服上是平台
那道登录门在挡，裸 Linux 上那道门**不存在**。所以：

- unit 默认只监听 `127.0.0.1`。要给别人用，请在前面放一个**带鉴权**的反向代理
  （nginx basic auth / oauth2-proxy / caddy + forward_auth），别直接改成 `0.0.0.0`。
- 能连上这个端口的人 = **完整控制权**：终端、任意文件、Docker、凭据库。
- **不建议公网部署**。公网只想做资产同步，请用 `onlyServer` 那个包（`--sync-only`）。

## 运维

- 日志：`/var/lib/nexterm/logs/nexterm.log.<日期>`，**时间是 UTC**（`journalctl`
  里只有启动横幅与风险警告，常规日志走文件）。
- 换端口 / 数据目录：改 unit 里的 `ExecStart`（命令行 > 环境变量 > 内置默认）。
- 令牌：界面「设置 → 资产同步」里查看或轮换；命令行等价物是
  `nexterm-server token --data-dir /var/lib/nexterm`。
MDEOF
sed -i.bak "s/@VERSION@/$VERSION/g" "$LS_DIR/README.md" && rm -f "$LS_DIR/README.md.bak"
tar_cz "$OUT/$LS_NAME.tar.gz" "$STAGE" "$LS_NAME"

# ── ② onlyServer ───────────────────────────────────────────────────────
OS_NAME="NexTerm-onlyServer-$VERSION-linux-amd64"
OS_DIR="$STAGE/$OS_NAME"
mkdir -p "$OS_DIR"
install -m 0755 "$BIN" "$OS_DIR/nexterm-server"
install -m 0644 "$ROOT/deploy/systemd/nexterm-onlyserver.service" "$OS_DIR/nexterm-onlyserver.service"
install -m 0644 "$ROOT/LICENSE" "$OS_DIR/LICENSE"
cat > "$OS_DIR/onlyserver.env.example" <<'ENVEOF'
# 复制到 /etc/nexterm/onlyserver.env 并 chmod 0600。
# 同步过来的密码用这把密钥重封入库。**换掉它 = 已有密码解不开**，请一并备份。
NEXTERM_MASTER_KEY=
ENVEOF
cat > "$OS_DIR/README.md" <<'MDEOF'
# NexTerm onlyServer @VERSION@（linux/amd64）

**只做资产同步**的服务端 —— 给「没有懒猫微服、只有一台公网服务器」的人当同步对端。
它只挂两个端点（`/sync/rpc`、`/healthz`），命令表只有三条
（`sync_digest` / `sync_export` / `sync_import`）：没有浏览器界面，没有 `/rpc`。
所以**令牌泄漏也只能读写这份资产库**，拿不到终端、文件与容器。

## 装

```bash
sudo useradd --system --home /var/lib/nexterm --shell /usr/sbin/nologin nexterm
sudo install -d -o nexterm -g nexterm /opt/nexterm /etc/nexterm /var/lib/nexterm
sudo install -m 0755 nexterm-server /opt/nexterm/nexterm-server

printf 'NEXTERM_MASTER_KEY=%s\n' "$(openssl rand -base64 32)" | sudo tee /etc/nexterm/onlyserver.env >/dev/null
sudo chmod 0600 /etc/nexterm/onlyserver.env

sudo install -m 0644 nexterm-onlyserver.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now nexterm-onlyserver
```

## 取同步令牌（没有界面，只能在命令行）

```bash
sudo -u nexterm /opt/nexterm/nexterm-server token --data-dir /var/lib/nexterm
```

只把令牌写 stdout（可以直接 `TOKEN=$(...)`），说明走 stderr。
换令牌用 `rotate-token` —— **旧令牌立即失效**，正在同步的对端会连不上。
然后在桌面端「设置 → 资产同步」里填 `https://<你的域名>` 加这串令牌。

## 公网必须 TLS

令牌是**明文放在请求头**里的。unit 默认绑 `127.0.0.1` 就是为了让你在前面放个
反代终止 TLS，例如 caddy 两行：

```
sync.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

## 运维

- 健康检查：`curl -s 127.0.0.1:8080/healthz` —— 应看到 `"syncOnly":true` 与
  `"commands":3`。这两个数不对就说明装的不是本包（或参数没生效）。
- 日志：`/var/lib/nexterm/logs/nexterm.log.<日期>`，**时间是 UTC**。
- 换密钥 = 换库（同步过来的密码解不开）。备份 `/etc/nexterm/onlyserver.env`
  与 `/var/lib/nexterm` 这两样。
MDEOF
sed -i.bak "s/@VERSION@/$VERSION/g" "$OS_DIR/README.md" && rm -f "$OS_DIR/README.md.bak"
tar_cz "$OUT/$OS_NAME.tar.gz" "$STAGE" "$OS_NAME"

# ── 汇总 ───────────────────────────────────────────────────────────────
echo
echo "产物："
for f in "$OUT"/*.tar.gz; do
  echo "  $(basename "$f")  $(du -h "$f" | cut -f1)  $(sha256 "$f" | cut -d' ' -f1)"
  # 顺手把内容列出来：发版时最该核对的就是「该在的都在」。
  tar -tzf "$f" | sed 's/^/    /'
done
