#!/usr/bin/env python3
"""校验 lazycat/lzc-manifest.yml 的 `application.injects`（网关头 + 文件选择器接入）与
主密钥注入链仍与随包脚本一致。

为什么需要它 —— 这条链路上有四处**只能靠约定对齐、出错时完全静默**的地方：

1. `subdomain` 用的是官方 `#@build if profile=dev / else / end` **打包期文本裁剪指令**，
   dev 与 release 的包 ID 不同，同一个文件必须两套都对。
2. browser inject 里的 `file:///lzcapp/pkg/content/lazycat-injects/...` 必须与
   `lazycat/image/build-server.sh` 实际拷贝的落点一致。对不上 ⇒ 注入静默失效，
   页面里不会报任何错，只是"网盘文件"那个页签不出现。
3. request inject 的 `bridgePrefix` 必须与 browser inject 的 `params.fileBridgeRoot` 一致。
   对不上 ⇒ 选择**他人共享目录**时请求打偏（官方文档「常见错误」第一条）。
4. 平台只能把 `stable_secret` 注入环境变量，主密钥必须经镜像 entrypoint 落成 0600
   文件再以 `NEXTERM_MASTER_KEY_FILE` 交给服务端（`NEXTERM_MASTER_KEY` 已弃用）。
   manifest 直注已弃用变量、entrypoint 漏 unset 或漏 0600，都会让密钥以弃用形态
   留在进程环境里，服务端只打一条弃用告警，不报错。

依赖：PyYAML（`pip install pyyaml`）。用法：

    python3 scripts/verify-manifest-injects.py
"""

from __future__ import annotations

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
DOCKERFILE = ROOT / "lazycat" / "image" / "Dockerfile"
ENTRYPOINT_SH = ROOT / "lazycat" / "image" / "entrypoint.sh"

BRIDGE_ID = "lazycat-file-bridge"
CHOOSER_ID = "open-save-chooser"
GATEWAY_ID = "gateway-auth"
CONTENT_PREFIX = "file:///lzcapp/pkg/content/"

MASTER_KEY_TRANSPORT = "NEXTERM_LAZYCAT_VAULT_MASTER_KEY"
MASTER_KEY_TRANSPORT_ENV = f'{MASTER_KEY_TRANSPORT}={{{{ stable_secret "vault_master" }}}}'

# 匿名可达的最小集合: 设备 agent 合同 (enroll/sync/current-url/WS) 与公开
# 分享链接数据面 (/share/public/{token}, token 即凭据)。
# 设备身份由设备凭证把关, 分享访问由 token 哈希把关, 均不经平台 gateway auth;
# 任何 widening 都会让匿名请求也拿到网关头, 任何收窄都会让设备 agent 或公开
# 分享在平台上够不到服务器。/sync/v2/* 与分享管理 /share/links、
# /share/host-shares 走账号会话 (push 还要 CSRF), 不得加入。
EXPECTED_PUBLIC_PATH = ["/device/enroll", "/agent/sync", "/agent/current-url", "/ws/device", "/share/public"]

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


