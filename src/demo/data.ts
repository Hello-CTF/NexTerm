
export interface DemoAsset {
  id: string;
  groupId: string | null;
  kind: string;
  name: string;
  host: string | null;
  port: number | null;
  username: string | null;
  authKind: string | null;
  keyPath: string | null;
  credId: string | null;
  options: Record<string, unknown>;
  tags: string;
  note: string;
  sort: number;
  createdAt: number;
  updatedAt: number;
  deletedAt: number | null;
  builtin?: boolean;
}

export interface DemoGroup {
  id: string;
  parentId: string | null;
  name: string;
  sort: number;
  createdAt: number;
  updatedAt: number;
}

export interface DemoContainer {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  ports: string;
  composeProject: string | null;
}

export interface DemoImage {
  id: string;
  repository: string;
  tag: string;
  size: string;
  createdSince: string;
}

export interface DemoFileEntry {
  name: string;
  path: string;
  kind: "dir" | "file" | "symlink";
  size: number;
  mode: string;
  owner: string;
  group: string;
  mtime: number;
  symlinkTarget: string | null;
}

export interface DemoAuditEntry {
  id: number;
  ts: number;
  sessionId: string | null;
  assetId: string | null;
  source: string;
  kind: string;
  payload: unknown;
  exitCode: number | null;
  durationMs: number | null;
}

const NOW = Date.now();
const MIN = 60_000;
const HOUR = 3_600_000;
const DAY = 86_400_000;

let seq = 1000;
export function uid(prefix: string) {
  seq += 1;
  return `${prefix}-${seq.toString(36)}${Math.random().toString(36).slice(2, 6)}`;
}


export const groups: DemoGroup[] = [
  { id: "g-company", parentId: null, name: "公司", sort: 1, createdAt: NOW - 90 * DAY, updatedAt: NOW - 90 * DAY },
  { id: "g-test", parentId: null, name: "测试集群", sort: 2, createdAt: NOW - 60 * DAY, updatedAt: NOW - 60 * DAY },
  { id: "g-tools", parentId: null, name: "工具机", sort: 3, createdAt: NOW - 30 * DAY, updatedAt: NOW - 30 * DAY },
];

function asset(
  id: string,
  groupId: string | null,
  kind: string,
  name: string,
  host: string | null,
  port: number | null,
  username: string | null,
  extra: Partial<DemoAsset> = {},
): DemoAsset {
  return {
    id,
    groupId,
    kind,
    name,
    host,
    port,
    username,
    authKind: kind === "local" ? "none" : "password",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: NOW - 20 * DAY,
    updatedAt: NOW - 2 * HOUR,
    deletedAt: null,
    builtin: false,
    ...extra,
  };
}

export const assets: DemoAsset[] = [
  asset("a-local", null, "local", "当前设备", null, null, null, {
    authKind: "none",
    builtin: true,
    sort: -1,
  }),
  asset("a-nat", "g-company", "ssh", "nat-01", "10.0.0.8", 22, "root", { credId: "cred-nat" }),
  asset("a-win", "g-company", "winrm", "win-2019", "10.0.0.21", 5985, "Administrator", {
    authKind: "password",
  }),
  asset("a-web01", "g-test", "ssh", "web-01", "127.0.0.1", 22, "deploy", { credId: "cred-web01" }),
  asset("a-docker", "g-test", "docker", "官网 docker", "127.0.0.1", 22, "deploy"),
  asset("a-mysql", "g-test", "mysql", "db-prod", "127.0.0.1", 3306, "shop_app", {
    credId: "cred-dbprod",
  }),
  asset("a-redis", "g-test", "redis", "redis-cache", "127.0.0.1", 6379, null, {
    authKind: "none",
  }),
  asset("a-build", "g-tools", "ssh", "build-01", "10.0.0.40", 22, "ci", {
    authKind: "key",
    credId: "cred-key",
  }),
];


export const containers: DemoContainer[] = [
  {
    id: "c1a2b3d4e5f6",
    name: "api-server",
    image: "nexterm/api:2.4.1",
    state: "running",
    status: "Up 3 hours",
    ports: "0.0.0.0:8080->8080/tcp",
    composeProject: "官网",
  },
  {
    id: "c2b3c4d5e6f7",
    name: "kingbase-pg",
    image: "kingbase_v009r001c01b0b0C",
    state: "running",
    status: "Up 2 months",
    ports: "172.17.0.4:5432->5432/tcp",
    composeProject: "官网",
  },
  {
    id: "c3c4d5e6f7a8",
    name: "mysql-prod",
    image: "mysql:8.0",
    state: "exited",
    status: "Exited (137) 12 minutes ago",
    ports: "172.17.0.5:3306->3306/tcp",
    composeProject: null,
  },
  {
    id: "c4d5e6f7a8b9",
    name: "redis-cache",
    image: "redis:7-alpine",
    state: "running",
    status: "Up 3 hours (healthy)",
    ports: "6379/tcp",
    composeProject: null,
  },
  {
    id: "c5e6f7a8b9c0",
    name: "kingbase",
    image: "kingbase_v009r001c01b0b0C",
    state: "running",
    status: "Up 2 months",
    ports: "54321/tcp",
    composeProject: null,
  },
  {
    id: "c6f7a8b9c0d1",
    name: "nginx-gateway",
    image: "nginx:1.27-alpine",
    state: "running",
    status: "Up 3 hours",
    ports: "0.0.0.0:80->80/tcp, 0.0.0.0:443->443/tcp",
    composeProject: "官网",
  },
];

