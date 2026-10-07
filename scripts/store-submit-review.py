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
        "brief": "浏览器里的 SSH / SFTP / Docker / 数据库一体化运维终端。会话与布局都跑在微服上，关掉网页不断、换设备接着用。",
        "description": """\
NexTerm 把日常运维要开的一堆工具收进一个浏览器窗口：SSH 终端、SFTP 文件管理、Docker 容器、数据库客户端、磁盘挂载，再加上一个能读懂终端输出的 AI 助手。

跑在你的微服上
终端进程、会话与工作区布局都留在微服，浏览器只是接上去看。关掉网页不会结束会话，换一台设备打开即可接管原来那条终端、接着看原来的输出；同一时刻只有一台设备能操作，其余设备观看，点一下即可接管。工作区、分屏与标签结构也保存在微服上，任一端的改动会同步到其他设备。

主要能力
· 终端：多标签与分屏、会话录制、日志导出、UTF-8 / GBK / GB18030 / Big5 编码切换
· 文件：SFTP 浏览与内置编辑器，传输进度与断点续传、MD5 / SHA256 校验
· Docker：容器与镜像列表、启停删除、日志跟随、容器终端、容器文件浏览
· 数据库：MySQL / MariaDB 库表浏览与 SQL 工作台；Redis 键浏览与命令台
· 磁盘挂载：通过 sshfs 把远端目录挂进工作区（需授予 FUSE 权限）
· AI 助手：命令按风险分级、危险操作先确认、改文件留前后对照

关于端口转发
端口转发在此平台上不可用。平台的端口暴露是裸 TCP 且不带鉴权，应用侧补不上这个洞；需要把远端端口开放给外部时，请改用微服平台自带的转发功能。

凭据安全
所有主机凭据由微服注入的根密钥加密后保存在你自己的实例里，不上传任何第三方。浏览器版为了让「打开浏览器就能连服务器」成立，进程内存里会持有解密后的密钥，这台微服是您自己的硬件。需要更强隔离时，请不要在这台微服上保存生产环境的口令，改用密钥认证。

数据持久化
配置与凭据保存在 /lzcapp/var，随应用升级保留。""",
        "keywords": "SSH,SFTP,Docker,数据库,终端,运维,浏览器,多端会话,AI助手",
    },
    {
        "language": "en",
        "name": "NexTerm",
        "brief": "An all-in-one SSH / SFTP / Docker / database ops terminal in your browser. Sessions and layout live on your microserver, so closing the page keeps them alive and another device picks up where you left off.",
        "description": """\
NexTerm puts the tools you open every day into a single browser window: an SSH terminal, SFTP file manager, Docker containers, a database client and disk mounting, plus an AI assistant that can read your terminal output.

Runs on your microserver
Terminal processes, sessions and workspace layout all live on the microserver; the browser only attaches to them. Closing the page does not end a session, and opening it from another device takes over the same terminal and replays its scrollback. Only one device can type at a time while the others watch; click to take over. Workspaces, split panes and tabs are stored on the microserver too, and a change on any device propagates to the rest.

Capabilities
- Terminal: tabs and split panes, session recording, log export, UTF-8 / GBK / GB18030 / Big5 encoding switching
- Files: SFTP browsing with a built-in editor, transfer progress and resume, MD5 / SHA256 checksums
- Docker: container and image lists, start/stop/remove, log following, container shell, container file browsing
- Databases: MySQL / MariaDB table browsing and a SQL workbench; Redis key browser and console
- Disk mounting: mount remote directories via sshfs (requires the FUSE permission)
- AI assistant: commands graded by risk, dangerous operations confirmed first, file edits shown as before/after diffs

About port forwarding
Port forwarding is not available on this platform. The platform exposes ports as raw TCP with no authentication, which the app cannot compensate for. To expose a remote port, use the native forwarding feature of the microserver platform instead.

Credential safety
All host credentials are encrypted with a root key injected by your microserver and stored on your own instance, and nothing is sent to a third party. In the browser build the decrypted key is held in process memory so that "open a browser and connect" works; the microserver is your own hardware. If you need stronger isolation, do not store production passwords here, use key authentication.

Data persistence
Configuration and credentials live in /lzcapp/var and survive app upgrades.""",
        "keywords": "SSH,SFTP,Docker,database,terminal,ops,browser,multi-device,AI assistant",
    },
]

DEFAULT_CHANGELOG = {
    # 商店上一版是 0.1.3（review.id 19682，2026-09-30），所以这次是 0.2.0 的更新说明，
    # **不是**「首个版本」——写错会让审核看到一份与版本号不符的 changelog。
    "zh": "v0.2.0：新增服务端形态——把 NexTerm 部署到一台常开的机器，浏览器打开就是完整工作台。终端进程、会话与工作区布局都留在服务端，关掉网页不会中断，换台设备接着用；同一时刻只有一台设备能操作，其余设备观看。同时新增桌面与服务端之间的资产同步（含凭据）。懒猫版不提供端口转发，请改用微服平台自带的转发功能。",
    "en": "v0.2.0: adds the server form — deploy NexTerm on an always-on machine and open it in a browser as a complete workspace. Terminal processes, sessions and workspace layout stay on the server, so closing the page does not interrupt them and another device picks up where you left off; only one device types at a time while the others watch. Also adds asset sync between desktop and server, credentials included. The LazyCat build does not provide port forwarding — use the platform's own forwarding instead.",
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
            # dry-run 的假响应，取值要与 lazycat/package.yml 保持一致，否则会误导出
            # 「和真实提审不一样」的结论。2026-10-03 起 package.yml **不再声明**
            # unsupported_platforms（全平台放开），所以这里也是空。
            # 真实路径下这个字段取自 `/app/lpk/upload` 的响应（平台从 LPK 里读）。
            "unsupportedPlatforms": [], "minOsVersion": "",
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