def assert_profile(document, want_subdomain):
    app = document["application"]

    assert app["subdomain"] == want_subdomain, (
        f'subdomain 期望 {want_subdomain}，实际 {app["subdomain"]}（#@build 裁剪错了？）'
    )
    print(f"    subdomain = {app['subdomain']}")

    routes = app.get("routes") or []
    assert routes == ["/=http://nexterm-server:8080/"], (
        f"routes 应当只有一条短服务名路由（全限定名会把包 ID 焊死）：{routes}"
    )
    print("    routes = 1 条短服务名路由")

    public_path = app.get("public_path")
    assert public_path == EXPECTED_PUBLIC_PATH, (
        f"public_path 应当恰好是设备 agent 合同 {len(EXPECTED_PUBLIC_PATH)} 条（被改宽会让匿名请求也拿到网关头，"
        f"被收窄会让设备 agent 够不到服务器）：{public_path!r}"
    )
    print(f"    public_path = {EXPECTED_PUBLIC_PATH}")

    injects = app.get("injects")
    assert isinstance(injects, list) and len(injects) == 3, (
        f"injects 应当正好 3 条（网关头 + request 桥接 + browser 选择器）：{injects!r}"
    )
    by_id = {i.get("id"): i for i in injects}
    assert set(by_id) == {BRIDGE_ID, CHOOSER_ID, GATEWAY_ID}, f"inject id 不对：{sorted(by_id)}"

    gateway = by_id[GATEWAY_ID]
    assert gateway.get("on") == "request", f"{GATEWAY_ID}.on 应为 request"
    assert gateway.get("auth_required", True) is True, (
        f"{GATEWAY_ID}.auth_required 必须为 true（false 会给匿名请求也注入网关密钥）"
    )
    assert gateway.get("when") == ["/*"], gateway.get("when")
    gateway_scripts = gateway.get("do") or []
    assert len(gateway_scripts) == 1, f"{GATEWAY_ID}.do 应当只有一条脚本：{gateway_scripts!r}"
    gateway_src = gateway_scripts[0].get("src")
    assert isinstance(gateway_src, str) and "ctx.headers.set" in gateway_src and "ctx.params.key" in gateway_src, (
        "网关头脚本必须通过 ctx.params.key 设置 X-NexTerm-Gateway-Auth"
    )
    gateway_key = (gateway_scripts[0].get("params") or {}).get("key")
    seed_match = re.fullmatch(r"\{\{\s*stable_secret\s+\"([^\"]+)\"\s*\}\}", str(gateway_key))
    assert seed_match, f"{GATEWAY_ID}.params.key 必须是 stable_secret 模板：{gateway_key!r}"
    environment = (document.get("services") or {}).get("nexterm-server", {}).get("environment") or []
    expected_env = f'NEXTERM_GATEWAY_AUTH={{{{ stable_secret "{seed_match.group(1)}" }}}}'
    assert expected_env in environment, (
        f"services 环境变量必须使用同一 stable_secret seed（缺 {expected_env!r}）：{environment!r}"
    )
    print(f"    {GATEWAY_ID}: on=request, auth_required=true, params.key 与 NEXTERM_GATEWAY_AUTH 同 seed")
    check_js(gateway_src, GATEWAY_ID)

    assert MASTER_KEY_TRANSPORT_ENV in environment, (
        f"services 环境变量必须保留 {MASTER_KEY_TRANSPORT_ENV!r} 作为平台注入通道"
        f"（镜像 entrypoint 把它落成 0600 密钥文件）：{environment!r}"
    )
    assert not any(entry.startswith("NEXTERM_MASTER_KEY=") for entry in environment), (
        f"不得把已弃用的 NEXTERM_MASTER_KEY 直注服务环境（entrypoint 落 0600 文件后"
        f"经 NEXTERM_MASTER_KEY_FILE 交给服务端）：{environment!r}"
    )
    print(f"    master key: {MASTER_KEY_TRANSPORT} 平台注入, 已弃用的 NEXTERM_MASTER_KEY 不再直注")

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
    return uri


def assert_entrypoint_wrapper(wrapper: str, dockerfile: str) -> None:
    """镜像 entrypoint 必须把平台注入通道落成 0600 密钥文件再启动服务端。"""
    assert 'ENTRYPOINT ["/usr/local/bin/nexterm-server-entrypoint"]' in dockerfile, (
        "Dockerfile ENTRYPOINT 必须指向 entrypoint 包装脚本"
    )
    assert f"unset {MASTER_KEY_TRANSPORT}" in wrapper, (
        f"entrypoint 必须在 exec 前 unset {MASTER_KEY_TRANSPORT}，否则密钥留在进程环境里"
    )
    assert 'NEXTERM_MASTER_KEY_FILE="$data_dir/master.key"' in wrapper and "export NEXTERM_MASTER_KEY_FILE" in wrapper, (
        "entrypoint 必须把密钥文件路径经 NEXTERM_MASTER_KEY_FILE 传给服务端"
    )
    assert "chmod 0600" in wrapper, "entrypoint 必须把密钥文件权限设为 0600"
    assert "umask 077" in wrapper, "entrypoint 必须在写密钥文件前 umask 077"
    assert "exec /usr/local/bin/nexterm-server" in wrapper, "entrypoint 必须 exec 真正的服务端"
    print("    entrypoint: 通道变量落 0600 密钥文件, NEXTERM_MASTER_KEY_FILE 交接, unset 后 exec 服务端")