export const images: DemoImage[] = [
  { id: "sha256:7dcddc01f13b", repository: "mysql", tag: "8.0", size: "1.09GB", createdSince: "4 months ago" },
  { id: "sha256:7dcddc01f13b", repository: "docker.m.daocloud.io/library/mysql", tag: "8.0", size: "1.09GB", createdSince: "4 months ago" },
  { id: "sha256:1a2b3c4d", repository: "nexterm/api", tag: "2.4.1", size: "212MB", createdSince: "3 days ago" },
  { id: "sha256:2b3c4d5e", repository: "mysql", tag: "8.0", size: "591MB", createdSince: "2 weeks ago" },
  { id: "sha256:3c4d5e6f", repository: "redis", tag: "7-alpine", size: "41.2MB", createdSince: "5 weeks ago" },
  { id: "sha256:4d5e6f7a", repository: "nginx", tag: "1.27-alpine", size: "48.9MB", createdSince: "1 month ago" },
  { id: "sha256:5e6f7a8b", repository: "kingbase_v009r001c01b0b0C", tag: "latest", size: "1.42GB", createdSince: "2 months ago" },
  { id: "sha256:6f7a8b9c", repository: "alpine", tag: "3.20", size: "7.8MB", createdSince: "3 months ago" },
  { id: "sha256:7a8b9c0d", repository: "<none>", tag: "<none>", size: "182MB", createdSince: "2 months ago" },
];

export const containerStats: Record<string, { cpu: string; mem: string }> = {
  "api-server": { cpu: "30.4%", mem: "1.4GB" },
  "kingbase-pg": { cpu: "0.2%", mem: "293.5MB" },
  "mysql-prod": { cpu: "—", mem: "—" },
  "redis-cache": { cpu: "1.1%", mem: "48.2MB" },
  kingbase: { cpu: "0.0%", mem: "330.1MB" },
  "nginx-gateway": { cpu: "0.3%", mem: "18.7MB" },
};


function dir(name: string, parent: string, owner = "root", mtime = NOW - 5 * DAY): DemoFileEntry {
  const path = parent === "/" ? `/${name}` : `${parent}/${name}`;
  return {
    name,
    path,
    kind: "dir",
    size: 4096,
    mode: "drwxr-xr-x",
    owner,
    group: owner === "root" ? "root" : owner,
    mtime,
    symlinkTarget: null,
  };
}

function file(
  name: string,
  parent: string,
  size: number,
  owner = "root",
  mode = "-rw-r--r--",
  mtime = NOW - 3 * DAY,
): DemoFileEntry {
  const path = parent === "/" ? `/${name}` : `${parent}/${name}`;
  return { name, path, kind: "file", size, mode, owner, group: owner, mtime, symlinkTarget: null };
}

