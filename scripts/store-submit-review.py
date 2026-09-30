#!/usr/bin/env python3
"""把 NexTerm 提审到懒猫商店 —— 一条命令走完「传截图 → 传 LPK → 提审」。

**为什么不是 `lzc-cli appstore publish`**：那个命令**只发 `version`、不带 `infos`**，
所以它只适用于「应用资料已经在开发者控制台里填过」的场景。首次提审（以及每次
改简介 / 截图后重新提审）必须自己发，把 `infos` 一起交上去 —— 应用资料
**没有独立保存接口**，只在提审时随请求落库。

完整链路与实测坑见 `docs/LAZYCAT-PORT.md` §15.8。三个最容易写错的点：

1. `infos[].language` 是**短码** `zh` / `en`，不是 manifest `locales` 里的 `zh-CN` / `en-US`
   （判据：`GET /developer/app/<id>` 的 `info_data` 键名）。
2. 截图上传端点的 multipart 字段名是 **`file`**（不是 `image` —— 字段名错会回
   Go 的 `http: no such file`，容易误判成「沙箱读不到文件」）。
3. 提审体里**不能带 `submit_channel`**，否则回
   `400 submit_channel is not allowed for this review endpoint`（那是控制台另一个端点的参数）。

用法::

    python3 scripts/store-submit-review.py                 # 全流程提审
    python3 scripts/store-submit-review.py --dry-run       # 只组装并打印，不联网
    python3 scripts/store-submit-review.py --status        # 只看审核队列里本应用的记录

凭据：`~/.config/lazycat/box-config.json` 的 `token`（= 社区账号的 userToken），只读不外传。
身份头 `X-User-Token`；服务端同时也认 `cookie: userToken=<token>`。
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PKG = "cloud.lazycat.app.nexterm"
API = "https://appstore.api.lazycat.cloud/api/v3/developer"
TOKEN_FILE = os.path.expanduser("~/.config/lazycat/box-config.json")

# ------------------------------------------------------------------ 商店文案
# `brief` 必填（控制台表单上有红星）。`keywords` 用英文逗号分隔。
# `source` 的 label 是「项目移植来源 URL 地址」—— 本项目为原创，留空。
LANGUAGES = [
    {
        "language": "zh",
        "name": "NexTerm 运维终端",
        "brief": "浏览器里的 SSH / SFTP / Docker / 数据库一体化运维终端，凭据加密后存在你自己的微服上。",
        "description": """\
NexTerm 把日常运维要开的一堆工具收进一个浏览器窗口：SSH 终端、SFTP 文件管理、Docker 容器、数据库客户端、端口转发与磁盘挂载，再加上一个能读懂终端输出的 AI 助手。

主要能力
· 终端：多标签与分屏、会话录制、日志导出
· 文件：SFTP 浏览与编辑，支持拖拽上传下载
· Docker：容器与镜像列表、启停、日志查看
· 数据库：MySQL / PostgreSQL / Redis 连接与查询
· 端口转发：SSH 隧道一键映射，浏览器直接访问内网服务
· 磁盘挂载：通过 sshfs 把远端目录挂进工作区
· AI 助手：命令按风险分级、危险操作先确认、改文件留前后对照

凭据安全
所有主机凭据由微服注入的根密钥加密后保存在你自己的盒子上，不上传任何第三方。浏览器版为了让「打开浏览器就能连服务器」成立，进程内存里会持有解密后的密钥——盒子是您自己的硬件。需要更强隔离时，请不要在这台微服上保存生产环境的口令，改用密钥认证。

数据持久化
配置与凭据保存在 /lzcapp/var，随应用升级保留。""",
        "keywords": "SSH,SFTP,Docker,数据库,终端,运维,端口转发,隧道,AI助手",
    },
    {
        "language": "en",
        "name": "NexTerm",
        "brief": "An all-in-one SSH / SFTP / Docker / database ops terminal in your browser, with credentials encrypted and stored on your own microserver.",
        "description": """\
