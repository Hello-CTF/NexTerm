#!/usr/bin/env python3
"""校验 lazycat/lzc-manifest.yml 的 `application.injects`（文件选择器接入）仍与随包脚本一致。

为什么需要它 —— 这条链路上有三处**只能靠约定对齐、出错时完全静默**的地方：

1. `subdomain` 用的是官方 `#@build if profile=dev / else / end` **打包期文本裁剪指令**，
   dev 与 release 的包 ID 不同，同一个文件必须两套都对。
2. browser inject 里的 `file:///lzcapp/pkg/content/lazycat-injects/...` 必须与
   `lazycat/image/build-server.sh` 实际拷贝的落点一致。对不上 ⇒ 注入静默失效，
   页面里不会报任何错，只是"网盘文件"那个页签不出现。
3. request inject 的 `bridgePrefix` 必须与 browser inject 的 `params.fileBridgeRoot` 一致。
   对不上 ⇒ 选择**他人共享目录**时请求打偏（官方文档「常见错误」第一条）。

依赖：PyYAML（`pip install pyyaml`）。用法：

    python3 scripts/verify-manifest-injects.py
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

if not __debug__:
    sys.exit("禁止使用 python -O/PYTHONOPTIMIZE：优化模式会删除门禁断言")

ROOT = Path(__file__).resolve().parent.parent
MANIFEST = ROOT / "lazycat" / "lzc-manifest.yml"
BUILD_SH = ROOT / "lazycat" / "image" / "build-server.sh"

BRIDGE_ID = "lazycat-file-bridge"
CHOOSER_ID = "open-save-chooser"
CONTENT_PREFIX = "file:///lzcapp/pkg/content/"

DIRECTIVE = re.compile(r"^\s*#@build\s+(.*?)\s*$")


def load_yaml():
    """导入 PyYAML，并摘掉 YAML 1.1 的 bool 隐式解析器。

    否则裸 `on: request` 会被解析成 `{"True": "request"}`（Norway problem），
    而 `on` 在 manifest 规范里是**字符串键**。
    """
    try:
        import yaml
    except ImportError:
        sys.exit("需要 PyYAML：pip install pyyaml")

    class Loader(yaml.SafeLoader):
        pass

    for ch, resolvers in list(Loader.yaml_implicit_resolvers.items()):
        Loader.yaml_implicit_resolvers[ch] = [
            (tag, rx) for tag, rx in resolvers if tag != "tag:yaml.org,2002:bool"
        ]
    Loader.add_implicit_resolver(
        "tag:yaml.org,2002:bool",
        re.compile(r"^(?:true|True|TRUE|false|False|FALSE)$"),
        list("tTfF"),
    )
    return yaml, Loader


def trim(text: str, profile: str) -> str:
    """按 `#@build if/else/end` 指令裁剪文本，返回**生效后**的 manifest。

    不能直接拿未裁剪的文件去 parse：`subdomain` 那两行是同一个键的重复出现，
    未裁剪时必然报 `duplicated mapping key` —— 那是校验方法不对，不是数据错误。
    """
    out: list[str] = []
    stack: list[tuple[str, bool]] = []
    for lineno, line in enumerate(text.splitlines(), 1):
        m = DIRECTIVE.match(line)
        if m:
            body = m.group(1)
            if body.startswith("if "):
                cond = body[3:].strip()
                mm = re.fullmatch(r"profile\s*=\s*(\S+)", cond)
                if not mm:
                    sys.exit(f"{lineno}: 不认识的 #@build 条件: {cond!r}")
                stack.append(("if", mm.group(1) == profile))
            elif body == "else":
                if not stack:
                    sys.exit(f"{lineno}: 孤立的 #@build else")
                stack[-1] = ("else", not stack[-1][1])
            elif body == "end":
                if not stack:
                    sys.exit(f"{lineno}: 孤立的 #@build end")
                stack.pop()
            else:
                sys.exit(f"{lineno}: 不认识的 #@build 指令: {body!r}")
            continue
        if all(taken for _, taken in stack):
            out.append(line)
    if stack:
        sys.exit(f"#@build 未闭合，残余 {len(stack)} 层")
    return "\n".join(out) + "\n"


def check_js(src: str, label: str) -> None:
    """用 node 的 `new Function` 过一遍语法（只编译，不执行）。

    之所以是 `new Function('ctx', src)`：request 阶段脚本就是这样被包进函数体的，
    顶层 `return` 因此合法 —— 官方示例里也真的用了顶层 `return`。
    """
    code = (
        "const code = " + json.dumps(src) + ";\n"
        "try { new Function('ctx', code); }"
        " catch (e) { console.error(e.message); process.exit(3); }\n"
        f"console.log('{label}: {len(src)} 字符，语法 OK');\n"
    )
    r = subprocess.run(["node", "-e", code], capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit(f"{label} 语法检查失败: {r.stderr or r.stdout}")
    print("    " + r.stdout.strip())


def main() -> int:
    if not MANIFEST.is_file():
        sys.exit(f"找不到 {MANIFEST}")
    raw = MANIFEST.read_text(encoding="utf-8")
    yaml, Loader = load_yaml()

    chooser_uri = None
    for profile, want_subdomain in (("dev", "nexterm-dev"), ("release", "nexterm")):
        print(f"== profile={profile} ==")
        app = yaml.load(trim(raw, profile), Loader=Loader)["application"]

        assert app["subdomain"] == want_subdomain, (
            f'subdomain 期望 {want_subdomain}，实际 {app["subdomain"]}（#@build 裁剪错了？）'
        )
        print(f"    subdomain = {app['subdomain']}")

        routes = app.get("routes") or []
        assert routes == ["/=http://nexterm-server:8080/"], (
            f"routes 应当只有一条短服务名路由（全限定名会把包 ID 焊死）：{routes}"
        )
        print("    routes = 1 条短服务名路由")

        injects = app.get("injects")
        assert isinstance(injects, list) and len(injects) == 2, (
            f"injects 应当正好 2 条（request 桥接 + browser 选择器）：{injects!r}"
        )
        by_id = {i.get("id"): i for i in injects}
        assert set(by_id) == {BRIDGE_ID, CHOOSER_ID}, f"inject id 不对：{sorted(by_id)}"

        bridge = by_id[BRIDGE_ID]
        assert bridge.get("on") == "request", f"{BRIDGE_ID}.on 应为 request"
        assert bridge.get("when") == ["/__lazycat_file_bridge/*"], bridge.get("when")
        bridge_src = (bridge.get("do") or [{}])[0].get("src")
        assert isinstance(bridge_src, str) and "ctx.proxy.to" in bridge_src, "桥接脚本不完整"
        m = re.search(r'bridgePrefix\s*=\s*"([^"]+)"', bridge_src)
        assert m, "桥接脚本里找不到 bridgePrefix"
        bridge_prefix = m.group(1)
        print(f"    {BRIDGE_ID}: on=request, bridgePrefix={bridge_prefix}")
        check_js(bridge_src, BRIDGE_ID)

        chooser = by_id[CHOOSER_ID]
        assert chooser.get("on") == "browser", f"{CHOOSER_ID}.on 应为 browser"
        assert chooser.get("when") == ["/*"], chooser.get("when")
        scripts = chooser.get("do") or []
        assert len(scripts) == 1, f"{CHOOSER_ID}.do 应当只有一条脚本：{scripts!r}"
        uri = scripts[0].get("src")
        assert isinstance(uri, str) and uri.startswith(CONTENT_PREFIX), (
            f"{CHOOSER_ID} 的脚本必须随包提供（{CONTENT_PREFIX}...），实际 {uri!r}"
        )
        params = scripts[0].get("params") or {}
        assert params.get("fileBridgeRoot") == bridge_prefix, (
            f"params.fileBridgeRoot({params.get('fileBridgeRoot')!r}) 必须与 "
            f"bridgePrefix({bridge_prefix!r}) 一致"
        )
        print(f"    {CHOOSER_ID}: on=browser, fileBridgeRoot 与 bridgePrefix 一致")
        chooser_uri = uri

    print("== 包内路径一致性 ==")
    rel = chooser_uri[len(CONTENT_PREFIX):]
    repo_rel = rel.replace("lazycat-injects/", "injects/", 1)
    sh = BUILD_SH.read_text(encoding="utf-8")
    assert re.search(
        r'cp\s+"\$HERE"/\.\./injects/\*\.js\s+"\$CONTENT/lazycat-injects/"', sh
    ), "build-server.sh 里没有把 injects/*.js 拷进 $CONTENT/lazycat-injects/"
    print(f"    manifest 引用   {rel}")
    print("    build-server.sh 拷到 $CONTENT/lazycat-injects/")
    src = ROOT / "lazycat" / repo_rel
    assert src.is_file(), f"仓库里没有随包脚本：{src}"
    print(f"    仓库源文件存在（{src.stat().st_size} 字节）")

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifact-report", type=Path, help="M29 artifact JSON to validate for LazyCat/Pages consumption")
    parser.add_argument("--artifact-id", help="required artifact id (prevents validating the wrong target)")
    parser.add_argument("--require-release", action="store_true", help="require passed file/cgo/static/Rust/custom-Go gates")
    args = parser.parse_args()
    if args.require_release and not args.artifact_report:
        parser.error("--require-release needs --artifact-report")
    if args.artifact_report:
        report_path = args.artifact_report.resolve()
        report = json.loads(report_path.read_text(encoding="utf-8"))
        if args.artifact_id:
            assert report.get("id") == args.artifact_id, (
                f"artifact id {report.get('id')!r} != required {args.artifact_id!r}"
            )
        artifact = report.get("artifact") or {}
        artifact_path = (ROOT / artifact.get("path", "")).resolve()
        assert artifact_path.is_file(), f"报告中的产物不存在：{artifact_path}"
        payload = artifact_path.read_bytes()
        assert artifact.get("size_bytes") == len(payload), "产物字节数与报告不一致"
        assert artifact.get("sha256") == hashlib.sha256(payload).hexdigest(), "产物 SHA256 与报告不一致"
        kind = report.get("kind")
        platform = report.get("platform") or {}
        arch = platform.get("arch")
        assert kind != "sync-archive", "onlyServer 独立包已停止发布；--sync-only 仅保留为 full 包运行时"
        expected_name = None
        member_assertion = None
        if kind == "server-archive":
            expected_name = f"NexTerm-server_{report.get('version')}_linux_{arch}.tar.gz"
            member_assertion = "full-server-archive-members"
        elif kind == "desktop-linux-archive":
            expected_name = f"NexTerm-desktop_{report.get('version')}_linux_{arch}.tar.gz"
            member_assertion = "linux-desktop-archive-members"
        elif kind == "desktop-nsis":
            setup_arch = "x64" if arch == "amd64" else "arm64"
            expected_name = f"NexTerm_{report.get('version')}_{setup_arch}-setup.exe"
        elif kind == "desktop-dmg":
            dmg_arch = "aarch64" if arch == "arm64" else "x86_64"
            expected_name = f"NexTerm_{report.get('version')}_{dmg_arch}.dmg"
        if expected_name is not None:
            assert artifact_path.name == expected_name, f"文件名 {artifact_path.name} 不符合合同 {expected_name}"
        if member_assertion is not None:
            assert any(
                item.get("id") == member_assertion and item.get("status") == "passed"
                for item in report.get("assertions") or []
            ), f"归档成员门禁 {member_assertion} 未通过"
        assert report.get("real_target_acceptance_claim") is False, "交付报告不得自称完成真机验收"
        if args.require_release:
            assert report.get("status") == "passed", f"产物门禁未全通过：{report.get('status')}"
            assert report.get("assertions") and all(item.get("status") == "passed" for item in report["assertions"]), (
                "文件/架构/cgo/static 断言存在未通过项"
            )
            comparisons = report.get("comparisons") or {}
            assert comparisons.get("rust", {}).get("status") == "passed", "Rust 严格体积门禁未通过或缺基线"
            assert comparisons.get("custom_go", {}).get("status") == "measured", "custom-Go/Eino 体积证据缺失"
            if report.get("kind") == "server" and report.get("platform", {}).get("os") == "linux":
                assert report.get("platform", {}).get("cgo") == "disabled", "Linux 服务端必须禁用 cgo"
                assert report.get("format", {}).get("static") is True, "Linux 服务端必须是静态 ELF"
        print(f"    产物契约 {report.get('id')}: {report.get('status')}（哈希与字节数一致）")

    print("\n全部断言通过；外部真机验收仍以 evidence gap 单独记录。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