export const fsTree: Record<string, DemoFileEntry[]> = {
  "/": [
    dir("bin", "/"),
    dir("boot", "/"),
    dir("data", "/", "deploy"),
    dir("etc", "/"),
    dir("home", "/"),
    dir("opt", "/"),
    dir("root", "/"),
    dir("tmp", "/", "root", NOW - 2 * HOUR),
    dir("usr", "/"),
    dir("var", "/"),
  ],
  "/home": [dir("deploy", "/home", "deploy", NOW - 120 * DAY)],
  "/home/deploy": [
    dir("projects", "/home/deploy", "deploy", NOW - 12 * DAY),
    dir("backups", "/home/deploy", "deploy", NOW - 2 * DAY),
    file(".bash_history", "/home/deploy", 20_994, "deploy", "-rw-------", NOW - 40 * MIN),
    file(".bash_logout", "/home/deploy", 220, "deploy", "-rw-r--r--", NOW - 120 * DAY),
    file(".bashrc", "/home/deploy", 3_771, "deploy", "-rw-r--r--", NOW - 10 * DAY),
    file(".gitconfig", "/home/deploy", 246, "deploy", "-rw-r--r--", NOW - 60 * DAY),
    file(".npmrc", "/home/deploy", 86, "deploy", "-rw-------", NOW - 30 * DAY),
    file(".profile", "/home/deploy", 807, "deploy", "-rw-r--r--", NOW - 120 * DAY),
    file(".python_history", "/home/deploy", 3_412, "deploy", "-rw-------", NOW - 3 * DAY),
    file(".sudo_as_admin_successful", "/home/deploy", 0, "deploy", "-rw-r--r--", NOW - 120 * DAY),
    file(".wget-hsts", "/home/deploy", 152, "deploy", "-rw-r--r--", NOW - 5 * DAY),
    file("1", "/home/deploy", 0, "deploy", "-rw-r--r--", NOW - 20 * DAY),
    file("2024web.zip", "/home/deploy", 132_812_800, "deploy", "-rw-r--r--", NOW - 26 * DAY),
    file("a.tar", "/home/deploy", 1_048_576, "deploy", "-rw-r--r--", NOW - 24 * DAY),
    file("blog.tar", "/home/deploy", 2_097_152, "deploy", "-rw-r--r--", NOW - 24 * DAY),
    file("build_vpn.log", "/home/deploy", 88_214, "deploy", "-rw-r--r--", NOW - 9 * DAY),
    file("Cardinal_v0.7.3_linux_amd64.tar", "/home/deploy", 24_117_248, "deploy", "-rw-r--r--", NOW - 18 * DAY),
    file("cms.tar", "/home/deploy", 3_145_728, "deploy", "-rw-r--r--", NOW - 24 * DAY),
    file("console.log", "/home/deploy", 42_118, "deploy", "-rw-r--r--", NOW - 90 * MIN),
    file("console.pid", "/home/deploy", 6, "deploy", "-rw-r--r--", NOW - 90 * MIN),
    file("dotnet-install.sh", "/home/deploy", 68_144, "deploy", "-rwxr-xr-x", NOW - 33 * DAY),
    file("evil.tar", "/home/deploy", 5_242_880, "deploy", "-rw-r--r--", NOW - 24 * DAY),
    file("reset_snapshot.py", "/home/deploy", 2_184, "deploy", "-rwxr-xr-x", NOW - 6 * DAY),
    file("trade.tar", "/home/deploy", 1_572_864, "deploy", "-rw-r--r--", NOW - 24 * DAY),
    file("web-hard-poker-night.tar", "/home/deploy", 4_194_304, "deploy", "-rw-r--r--", NOW - 24 * DAY),
  ],
  "/home/deploy/projects": [
    dir("api-server", "/home/deploy/projects", "deploy", NOW - 3 * DAY),
    dir("web-console", "/home/deploy/projects", "deploy", NOW - 8 * DAY),
    file("notes.md", "/home/deploy/projects", 1_204, "deploy", "-rw-r--r--", NOW - 3 * DAY),
  ],
  "/home/deploy/projects/api-server": [
    dir("src", "/home/deploy/projects/api-server", "deploy", NOW - 3 * DAY),
    dir("node_modules", "/home/deploy/projects/api-server", "deploy", NOW - 3 * DAY),
    file(".env", "/home/deploy/projects/api-server", 318, "deploy", "-rw-------", NOW - 3 * DAY),
    file("package.json", "/home/deploy/projects/api-server", 986, "deploy"),
    file("tsconfig.json", "/home/deploy/projects/api-server", 542, "deploy"),
  ],
  "/home/deploy/projects/api-server/src": [
    file("index.ts", "/home/deploy/projects/api-server/src", 906, "deploy"),
    file("routes.ts", "/home/deploy/projects/api-server/src", 2_410, "deploy"),
    file("db.ts", "/home/deploy/projects/api-server/src", 1_302, "deploy"),
  ],
  "/home/deploy/projects/web-console": [
    dir("src", "/home/deploy/projects/web-console", "deploy", NOW - 8 * DAY),
    file("package.json", "/home/deploy/projects/web-console", 1_104, "deploy"),
    file("vite.config.ts", "/home/deploy/projects/web-console", 388, "deploy"),
  ],
  "/home/deploy/projects/web-console/src": [
    file("main.tsx", "/home/deploy/projects/web-console/src", 412, "deploy"),
    file("App.tsx", "/home/deploy/projects/web-console/src", 3_688, "deploy"),
  ],
  "/home/deploy/backups": [
    file("api-server-20260925.tar.gz", "/home/deploy/backups", 24_117_248, "deploy", "-rw-r--r--", NOW - DAY),
    file("console-20260924.tar.gz", "/home/deploy/backups", 8_412_672, "deploy", "-rw-r--r--", NOW - 2 * DAY),
  ],
  "/etc": [
    dir("nginx", "/etc"),
    dir("ssh", "/etc", "root", NOW - 40 * DAY),
    dir("systemd", "/etc"),
    file("crontab", "/etc", 1130),
    file("hosts", "/etc", 221),
    file("hostname", "/etc", 14),
    file("passwd", "/etc", 2874),
    file("resolv.conf", "/etc", 154, "root", "-rw-r--r--", NOW - 2 * DAY),
  ],
  "/etc/nginx": [
    dir("conf.d", "/etc/nginx"),
    dir("sites-available", "/etc/nginx"),
    file("fastcgi_params", "/etc/nginx", 1007),
    file("mime.types", "/etc/nginx", 5349),
    file("nginx.conf", "/etc/nginx", 2412, "root", "-rw-r--r--", NOW - 6 * HOUR),
  ],
  "/etc/nginx/conf.d": [file("api.conf", "/etc/nginx/conf.d", 986, "root", "-rw-r--r--", NOW - 6 * HOUR)],
  "/etc/nginx/sites-available": [file("default", "/etc/nginx/sites-available", 1544)],
  "/data": [
    dir("app", "/data", "deploy"),
    dir("backup", "/data", "deploy"),
    dir("logs", "/data", "deploy"),
  ],
  "/data/app": [
    dir("dist", "/data/app", "deploy"),
    dir("node_modules", "/data/app", "deploy", NOW - 12 * DAY),
    dir("src", "/data/app", "deploy"),
    file(".env", "/data/app", 412, "deploy", "-rw-------", NOW - 2 * HOUR),
    file("docker-compose.yml", "/data/app", 1876, "deploy", "-rw-r--r--", NOW - 4 * DAY),
    file("package.json", "/data/app", 1128, "deploy", "-rw-r--r--", NOW - 4 * DAY),
    file("README.md", "/data/app", 3204, "deploy", "-rw-r--r--", NOW - 20 * DAY),
  ],
  "/data/app/src": [
    file("index.ts", "/data/app/src", 824, "deploy"),
    file("server.ts", "/data/app/src", 3120, "deploy"),
    file("db.ts", "/data/app/src", 1478, "deploy"),
  ],
  "/data/app/dist": [file("server.js", "/data/app/dist", 9820, "deploy"), file("server.js.map", "/data/app/dist", 24188, "deploy")],
  "/data/logs": [
    file("app.log", "/data/logs", 4_812_640, "deploy", "-rw-r--r--", NOW - 4 * MIN),
    file("nginx-access.log", "/data/logs", 18_204_112, "deploy", "-rw-r--r--", NOW - MIN),
    file("nginx-error.log", "/data/logs", 902_144, "deploy", "-rw-r--r--", NOW - 18 * MIN),
  ],
  "/data/backup": [file("app-20260925.tar.gz", "/data/backup", 84_213_760, "deploy", "-rw-r--r--", NOW - DAY)],
};

