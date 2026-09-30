#!/usr/bin/env python3
"""本机双实例端到端验收：用真 HTTP 走一遍同步链路。

# 为什么不能只靠 cargo 单测

单测直接调 `sync::apply`，验的是**内核逻辑**（载荷往返、冲突跳过、墓碑、拓扑序）。
而同步真正的风险集中在「桌面 → HTTP → 盒子」这一段：

- 令牌头名对不对、错令牌给不给 401；
- `{ok,data}` 信封的解析；
- 服务端 RPC 的命令**参数形状**（`{args:{...}}` 那一层嵌套）；
- `/sync/rpc` 与 `/rpc` 的分工（前者要鉴权，后者不能被动）。

这些只有**真起两个进程、真发 HTTP** 才能证伪。单测全绿而这一段接错，症状是
「桌面点同步，提示参数错误」—— 那时候再回来查成本高得多。

# 覆盖

1. 两个实例都真的起来了（`/healthz` 自证命令数）
2. 应用层令牌：对的放行、错的 / 缺的 401
3. **令牌各自独立**（A 的令牌在 B 上无效）—— 这条防的是「令牌被当成全局共享」
4. 分组 + 资产 + 凭据（明文过河 → 对端用自己密钥重新加密）完整搬运
5. 再同步一次是**更新**而不是**新增**
6. 墓碑传播（A 删了，B 跟着软删）
7. 版本闸门（伪造高版本号，整包拒收）

用法：
    python3 scripts/e2e-sync-local.py [--bin target/debug/nexterm-server]
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
TOKEN_HEADER = "X-NexTerm-Sync-Token"

PASSED: list[str] = []
FAILED: list[str] = []


def check(name: str, ok: bool, detail: str = "") -> bool:
    """记一条断言。**不在第一个失败处退出** —— 一次跑完看全貌比逐个修快。"""
    if ok:
        PASSED.append(name)
        print(f"  \033[32m✓\033[0m {name}")
    else:
        FAILED.append(name)
        print(f"  \033[31m✗\033[0m {name}" + (f"\n      {detail}" if detail else ""))
    return ok


def call(port: int, cmd: str, args=None, token: str | None = None, path: str = "/rpc"):
    """发一条 RPC。返回 (status, envelope)。"""
    body = json.dumps({"cmd": cmd, "args": args}).encode()
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}{path}",
        data=body,
        headers={"content-type": "application/json"},
    )
    if token is not None:
        req.add_header(TOKEN_HEADER, token)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        try:
            return exc.code, json.loads(raw)
        except Exception:
            return exc.code, {"raw": raw.decode(errors="replace")[:200]}


def data(port: int, cmd: str, args=None, token: str | None = None, path: str = "/rpc"):
    """要 `data`，顺带把信封里的错误抛成异常（便于写测试）。"""
    status, env = call(port, cmd, args, token, path)
    if not isinstance(env, dict) or env.get("ok") is not True:
        raise AssertionError(f"{cmd} 失败: HTTP {status} {env}")
    return env["data"]


def user_assets(port: int) -> list:
    """用户自建资产（**剔除内置的「当前设备」**）。

    `asset_list` 是给界面用的，里面一定有内置那一行；而同步的口径里它不存在
    （既不导出也不进摘要）。断言必须按同一个口径数，否则「同步过去了 1 条」
    会被读成「变成 2 条了」。
    """
    return [a for a in data(port, "asset_list") if not a.get("builtin")]


def wait_ready(port: int, proc: subprocess.Popen, timeout: float = 30.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            return False
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=1) as r:
                if r.status == 200:
                    return True
        except Exception:
            time.sleep(0.2)
    return False


def ensure_server_binary(binary: pathlib.Path) -> bool:
    """确保 `target/debug/nexterm-server` 是**服务端模式**构建。

    为什么要主动重建：`cargo test --workspace` / 默认的 `cargo build` 在 desktop feature 下
    也会把这个 bin 目标编出来，**覆盖掉**服务端模式的那份 —— 文件还在、但一启动就打印
    「当前是桌面模式构建」然后退出。只判 `is_file()` 会给出一个很难懂的现象。
    增量构建命中缓存时只要几秒。
    """
    print("构建服务端二进制（cargo build --no-default-features --features server）…")
    r = subprocess.run(
        [
            "cargo",
            "build",
            "--no-default-features",
            "--features",
            "server",
            "--bin",
            "nexterm-server",
        ],
        cwd=ROOT / "src-tauri",
    )
    if r.returncode != 0 or not binary.is_file():
        print(f"服务端二进制构建失败或不存在：{binary}")
        return False
    return True


# 只在 `#[cfg(feature = "desktop")]` 分支里编译进去的提示串；服务端模式不会有它。
# 判「这份是不是桌面模式」用它在文件里出现与否，不去运行二进制（服务端模式一跑就会常驻）。
_DESKTOP_MODE_MARKER = b"--no-default-features --features server --bin nexterm-server"

_BUILD_HINT = (
    "先跑：cargo build --no-default-features --features server --bin nexterm-server\n"
    "（`cargo test --workspace` 会用桌面模式覆盖同一路径，务必重编）"
)


def is_desktop_mode_binary(binary: pathlib.Path) -> bool:
    try:
        return _DESKTOP_MODE_MARKER in binary.read_bytes()
    except OSError:
        return False


def spawn(binary: str, data_dir: pathlib.Path, port: int, log: pathlib.Path):
    env = dict(os.environ)
    env.update(
        {
            "NEXTERM_DATA_DIR": str(data_dir),
            "NEXTERM_LISTEN": f"127.0.0.1:{port}",
            "NEXTERM_MASTER_KEY": "e2e-test-master-key-1",
            "RUST_LOG": "info",
        }
    )
    handle = log.open("wb")
    return subprocess.Popen([binary], env=env, stdout=handle, stderr=subprocess.STDOUT)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default=None, help="自己指定二进制（给了就不再自动构建）")
    ap.add_argument(
        "--no-build",
        action="store_true",
        help="不自动构建，直接用 --bin / 默认路径的那份（默认会先构建，见 ensure_server_binary）",
    )
    ap.add_argument("--keep", action="store_true", help="跑完不删数据目录（排查用）")
    opt = ap.parse_args()

    binary = pathlib.Path(str(ROOT / (opt.bin or "target/debug/nexterm-server")))
    explicit = opt.bin is not None

    # 没显式指定 --bin 时，自己保证这份二进制是服务端模式：不存在建，存在但被
    # `cargo test --workspace` 覆盖成桌面模式了也建。
    if not explicit and not opt.no_build:
        if (not binary.is_file()) or is_desktop_mode_binary(binary):
            if not ensure_server_binary(binary):
                return 2
    if not binary.is_file():
        print(f"找不到服务端二进制 {binary}\n{_BUILD_HINT}")
        return 2
    if is_desktop_mode_binary(binary):
        print(f"{binary} 是桌面模式构建，里面没有服务端代码。\n{_BUILD_HINT}")
        return 2

    work = pathlib.Path("/tmp/nx-sync-e2e")
    shutil.rmtree(work, ignore_errors=True)
    (work / "a").mkdir(parents=True)
    (work / "b").mkdir(parents=True)

    port_a, port_b = 18081, 18082
    print("启动两个服务端实例…")
    proc_a = spawn(binary, work / "a", port_a, work / "a.log")
    proc_b = spawn(binary, work / "b", port_b, work / "b.log")

    try:
        ok_a = wait_ready(port_a, proc_a)
        ok_b = wait_ready(port_b, proc_b)
        if not (ok_a and ok_b):
            print("实例没起来，日志尾部：")
            for tag in ("a", "b"):
                print(f"--- {tag}.log ---")
                print((work / f"{tag}.log").read_text(errors="replace")[-2000:])
            return 1

        print("\n[1] 两个实例都在服务")
        health_a = json.loads(urllib.request.urlopen(f"http://127.0.0.1:{port_a}/healthz").read())
        check(
            "healthz 自证命令已装载",
            health_a.get("commands", 0) > 130,
            f"commands={health_a.get('commands')}",
        )
        check(
            "同步命令已在表里",
            call(port_a, "sync_digest")[1].get("ok") is True,
            "sync_digest 调不通说明没注册进命令清单",
        )

        print("\n[2] 令牌闸门")
        token_a = data(port_a, "sync_token")
        token_b = data(port_b, "sync_token")
        check("服务端能取到自己的令牌", bool(token_a) and bool(token_b))
        check("两个实例的令牌不同", token_a != token_b, "令牌是每实例独立的")

        code, _ = call(port_b, "sync_digest", None, path="/sync/rpc")
        check("缺少令牌 → 401", code == 401, f"实际 {code}")

        code, _ = call(port_b, "sync_digest", None, token="not-a-real-token", path="/sync/rpc")
        check("错令牌 → 401", code == 401, f"实际 {code}")

        code, _ = call(port_b, "sync_digest", None, token=token_a, path="/sync/rpc")
        check("A 的令牌在 B 上无效 → 401", code == 401, f"实际 {code}")

        code, env = call(port_b, "sync_digest", None, token=token_b, path="/sync/rpc")
        check("B 的令牌 → 放行", code == 200 and env.get("ok") is True, f"HTTP {code} {env}")

        code, _ = call(port_a, "sync_digest", None, path="/rpc")
        check("/rpc 不受同步令牌影响（浏览器版链路未变）", code == 200, f"实际 {code}")

        print("\n[3] 造数据：分组 + 凭据 + 资产")
        group = data(
            port_a,
            "group_create",
            {"parentId": None, "name": "E2E 生产", "sort": 0},
        )
        cred = data(
            port_a,
            "vault_set_credential",
            # 注意这一层 `args` 是命令签名里的 `args: SetCredentialArgs`，
            # 与服务端 RPC 的 `{cmd, args}` 外层是两个东西 —— 少了它，
            # 服务端会报 `参数 args: invalid type: null`。
            {"args": {"name": "e2e-root-pass", "kind": "password", "secret": "s3cret-中文-e2e"}},
        )
        asset = data(
            port_a,
            "asset_create",
            {
                "args": {
                    "groupId": group["id"],
                    "kind": "ssh",
                    "name": "e2e-web-1",
                    "host": "10.9.8.7",
                    "port": 2222,
                    "username": "root",
                    "authKind": "password",
                    "credId": cred["id"],
                    "options": {"keepalive": 30},
                    "tags": "e2e",
                    "note": "端到端验证用",
                }
            },
        )
        check("资产建好了", bool(asset.get("id")))

        print("\n[4] 推送：A 导出 → B 导入（走真 HTTP）")
        bundle = data(
            port_a,
            "sync_export",
            {"args": {"assetIds": [asset["id"]], "withCreds": True}},
        )
        check("导出包里有 1 条资产", len(bundle.get("assets", [])) == 1)
        check("导出包里有祖先分组", len(bundle.get("groups", [])) == 1)
        check("导出包里有凭据（明文过河）", len(bundle.get("creds", [])) == 1)
        check("包里没有 builtin 字段", "builtin" not in bundle["assets"][0])

        report = data(
            port_b,
            "sync_import",
            {"args": {"bundle": bundle, "force": False}},
            token=token_b,
            path="/sync/rpc",
        )
        check("B 新建 1 条资产", report.get("assetsCreated") == 1, str(report))
        check("B 新建 1 条凭据", report.get("credsCreated") == 1, str(report))
        check("B 新建 1 个分组", report.get("groupsCreated") == 1, str(report))
        check("导入无警告", not report.get("warnings"), str(report.get("warnings")))

        got = data(port_b, "asset_get", {"id": asset["id"]})
        check("B 上资产字段一致", got["host"] == "10.9.8.7" and got["port"] == 2222, str(got))
        check("B 上分组引用被保留", got["groupId"] == group["id"])
        check("B 上凭据引用被保留", got["credId"] == cred["id"])
        check("B 上 options 是对象而非字符串", got["options"] == {"keepalive": 30}, str(got["options"]))

        # 关键：B 用**自己的**密钥解出来了 —— 说明是「重加密」而不是「密文照搬」
        revealed = data(port_b, "vault_reveal_credential", {"id": cred["id"]})
        check(
            "B 能用自己密钥解出原口令",
            revealed.get("value") == "s3cret-中文-e2e",
            f"实际 {revealed.get('value')!r}",
        )

        print("\n[5] 幂等：再推一次应是「更新」而非「新增」")
        bundle2 = data(
            port_a,
            "sync_export",
            {"args": {"assetIds": [asset["id"]], "withCreds": True}},
        )
        report2 = data(
            port_b,
            "sync_import",
            {"args": {"bundle": bundle2, "force": False}},
            token=token_b,
            path="/sync/rpc",
        )
        assets_b = user_assets(port_b)
        check("第二次是更新", report2.get("assetsUpdated") == 1, str(report2))
        check("第二次没有新增", report2.get("assetsCreated") == 0, str(report2))
        check("B 上仍只有 1 条用户资产", len(assets_b) == 1, f"实际 {len(assets_b)} 条")
        check(
            "内置「当前设备」没被同步过来（B 上仍只有它自己那一条）",
            len([a for a in data(port_b, "asset_list") if a.get("builtin")]) == 1,
        )

        print("\n[6] 反向：从 B 拉回 A（同一个包、反方向）")
        pulled = data(
            port_a,
            "sync_import",
            {"args": {"bundle": bundle2, "force": False}},
        )
        check("A 侧导入自己的包不会新建", pulled.get("assetsCreated") == 0, str(pulled))
        check("A 侧也不会重复新增", len(user_assets(port_a)) == 1, "同 ID 应就地更新")

        print("\n[7] 版本闸门")
        forged = dict(bundle)
        forged["protocol"] = 999
        code, env = call(
            port_b,
            "sync_import",
            {"args": {"bundle": forged, "force": True}},
            token=token_b,
            path="/sync/rpc",
        )
        err = (env or {}).get("error", {})
        check("高版本包被拒", code == 200 and err.get("code") == "unsupported", f"HTTP {code} {env}")

        print("\n[8] 墓碑传播")
        data(port_a, "asset_delete", {"id": asset["id"]})
        tomb = data(
            port_a,
            "sync_export",
            {"args": {"assetIds": [asset["id"]], "withCreds": False}},
        )
        check("墓碑在包里", tomb["assets"][0].get("deletedAt") is not None)
        data(
            port_b,
            "sync_import",
            {"args": {"bundle": tomb, "force": True}},
            token=token_b,
            path="/sync/rpc",
        )
        visible_b = user_assets(port_b)
        check("B 上的这条随墓碑消失", len(visible_b) == 0, f"实际还剩 {len(visible_b)} 条")
        check(
            "墓碑没有波及内置资产",
            len([a for a in data(port_b, "asset_list") if a.get("builtin")]) == 1,
        )

        print("\n[9] 凭据库根密钥确实来自注入（服务端是解锁态）")
        vault = data(port_b, "vault_status")
        check("B 凭据库已解锁", vault.get("unlocked") is True and vault.get("mode") == "master", str(vault))

    finally:
        for proc in (proc_a, proc_b):
            proc.terminate()
        for proc in (proc_a, proc_b):
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
        if not opt.keep:
            pass  # 保留 /tmp/nx-sync-e2e 便于排查；目录很小

    total = len(PASSED) + len(FAILED)
    print(f"\n{'=' * 56}")
    if FAILED:
        print(f"\033[31m{len(FAILED)}/{total} 条断言失败\033[0m")
        for f in FAILED:
            print(f"  - {f}")
        print(f"\n数据目录与日志留在 {work}")
        return 1
    print(f"\033[32m全部 {total} 条断言通过\033[0m")
    return 0


if __name__ == "__main__":
    sys.exit(main())