NexTerm puts the tools you open every day into a single browser window: an SSH terminal, SFTP file manager, Docker containers, a database client, port forwarding and disk mounting — plus an AI assistant that can read your terminal output.

Capabilities
- Terminal: tabs and split panes, session recording, log export
- Files: SFTP browsing and editing, drag-and-drop upload/download
- Docker: container and image lists, start/stop, logs
- Databases: MySQL / PostgreSQL / Redis connections and queries
- Port forwarding: one-click SSH tunnels that reach internal services from the browser
- Disk mounting: mount remote directories via sshfs
- AI assistant: commands graded by risk, dangerous operations confirmed first, file edits shown as before/after diffs

Credential safety
All host credentials are encrypted with a root key injected by your microserver and stored on your own device — nothing is sent to a third party. In the browser build the decrypted key is held in process memory so that "open a browser and connect" works; the microserver is your own hardware. If you need stronger isolation, do not store production passwords here — use key authentication.

Data persistence
Configuration and credentials live in /lzcapp/var and survive app upgrades.""",
        "keywords": "SSH,SFTP,Docker,database,terminal,ops,port forwarding,tunnel,AI assistant",
    },
]

DEFAULT_CHANGELOG = {
    "zh": "首个版本：SSH / SFTP / Docker / 数据库一体化终端，含 AI 助手、端口转发与磁盘挂载。",
    "en": "First release: an all-in-one SSH / SFTP / Docker / database terminal with an AI assistant, port forwarding and disk mounting.",
}


def token() -> str:
    return json.load(open(TOKEN_FILE))["token"]


def curl_json(url: str, extra: list[str], timeout: int = 300) -> dict:
    tok = token()
    cmd = [
        "curl", "-sS", "--max-time", str(timeout),
        "-H", f"X-User-Token: {tok}",
        "-H", f"cookie: userToken={tok}",
        *extra, url,
    ]
    out = subprocess.run(cmd, capture_output=True, text=True).stdout.strip()
    try:
        return json.loads(out)
    except json.JSONDecodeError:
        raise SystemExit(f"非 JSON 响应：{out[:400]}")


def upload_image(path: str) -> str:
    """上传一张截图，返回 `screenshot_pc_paths` 用的 url。

    字段名必须是 `file`：写别的会回 Go 的 `http: no such file`（= ErrMissingFile），
    那个文案会把人带偏到「沙箱读不到文件」上去。
    """
    r = curl_json(f"{API}/upload", ["-F", f"file=@{path}"], timeout=120)
    if "url" not in r:
        raise SystemExit(f"截图上传失败：{r}")
    return r["url"]


def upload_lpk(path: str) -> dict:
    r = curl_json(f"{API}/app/lpk/upload", ["-F", f"file=@{path}"])
    if "package" not in r:
        raise SystemExit(f"LPK 上传失败：{r}")
    return r


def build_body(up: dict, shots: list[str], changelog: dict) -> dict:
    infos = [
        {
            **lang,
            "source": "",
            "source_author": "",
            "support_pc": True,
            "support_mobile": False,
            "screenshot_pc_paths": shots,
            "screenshot_mobile_paths": [],
        }
        for lang in LANGUAGES
    ]
    version = {
        "package": up["package"],
        "name": up["version"],
        "icon_path": up["iconPath"],
        "pkg_path": up["url"],
        "pkg_hash": up["sha256"],
        "unsupported_platforms": up.get("unsupportedPlatforms") or [],
        "min_os_version": up.get("minOsVersion") or "",
        "lpk_size": up["lpkSize"],
        "image_size": up.get("imageSize") or 0,
        "changelogs": changelog,
    }
    # 刻意不带 submit_channel：本端点会回 400（见模块 docstring 第 3 条）。
    return {"infos": infos, "version": version}


def default_lpk() -> str:
    cands = sorted(
        p for p in glob.glob(os.path.join(REPO, "lazycat", "*.lpk"))
        if ".dev" not in os.path.basename(p)
    )
    if not cands:
        raise SystemExit("没找到正式 LPK：先跑 `cd lazycat && lzc-cli project release`")
    return cands[-1]


def show_status() -> None:
    r = curl_json(f"{API}/app/checks/in_reviews", ["-X", "GET"], timeout=30)
    mine = [
        it for it in r.get("items", [])
        if (it.get("app") or {}).get("package") == PKG
    ]
    if not mine:
        print("审核队列里没有本应用的记录（可能已审完或已撤回）")
        return
    for it in mine:
        info = (it.get("app") or {}).get("info") or {}
        print(
            f"review={it['id']}  status={it['status']}  version_id={it.get('version_id')}  "
            f"lang={info.get('language')}  截图={len(info.get('screenshot_pc_paths') or [])}"
        )


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--lpk", default=None, help="正式包路径（默认取 lazycat/ 下最新的非 dev 包）")
    ap.add_argument("--shots", default="docs/store", help="截图目录（默认 docs/store）")
    ap.add_argument("--dry-run", action="store_true", help="只组装并打印，不联网")
    ap.add_argument("--status", action="store_true", help="只查审核队列状态")
    args = ap.parse_args()

    if args.status:
        show_status()
        return 0

    shot_dir = os.path.join(REPO, args.shots) if not os.path.isabs(args.shots) else args.shots
    shots_local = sorted(glob.glob(os.path.join(shot_dir, "*.png")))
    if len(shots_local) < 2:
        raise SystemExit(f"{shot_dir} 下截图不足 2 张（support_pc 为真时必须 >= 2）")

    lpk = args.lpk or default_lpk()
    if not os.path.exists(lpk):
        raise SystemExit(f"LPK 不存在：{lpk}")

    if args.dry_run:
        print(f"LPK      : {lpk}  ({os.path.getsize(lpk)} 字节)")
        print(f"截图     : {len(shots_local)} 张 -> {shot_dir}")
        fake = {
            "package": PKG, "version": "(dry-run)", "iconPath": "(dry-run)",
            "url": "(dry-run)", "sha256": "(dry-run)",
            "unsupportedPlatforms": ["ios", "android"], "minOsVersion": "",
            "lpkSize": os.path.getsize(lpk), "imageSize": 0,
        }
        body = build_body(fake, [f"(dry-run)/{os.path.basename(p)}" for p in shots_local], DEFAULT_CHANGELOG)
        print(json.dumps(body, ensure_ascii=False, indent=2))
        return 0

    print(f"[1/3] 上传 {len(shots_local)} 张截图 ...")
    shots = [upload_image(p) for p in shots_local]
    for p, u in zip(shots_local, shots):
        print(f"      {os.path.basename(p):32s} -> {u}")

    print(f"[2/3] 上传 LPK {os.path.basename(lpk)} ...")
    up = upload_lpk(lpk)
    print(f"      version={up['version']} sha256={up['sha256'][:16]}... lpkSize={up['lpkSize']}")

    print("[3/3] 提交审核 ...")
    body = build_body(up, shots, DEFAULT_CHANGELOG)
    body_path = os.path.join(REPO, "lazycat", ".review-body.json")
    with open(body_path, "w") as f:
        json.dump(body, f, ensure_ascii=False, indent=2)
    print(f"      提审体：{body_path}")

    r = curl_json(
        f"{API}/app/{PKG}/review/create",
        ["-X", "POST", "-H", "Content-Type: application/json", "--data-binary", f"@{body_path}"],
        timeout=180,
    )
    if "id" not in r:
        print(f"提审失败：{json.dumps(r, ensure_ascii=False)}")
        return 1
    print(f"      已提交：review={r['id']} status={r['status']} version_id={r.get('version_id')}")
    print("\n撤回（审核期内）：DELETE 或 POST /developer/review/<id>/cancel")
    return 0


if __name__ == "__main__":
    sys.exit(main())