export const fsFileContent: Record<string, string> = {

  "/home/deploy/console.log": `[2026-09-26 17:52:04] serving on http://127.0.0.1:8099
[2026-09-26 18:41:19] GET /tools 200 12ms
[2026-09-26 18:55:40] reload TOOLS registry (18 entries)
Exception occurred during processing of request from ('127.0.0.1', 44234)
Traceback (most recent call last):
  File "/usr/lib/python3.11/socketserver.py", line 691, in process_request_thread
    self.finish_request(request, client_address)
  File "/usr/lib/python3.11/socketserver.py", line 361, in finish_request
    self.RequestHandlerClass(request, client_address, self)
  File "/usr/lib/python3.11/socketserver.py", line 755, in __init__
    self.handle()
  File "/usr/lib/python3.11/http/server.py", line 432, in handle
    self.handle_one_request()
  File "/usr/lib/python3.11/http/server.py", line 428, in handle_one_request
    method()
  File "/home/deploy/projects/api-server/tools/console.py", line 676, in do_GET
    return self.send_200(page, PAGE.replace("{{TOOLS_JSON}}", json.dumps(TOOLS, ensure_ascii=False)).encode())
  File "/usr/lib/python3.11/json/__init__.py", line 238, in dumps
    **kw).encode(obj)
  File "/usr/lib/python3.11/json/encoder.py", line 200, in encode
    chunks = self.iterencode(o, _one_shot=True)
  File "/usr/lib/python3.11/json/encoder.py", line 258, in iterencode
    return _iterencode(o, 0)
TypeError: Object of type datetime is not JSON serializable
`,
  "/home/deploy/.bashrc": String.raw`# ~/.bashrc: executed by bash(1) for non-login shells.

# If not running interactively, don't do anything
case $- in
    *i*) ;;
      *) return;;
esac

HISTCONTROL=ignoreboth
shopt -s histappend
HISTSIZE=1000
HISTFILESIZE=2000
shopt -s checkwinsize

# 让 ls 输出带颜色的那一行在有些镜像里被注释掉了，这里补回来
alias ls='ls --color=auto'
alias ll='ls -alF'
alias la='ls -A'
alias l='ls -CF'

# 运维常用的几个
alias d='docker'
alias dc='docker compose'
alias dps='docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"'
alias logs='journalctl -f -n 200'

# 登录时看看上次加载花了多久
if [ -f ~/.profile ]; then
    . ~/.profile
fi

export PS1='\u@\h:\w\$ '
export EDITOR=vim
`,
  "/home/deploy/.gitconfig": `[user]
	name = deploy
	email = deploy@web-01.internal
[core]
	editor = vim
	autocrlf = input
[alias]
	co = checkout
	br = branch
	lg = log --oneline --graph --decorate -20
[init]
	defaultBranch = main
`,
  "/home/deploy/.npmrc": `registry=https://registry.npmmirror.com
strict-peer-dependencies=false
fund=false
audit=false
`,
  "/home/deploy/.python_history": `import json
from pathlib import Path
tools = json.loads(Path("tools.json").read_text())
for t in tools["items"]:
    print(t["name"], t.get("updated_at"))
`,
  "/home/deploy/.wget-hsts": `# HSTS 1.0 Known Hosts database for GNU Wget.
# Edit at your own risk.
0	github.com	0	1758888000
0	registry.npmmirror.com	0	1758888000
0	download.docker.com	0	1758888000
`,
  "/home/deploy/build_vpn.log": `[2026-09-17 21:04:12] fetching openvpn 2.6.12 ...
[2026-09-17 21:04:19] ./configure --prefix=/opt/openvpn --enable-iproute2
[2026-09-17 21:05:02] make -j8
[2026-09-17 21:07:44] make install
[2026-09-17 21:07:45] done.
[2026-09-17 21:11:03] WARN: cannot open /etc/openvpn/server.conf, skip autostart
[2026-09-17 21:11:03] hint: copy your own server.conf to /etc/openvpn/ first
`,
  "/home/deploy/reset_snapshot.py": `#!/usr/bin/env python3
"""把 /home/deploy/backups 里最近的一份快照恢复回 /data/app。

每天 03:20 由 cron 调用，唯一的作用是"出事时能一键回到昨天"。
"""
import shutil
import subprocess
import sys
from datetime import datetime
from pathlib import Path

BACKUP_DIR = Path("/home/deploy/backups")
TARGET = Path("/data/app")
KEEP_DAYS = 7


def latest_snapshot() -> Path:
    snaps = sorted(BACKUP_DIR.glob("api-server-*.tar.gz"))
    if not snaps:
        sys.exit("no snapshot found in " + str(BACKUP_DIR))
    return snaps[-1]


def prune() -> None:
    cutoff = datetime.now().timestamp() - KEEP_DAYS * 86400
    for old in BACKUP_DIR.glob("*.tar.gz"):
        if old.stat().st_mtime < cutoff:
            old.unlink()
            print("pruned", old.name)


def main() -> None:
    snap = latest_snapshot()
    print("restoring", snap.name)
    subprocess.run(["tar", "-xzf", str(snap), "-C", str(TARGET)], check=True)
    prune()


if __name__ == "__main__":
    main()
`,
  "/home/deploy/dotnet-install.sh": String.raw`#!/usr/bin/env bash
# 官方脚本的精简版，只保留我们实际用到的分支
set -e

channel="$1"
install_dir="$2"

if [ -z "$channel" ]; then
    channel="LTS"
fi

if [ -z "$install_dir" ]; then
    install_dir="$HOME/.dotnet"
fi

say() { printf "%s\n" "$*" >&2; }

say "downloading dotnet $channel ..."
mkdir -p "$install_dir"
curl -fsSL "https://dot.net/v1/dotnet-install.sh" -o /tmp/dotnet-install.sh
bash /tmp/dotnet-install.sh --channel "$channel" --install-dir "$install_dir"

export PATH="$install_dir:$PATH"
dotnet --version
say "done. add $install_dir to PATH if you want it persistent."
`,
  "/home/deploy/projects/notes.md": `# 待办

## api-server
- [x] 把 502 那条链路的超时从 5s 调到 15s
- [ ] console.py 的 TOOLS 里带 datetime，json.dumps 会炸（见 ~/console.log）
- [ ] 给 /healthz 加上 DB 连通性检查

## 机器
- [ ] /data 分区用了 82%，考虑把 backups 挪到 /data/backup
- [ ] 把 dotnet-install.sh 换成官方源，现在这份是改过的

## 记一下
nginx 的 upstream 指向 127.0.0.1:8080，也就是本机的 api-server 容器。
容器重建后不用动 nginx 配置，因为走的是端口而不是容器 IP。
`,
  "/home/deploy/projects/api-server/package.json": `{
  "name": "api-server",
  "version": "2.4.1",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "tsx watch src/index.ts",
    "build": "tsc -p tsconfig.json",
    "start": "node dist/index.js"
  },
  "dependencies": {
    "fastify": "^4.28.1",
    "mysql2": "^3.11.0",
    "pino": "^9.3.2"
  },
  "devDependencies": {
    "tsx": "^4.17.0",
    "typescript": "^5.5.4"
  }
}
`,
  "/home/deploy/projects/api-server/.env": `NODE_ENV=production
PORT=8080
LOG_LEVEL=info
DB_HOST=127.0.0.1
DB_PORT=3306
DB_NAME=shop
DB_USER=shop_app
DB_POOL=12
`,
  "/home/deploy/projects/api-server/tsconfig.json": `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "strict": true,
    "outDir": "dist",
    "rootDir": "src"
  },
  "include": ["src"]
}
`,
  "/home/deploy/projects/api-server/src/index.ts": `import Fastify from "fastify";
import { registerRoutes } from "./routes.js";
import { pool } from "./db.js";

const app = Fastify({ logger: { level: process.env.LOG_LEVEL ?? "info" } });

await registerRoutes(app);

app.get("/healthz", async () => {
  const [rows] = await pool.query("SELECT 1 AS ok");
  return { ok: rows[0].ok === 1, uptime: process.uptime() };
});

const port = Number(process.env.PORT ?? 8080);
await app.listen({ port, host: "0.0.0.0" });
`,
  "/home/deploy/projects/api-server/src/db.ts": `import mysql from "mysql2/promise";

export const pool = mysql.createPool({
  host: process.env.DB_HOST ?? "127.0.0.1",
  port: Number(process.env.DB_PORT ?? 3306),
  database: process.env.DB_NAME ?? "shop",
  user: process.env.DB_USER ?? "shop_app",
  password: process.env.DB_PASSWORD,
  connectionLimit: Number(process.env.DB_POOL ?? 12),
  namedPlaceholders: true,
});

export async function ping(): Promise<boolean> {
  try {
    await pool.query("SELECT 1");
    return true;
  } catch {
    return false;
  }
}
`,
  "/home/deploy/projects/api-server/src/routes.ts": `import type { FastifyInstance } from "fastify";
import { pool } from "./db.js";

export async function registerRoutes(app: FastifyInstance) {
  app.get("/api/orders/:id", async (req, reply) => {
    const { id } = req.params as { id: string };
    const [rows] = await pool.query("SELECT * FROM orders WHERE order_no = ?", [id]);
    if (!rows.length) return reply.code(404).send({ error: "not_found" });
    return rows[0];
  });

  app.get("/api/stats/daily", async () => {
    const [rows] = await pool.query(
      "SELECT DATE(created_at) d, COUNT(*) n FROM orders GROUP BY d ORDER BY d DESC LIMIT 30",
    );
    return rows;
  });
}
`,
  "/etc/nginx/nginx.conf": `user  nginx;
worker_processes  auto;

error_log  /var/log/nginx/error.log notice;
pid        /var/run/nginx.pid;

events {
    worker_connections  1024;
}

http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;

    log_format  main  '$remote_addr - $remote_user [$time_local] "$request" '
                      '$status $body_bytes_sent "$http_referer" '
                      '"$http_user_agent" "$http_x_forwarded_for"';

    access_log  /var/log/nginx/access.log  main;

    sendfile        on;
    keepalive_timeout  65;

    # 上游：本机的 api-server 容器
    upstream api_backend {
        server 127.0.0.1:8080;
        keepalive 32;
    }

    server {
        listen       80;
        server_name  _;

        location /api/ {
            proxy_pass         http://api_backend/;
            proxy_http_version 1.1;
            proxy_set_header   Host              $host;
            proxy_set_header   X-Real-IP         $remote_addr;
            proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
            proxy_read_timeout 60s;
        }

        location / {
            root   /usr/share/nginx/html;
            index  index.html;
        }
    }
}
`,
  "/data/app/docker-compose.yml": `services:
  api-server:
    image: nexterm/api:2.4.1
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      NODE_ENV: production
      REDIS_URL: redis://redis-cache:6379/0
      DB_URL: mysql://shop_app@mysql-prod:3306/shop
    deploy:
      resources:
        limits:
          memory: 2g
    depends_on:
      - redis-cache
      - mysql-prod

  redis-cache:
    image: redis:7-alpine
    restart: unless-stopped
    command: redis-server --maxmemory 512mb --maxmemory-policy allkeys-lru

  mysql-prod:
    image: mysql:8.0
    restart: unless-stopped
    environment:
      MYSQL_DATABASE: shop
      MYSQL_USER: shop_app
      MYSQL_PASSWORD: \${MYSQL_PASSWORD}
    volumes:
      - mysql-data:/var/lib/mysql

volumes:
  mysql-data:
`,
  "/data/app/.env": `NODE_ENV=production
PORT=8080
REDIS_URL=redis://127.0.0.1:6379/0
DB_HOST=127.0.0.1
DB_PORT=3306
DB_USER=shop_app
DB_PASSWORD=********
JWT_SECRET=********
LOG_LEVEL=info
`,
  "/data/app/package.json": `{
  "name": "nexterm-api",
  "version": "2.4.1",
  "private": true,
  "type": "module",
  "scripts": {
    "build": "tsc -p tsconfig.json",
    "start": "node dist/server.js",
    "test": "vitest run"
  },
  "dependencies": {
    "express": "^4.21.2",
    "ioredis": "^5.4.1",
    "mysql2": "^3.11.5",
    "zod": "^3.24.1"
  }
}
`,
  "/data/app/README.md": `# nexterm-api

官网后端服务。负责商品目录、下单、支付回调。

## 本地启动

    cp .env.example .env
    pnpm install
    pnpm dev

## 部署

生产环境跑在 web-01 上，由 docker compose 托管：

    docker compose pull
    docker compose up -d

## 注意

- 内存上限 2g（见 docker-compose.yml 的 deploy.resources），
  历史上因为没设上限被 OOM Killer 干掉过一次。
- 依赖 redis-cache 与 mysql-prod，启动顺序由 depends_on 保证。
`,
  "/data/app/src/server.ts": `import express from "express";
import { createClient } from "ioredis";
import { pool } from "./db.js";

const app = express();
const redis = createClient({ url: process.env.REDIS_URL });

app.use(express.json());

app.get("/healthz", async (_req, res) => {
  const [db] = await pool.query("SELECT 1 AS ok");
  res.json({ ok: true, db: db[0].ok === 1, redis: redis.status === "ready" });
});

app.get("/api/products", async (req, res) => {
  const cacheKey = \`products:\${req.query.channel ?? "all"}\`;
  const cached = await redis.get(cacheKey);
  if (cached) return res.type("application/json").send(cached);

  const [rows] = await pool.query(
    "SELECT id, name, price FROM products WHERE listed = 1 ORDER BY sort ASC LIMIT 200",
  );
  await redis.setex(cacheKey, 300, JSON.stringify(rows));
  res.json(rows);
});

app.listen(Number(process.env.PORT ?? 8080), () => {
  // eslint-disable-next-line no-console
  console.warn("api-server listening on", process.env.PORT ?? 8080);
});
`,
  "/data/logs/app.log": `${new Date(NOW - 26 * MIN).toISOString()} INFO  server started, pid=1183 port=8080
${new Date(NOW - 25 * MIN).toISOString()} INFO  redis connected (redis-cache:6379)
${new Date(NOW - 25 * MIN).toISOString()} INFO  mysql pool ready, size=10
${new Date(NOW - 18 * MIN).toISOString()} WARN  pool exhausted, waiting for a free connection (queued=7)
${new Date(NOW - 16 * MIN).toISOString()} WARN  pool exhausted, waiting for a free connection (queued=23)
${new Date(NOW - 12 * MIN).toISOString()} ERROR read ECONNRESET from mysql-prod:3306
${new Date(NOW - 12 * MIN).toISOString()} ERROR db connection lost, retrying in 1000ms
${new Date(NOW - 11 * MIN).toISOString()} ERROR db connection lost, retrying in 2000ms
${new Date(NOW - 9 * MIN).toISOString()} WARN  upstream 502 for /api/products (db unavailable)
${new Date(NOW - 3 * MIN).toISOString()} WARN  upstream 502 for /api/orders
`,
  "/etc/hosts": `127.0.0.1       localhost
127.0.1.1       web-01
10.0.0.8        nat-01
10.0.0.21       win-2019
`,
};