def selftest(yaml, Loader, raw) -> None:
    import copy

    base = yaml.load(trim(raw, "release"), Loader=Loader)
    gateway_index = next(
        i for i, inject in enumerate(base["application"]["injects"]) if inject.get("id") == GATEWAY_ID
    )

    def drift_public_path(doc):
        doc["application"]["public_path"] = EXPECTED_PUBLIC_PATH + ["/rpc"]

    def drop_public_path(doc):
        doc["application"]["public_path"] = []

    def anonymous_gateway(doc):
        doc["application"]["injects"][gateway_index]["auth_required"] = False

    def static_gateway_key(doc):
        doc["application"]["injects"][gateway_index]["do"][0]["params"]["key"] = "hardcoded-secret"

    def mismatched_seed(doc):
        environment = doc["services"]["nexterm-server"]["environment"]
        doc["services"]["nexterm-server"]["environment"] = [
            entry.replace('stable_secret "gateway_auth"', 'stable_secret "other_seed"')
            if entry.startswith("NEXTERM_GATEWAY_AUTH=") else entry
            for entry in environment
        ]

    def deprecated_master_key_env(doc):
        doc["services"]["nexterm-server"]["environment"].append("NEXTERM_MASTER_KEY=hardcoded-secret")

    def drop_master_key_transport(doc):
        doc["services"]["nexterm-server"]["environment"] = [
            entry for entry in doc["services"]["nexterm-server"]["environment"]
            if not entry.startswith(f"{MASTER_KEY_TRANSPORT}=")
        ]

    negatives = [
        ("public_path 改宽", drift_public_path),
        ("public_path 删除", drop_public_path),
        ("auth_required: false", anonymous_gateway),
        ("网关密钥写死", static_gateway_key),
        ("env seed 不一致", mismatched_seed),
        ("已弃用 NEXTERM_MASTER_KEY 直注", deprecated_master_key_env),
        ("主密钥注入通道被删", drop_master_key_transport),
    ]
    assert_profile(copy.deepcopy(base), "nexterm")
    print("    正例通过")
    for name, mutate in negatives:
        mutated = copy.deepcopy(base)
        mutate(mutated)
        try:
            assert_profile(mutated, "nexterm")
        except AssertionError:
            print(f"    负例通过（{name} 被拒绝）")
            continue
        sys.exit(f"负例未被发现：{name} 应当断言失败")

    wrapper = ENTRYPOINT_SH.read_text(encoding="utf-8")
    dockerfile = DOCKERFILE.read_text(encoding="utf-8")
    wrapper_negatives = [
        ("entrypoint 漏 unset 通道变量", wrapper.replace(f"unset {MASTER_KEY_TRANSPORT}", "true"), dockerfile),
        ("entrypoint 漏 0600", wrapper.replace("chmod 0600", "chmod 0644"), dockerfile),
        ("entrypoint 漏 umask", wrapper.replace("umask 077", "umask 022"), dockerfile),
        ("entrypoint 漏 NEXTERM_MASTER_KEY_FILE", wrapper.replace('NEXTERM_MASTER_KEY_FILE="$data_dir/master.key"', "true"), dockerfile),
        ("Dockerfile 绕开 entrypoint", wrapper, dockerfile.replace('ENTRYPOINT ["/usr/local/bin/nexterm-server-entrypoint"]', 'ENTRYPOINT ["/usr/local/bin/nexterm-server"]')),
    ]
    assert_entrypoint_wrapper(wrapper, dockerfile)
    for name, mutated_wrapper, mutated_dockerfile in wrapper_negatives:
        try:
            assert_entrypoint_wrapper(mutated_wrapper, mutated_dockerfile)
        except AssertionError:
            print(f"    负例通过（{name} 被拒绝）")
            continue
        sys.exit(f"负例未被发现：{name} 应当断言失败")


def main() -> int:
    if not MANIFEST.is_file():
        sys.exit(f"找不到 {MANIFEST}")
    raw = MANIFEST.read_text(encoding="utf-8")
    yaml, Loader = load_yaml()

    print("== 自测（负例必须被拒绝）==")
    selftest(yaml, Loader, raw)

    chooser_uri = None
    for profile, want_subdomain in (("dev", "nexterm-dev"), ("release", "nexterm")):
        print(f"== profile={profile} ==")
        chooser_uri = assert_profile(yaml.load(trim(raw, profile), Loader=Loader), want_subdomain)

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

    print("== 主密钥注入链 ==")
    assert_entrypoint_wrapper(
        ENTRYPOINT_SH.read_text(encoding="utf-8"), DOCKERFILE.read_text(encoding="utf-8")
    )

    print("\n全部断言通过；外部真机验收仍以 evidence gap 单独记录。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