export const mysqlSchemas = ["information_schema", "mysql", "performance_schema", "shop", "sys"];

export const mysqlTables: Record<string, string[]> = {
  shop: [
    "categories",
    "coupons",
    "inventory",
    "order_items",
    "orders",
    "payments",
    "products",
    "refunds",
    "shipments",
    "users",
    "user_addresses",
    "user_coupons",
  ],
};

export const mysqlColumns: Record<
  string,
  { name: string; type: string; nullable: boolean; key: string; default: string | null; extra: string }[]
> = {
  orders: [
    { name: "id", type: "bigint unsigned", nullable: false, key: "PRI", default: null, extra: "auto_increment" },
    { name: "order_no", type: "varchar(32)", nullable: false, key: "UNI", default: null, extra: "" },
    { name: "user_id", type: "bigint unsigned", nullable: false, key: "MUL", default: null, extra: "" },
    { name: "channel", type: "varchar(24)", nullable: false, key: "MUL", default: null, extra: "" },
    { name: "amount", type: "int unsigned", nullable: false, key: "", default: null, extra: "" },
    { name: "refunded", type: "int unsigned", nullable: false, key: "", default: "0", extra: "" },
    { name: "status", type: "varchar(16)", nullable: false, key: "MUL", default: "pending", extra: "" },
    { name: "created_at", type: "datetime", nullable: false, key: "MUL", default: null, extra: "" },
    { name: "paid_at", type: "datetime", nullable: true, key: "", default: null, extra: "" },
  ],
  users: [
    { name: "id", type: "bigint unsigned", nullable: false, key: "PRI", default: null, extra: "auto_increment" },
    { name: "nickname", type: "varchar(48)", nullable: false, key: "", default: null, extra: "" },
    { name: "phone", type: "varchar(20)", nullable: false, key: "UNI", default: null, extra: "" },
    { name: "channel", type: "varchar(24)", nullable: true, key: "MUL", default: null, extra: "" },
    { name: "registered_at", type: "datetime", nullable: false, key: "MUL", default: null, extra: "" },
  ],
};

export const mysqlIndexes: Record<
  string,
  { name: string; unique: boolean; column: string; seq: number }[]
> = {
  orders: [
    { name: "PRIMARY", unique: true, column: "id", seq: 1 },
    { name: "uk_order_no", unique: true, column: "order_no", seq: 1 },
    { name: "idx_user_created", unique: false, column: "user_id", seq: 1 },
    { name: "idx_user_created", unique: false, column: "created_at", seq: 2 },
    { name: "idx_channel", unique: false, column: "channel", seq: 1 },
    { name: "idx_status", unique: false, column: "status", seq: 1 },
  ],
  users: [
    { name: "PRIMARY", unique: true, column: "id", seq: 1 },
    { name: "uk_phone", unique: true, column: "phone", seq: 1 },
    { name: "idx_channel", unique: false, column: "channel", seq: 1 },
  ],
};

export function fakeQuery(sql: string): {
  columns: string[];
  rows: unknown[][];
  rowsAffected: number;
  durationMs: number;
  truncated: boolean;
  error: string | null;
} {
  const s = sql.toLowerCase();
  const base = { rowsAffected: 0, durationMs: 38, truncated: false, error: null };
  if (s.includes("from orders")) {
    return {
      ...base,
      durationMs: 42,
      columns: ["channel", "orders", "gmv_yuan", "refund_pct"],
      rows: [
        ["app-ios", 12480, 1284900.0, 1.82],
        ["app-android", 11203, 1142380.5, 2.04],
        ["mini-program", 8771, 902114.0, 1.31],
        ["web", 5016, 488203.75, 3.47],
        ["h5", 1942, 176540.0, 2.88],
      ],
    };
  }
  if (s.includes("from users")) {
    return {
      ...base,
      durationMs: 27,
      columns: ["id", "nickname", "phone", "channel", "registered_at"],
      rows: [
        [90211, "青柠", "138****2043", "app-ios", "2026-03-11 09:20:14"],
        [90212, "老周", "139****7710", "web", "2026-03-11 10:02:55"],
        [90214, "阿町", "150****3312", "mini-program", "2026-03-12 21:44:08"],
        [90217, "Kai", "186****9987", "app-android", "2026-03-14 08:15:31"],
      ],
    };
  }
  if (s.includes("from products")) {
    return {
      ...base,
      durationMs: 19,
      columns: ["id", "name", "price", "stock", "listed"],
      rows: [
        [5001, "冷萃咖啡液 30 条", 12900, 412, 1],
        [5002, "手冲滤纸 100 张", 3900, 1873, 1],
        [5003, "陶瓷分享壶 600ml", 18900, 96, 1],
        [5007, "便携磨豆机", 25900, 0, 0],
      ],
    };
  }
  if (s.includes("count(")) {
    return { ...base, durationMs: 12, columns: ["count(*)"], rows: [[184_322]] };
  }
  if (s.startsWith("select 1")) {
    return { ...base, durationMs: 4, columns: ["1"], rows: [[1]] };
  }
  if (s.startsWith("update") || s.startsWith("delete") || s.startsWith("insert")) {
    return { ...base, rowsAffected: 1, durationMs: 16, columns: [], rows: [] };
  }
  if (/\b(create|alter|drop|truncate|rename)\b/.test(s)) {
    return { ...base, durationMs: 2, columns: [], rows: [], error: "演示模式：已拦截 DDL/结构变更语句，仅支持查询与数据修改" };
  }
  return { ...base, durationMs: 6, columns: ["result"], rows: [["OK"]], rowsAffected: 0 };
}

export const redisKeys = [
  "products:all",
  "products:app-ios",
  "products:web",
  "session:u90211",
  "session:u90212",
  "cart:u90211",
  "rate:ip:10.0.0.8",
  "lock:order:20260926-8891",
  "queue:notify",
  "stats:daily:20260926",
];

export const redisValues: Record<string, { keyType: string; ttl: number; value: unknown }> = {
  "products:all": {
    keyType: "string",
    ttl: 271,
    value: '[{"id":5001,"name":"冷萃咖啡液 30 条","price":12900},{"id":5002,"name":"手冲滤纸 100 张","price":3900}]',
  },
  "session:u90211": {
    keyType: "hash",
    ttl: 1724,
    value: { userId: 90211, channel: "app-ios", loginAt: "2026-09-26T09:20:14+08:00", device: "iPhone15,3" },
  },
  "cart:u90211": {
    keyType: "list",
    ttl: 604_800,
    value: ["5001:2", "5002:1", "5003:1"],
  },
  "lock:order:20260926-8891": {
    keyType: "string",
    ttl: 27,
    value: "1",
  },
  "stats:daily:20260926": {
    keyType: "zset",
    ttl: -1,
    value: { "app-ios": 12480, "app-android": 11203, "mini-program": 8771 },
  },
};


export const mounts: { id: string; localPoint: string; remote: string; sessionId: string | null; createdAt: number | null }[] = [
  { id: "m1", localPoint: "Z:", remote: "\\\\10.0.0.8\\share", sessionId: "s-nat", createdAt: NOW - 3 * DAY },
  { id: "m2", localPoint: "Y:", remote: "\\\\10.0.0.21\\C$", sessionId: "s-win", createdAt: NOW - 8 * DAY },
];

export const forwards: {
  id: string;
  sessionId: string;
  listenPort: number;
  targetHost: string | null;
  targetPort: number | null;
  kind: string;
  createdAt: number;
}[] = [
  {
    id: "f1",
    sessionId: "s-web01",
    listenPort: 13306,
    targetHost: "172.17.0.5",
    targetPort: 3306,
    kind: "local",
    createdAt: NOW - HOUR,
  },
  {
    id: "f2",
    sessionId: "s-web01",
    listenPort: 1080,
    targetHost: null,
    targetPort: null,
    kind: "socks",
    createdAt: NOW - 40 * MIN,
  },
];

export const auditEntries: DemoAuditEntry[] = [
  { id: 41, ts: NOW - 2 * MIN, sessionId: "s-web01", assetId: "a-web01", source: "ai", kind: "exec_commands", payload: { commands: ["docker ps --format '{{.Names}}\\t{{.Status}}'"] }, exitCode: 0, durationMs: 88 },
  { id: 40, ts: NOW - 3 * MIN, sessionId: "s-web01", assetId: "a-web01", source: "ai", kind: "exec_commands", payload: { commands: ["free -h"] }, exitCode: 0, durationMs: 41 },
  { id: 39, ts: NOW - 12 * MIN, sessionId: null, assetId: null, source: "user", kind: "ai_confirm", payload: { tool: "docker_control", rendered: "docker restart mysql-prod", decision: "allow" }, exitCode: null, durationMs: null },
  { id: 38, ts: NOW - 22 * MIN, sessionId: "s-web01", assetId: "a-web01", source: "user", kind: "terminal_command", payload: { line: "systemctl status nginx" }, exitCode: 0, durationMs: 41 },
  { id: 37, ts: NOW - 41 * MIN, sessionId: "s-web01", assetId: "a-web01", source: "user", kind: "fs_write", payload: { path: "/etc/nginx/nginx.conf", backup: "/etc/nginx/nginx.conf.nexterm-bak" }, exitCode: 0, durationMs: 122 },
  { id: 36, ts: NOW - 55 * MIN, sessionId: "s-web01", assetId: "a-web01", source: "user", kind: "terminal_command", payload: { line: "nginx -t" }, exitCode: 0, durationMs: 33 },
  { id: 35, ts: NOW - 68 * MIN, sessionId: "s-mysql", assetId: "a-mysql", source: "user", kind: "db_query", payload: { sql: "SELECT channel, COUNT(*) FROM orders GROUP BY channel" }, exitCode: null, durationMs: 42 },
  { id: 34, ts: NOW - 96 * MIN, sessionId: "s-mysql", assetId: "a-mysql", source: "ai", kind: "db_query", payload: { sql: "SELECT * FROM orders WHERE created_at >= DATE_SUB(NOW(), INTERVAL 1 DAY) LIMIT 20" }, exitCode: null, durationMs: 31 },
  { id: 33, ts: NOW - 2 * HOUR, sessionId: "s-web01", assetId: "a-web01", source: "user", kind: "mount_create", payload: { localPoint: "Y:", remote: "\\\\10.0.0.21\\C$" }, exitCode: 0, durationMs: 640 },
  { id: 32, ts: NOW - 3 * HOUR, sessionId: null, assetId: "a-docker", source: "user", kind: "docker_action", payload: { container: "api-server", action: "restart" }, exitCode: 0, durationMs: 1810 },
  { id: 31, ts: NOW - 4 * HOUR, sessionId: null, assetId: null, source: "ai", kind: "guard_block", payload: { rendered: "rm -rf /var/lib/mysql", reason: "forbidden: rm -rf 根路径" }, exitCode: null, durationMs: null },
  { id: 30, ts: NOW - 6 * HOUR, sessionId: "s-web01", assetId: "a-web01", source: "ai", kind: "takeover_enter", payload: { task: "安装 nginx 并启动", allowWrite: true }, exitCode: null, durationMs: null },
];

export const providerPresets = ["deepseek", "openai", "dashscope", "moonshot", "zhipu", "ollama", "lmstudio", "vllm"];

export const providerConfig = {
  baseUrl: "https://api.deepseek.com/v1",
  apiKey: "sk-demo-0000000000000000000000000000",
  model: "deepseek-chat",
  temperature: 0.3,
  contextWindow: 64000,
  proxy: null as string | null,
  stream: true,
};

export const modelState: {
  profiles: {
    id: string;
    name: string;
    baseUrl: string;
    apiKey: string;
    model: string;
    temperature: number;
    contextWindow: number;
    proxy: string | null;
    stream: boolean;
  }[];
  activeId: string;
} = {
  profiles: [
    {
      id: "m-deepseek",
      name: "DeepSeek 主力",
      baseUrl: "https://api.deepseek.com/v1",
      apiKey: "sk-demo-0000000000000000000000000000",
      model: "deepseek-chat",
      temperature: 0.3,
      contextWindow: 64000,
      proxy: null,
      stream: true,
    },
    {
      id: "m-glm",
      name: "智谱备用",
      baseUrl: "https://open.bigmodel.cn/api/paas/v4",
      apiKey: "sk-demo-1111111111111111111111111111",
      model: "glm-4-flash",
      temperature: 0.3,
      contextWindow: 128000,
      proxy: null,
      stream: true,
    },
    {
      id: "m-local",
      name: "本机 Ollama",
      baseUrl: "http://127.0.0.1:11434/v1",
      apiKey: "",
      model: "qwen2.5:7b",
      temperature: 0.3,
      contextWindow: 32000,
      proxy: null,
      stream: true,
    },
  ],
  activeId: "m-deepseek",
};

export const permissionConfig: {
  mode: "read_only" | "read_write" | "silent";
  dangerRules: string[];
} = {
  mode: "read_write",
  dangerRules: ["kubectl delete"],
};

export const conversations: {
  id: string;
  title: string;
  scope: unknown;
  createdAt: number;
  updatedAt: number;
}[] = [
  {
    id: "conv-1",
    title: "排查 api-server 502",
    scope: { sessionId: "s-web01", tabId: null, connId: null, assetId: "a-web01" },
    createdAt: NOW - 20 * MIN,
    updatedAt: NOW - 2 * MIN,
  },
  {
    id: "conv-2",
    title: "看看机器运行的服务",
    scope: { sessionId: "s-web01", tabId: null, connId: null, assetId: "a-web01" },
    createdAt: NOW - 90 * MIN,
    updatedAt: NOW - 88 * MIN,
  },
];

export const conversationMessages: Record<
  string,
  {
    id: string;
    role: string;
    content: unknown;
    tokensIn: number | null;
    tokensOut: number | null;
    createdAt: number;
  }[]
> = {
  "conv-1": [
    {
      id: "msg-1",
      role: "user",
      content: { role: "user", content: "api-server 一直 502，帮我看看", imageCount: 0 },
      tokensIn: null,
      tokensOut: null,
      createdAt: NOW - 20 * MIN,
    },
    {
      id: "msg-2",
      role: "assistant",
      content: {
        role: "assistant",
        content:
          "定位到了，**不是 nginx 的问题**。\n\n" +
          "1. `mysql-prod` 在 12 分钟前被 OOM Killer 干掉（退出码 137），`api-server` 连不上库。\n" +
          "2. 机器 7.7G 内存已被占满，`api-server` 日志里能看到 `pool exhausted`。\n\n" +
          "| 容器 | 状态 | 说明 |\n| --- | --- | --- |\n| `mysql-prod` | 已退出 (137) | 被 OOM Killer 干掉 |\n| `api-server` | 运行中 | 连不上库 → 对外一直 502 |",
      },
      tokensIn: 8414,
      tokensOut: 927,
      createdAt: NOW - 19 * MIN,
    },
    {
      id: "msg-3",
      role: "user",
      content: { role: "user", content: "那先把它拉起来", imageCount: 0 },
      tokensIn: null,
      tokensOut: null,
      createdAt: NOW - 3 * MIN,
    },
    {
      id: "msg-4",
      role: "assistant",
      content: {
        role: "assistant",
        content:
          "好。**先加上限再拉起来**，否则大概率又被 OOM 挑中：\n\n" +
          "```bash\n$ docker update --memory 2g api-server\n$ docker start mysql-prod\n```\n" +
          "起来后盯 5 分钟 `docker ps`，看它会不会再退。",
      },
      tokensIn: 2988,
      tokensOut: 282,
      createdAt: NOW - 2 * MIN,
    },
  ],
  "conv-2": [
    {
      id: "msg-5",
      role: "user",
      content: { role: "user", content: "帮我看看机器运行的服务", imageCount: 0 },
      tokensIn: null,
      tokensOut: null,
      createdAt: NOW - 90 * MIN,
    },
    {
      id: "msg-6",
      role: "assistant",
      content: {
        role: "assistant",
        content:
          "这台机器（linuxcore）当前运行的服务：\n\n" +
          "- `docker` / `containerd` — 容器运行时\n" +
          "- `1panel` — 1Panel 面板\n" +
          "- `ssh` / `tailscaled` / `openvpn-client@linuxcore`\n\n" +
          "✅ 没有失败单元。",
      },
      tokensIn: 2988,
      tokensOut: 312,
      createdAt: NOW - 88 * MIN,
    },
  ],
};
