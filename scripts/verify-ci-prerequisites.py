#!/usr/bin/env python3
"""静态校验 CI/Release 工作流的 Linux cgo 前置依赖顺序与发布合同不变量。

为什么需要它 —— CI run 37135394807 死在 "Install the pinned Wails CLI"：
`go install wails3` 在 Linux 上要编译 cgo webview 绑定，干净的 ubuntu-latest
上 pkg-config 找不到 gtk4 / webkitgtk-6.0。GitHub 每个 job 都是一台全新 runner，
所以每个会编译 cgo 代码的 job 都必须在**第一个** cgo 敏感步骤之前按位置安装
libgtk-4-dev / libwebkitgtk-6.0-dev（桌面 smoke 还需要 xvfb），release desktop
job 里 Wails CLI 的安装也必须排在 apt 之后。本脚本把这条顺序规则和发布合同
（八目标矩阵、wails3 版本钉、tag 前置条件、evidence gate、cgo 策略）固化成
静态断言，防止同类回归，也防止靠删测试/删矩阵/删 gate 来让 CI 变绿。

依赖：PyYAML（`pip install pyyaml`）。用法：

    python3 scripts/verify-ci-prerequisites.py [--root PATH]
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

if not __debug__:
    sys.exit("禁止使用 python -O/PYTHONOPTIMIZE：优化模式会删除门禁断言")

DEFAULT_ROOT = Path(__file__).resolve().parent.parent
WORKFLOWS = (
    ".github/workflows/ci.yml",
    ".github/workflows/release.yml",
    ".github/workflows/pages.yml",
)

WAILS_MODULE = "github.com/wailsapp/wails/v3/cmd/wails3"
GTK_PACKAGES = ("libgtk-4-dev", "libwebkitgtk-6.0-dev")
CGO_POLICY = 'kind === "desktop" && (goos === "darwin" || goos === "linux") ? "1" : "0"'

# 用户红线：不得通过删除测试或矩阵来规避 CI 失败 —— 质量门步骤必须原样保留。
QUALITY_GATES = (
    ("gofmt -l", "gofmt formatting gate"),
    ("go vet", "go vet gate"),
    ("go test -mod=readonly ./...", "go test gate"),
    ("go test -mod=readonly -race ./...", "go race test gate"),
    ("pnpm typecheck", "frontend typecheck"),
    ("pnpm lint", "frontend lint"),
    ("pnpm test", "frontend tests"),
    ("scripts/build.mjs bindings", "Wails bindings drift gate"),
    ("scripts/build.mjs frontend --repro-check", "reproducible frontend gate"),
    ("unittest discover -s tests/parity", "Go self-consistency parity suite"),
    ("verify-manifest-injects.py", "LazyCat manifest contract check"),
)

EXPECTED_CI_NATIVE = {
    ("windows", "amd64", "desktop"),
    ("windows", "arm64", "desktop"),
    ("darwin", "arm64", "desktop"),
    ("darwin", "amd64", "desktop"),
    ("linux", "amd64", "desktop"),
    ("linux", "arm64", "desktop"),
    ("linux", "amd64", "server"),
    ("linux", "arm64", "server"),
}
EXPECTED_RELEASE_DESKTOP = {
    ("windows", "amd64"),
    ("windows", "arm64"),
    ("darwin", "arm64"),
    ("darwin", "amd64"),
    ("linux", "amd64"),
    ("linux", "arm64"),
}
EXPECTED_RELEASE_SERVER = {("ubuntu-latest", "amd64"), ("ubuntu-24.04-arm", "arm64")}

GO_COMPILE = re.compile(r"\bgo\s+(?:vet|test|build|install)\b")
WAILS_CLI = re.compile(r"\bwails3\b")
WAILS_INSTALL = re.compile(r"go\s+install\s+" + re.escape(WAILS_MODULE) + r"@(\S+)")
BUILD_MJS = re.compile(r"scripts/build\.mjs\s+(\S+)")


def load_yaml():
    """导入 PyYAML，并摘掉 YAML 1.1 的 bool 隐式解析器（Norway problem）。

    否则工作流顶层的 `on:` 会被解析成 `True` 键，jobs 就拿不到了。
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


class Audit:
    def __init__(self) -> None:
        self.failures: list[str] = []
        self.checks = 0

    def check(self, ok: bool, message: str) -> None:
        self.checks += 1
        print(("ok   " if ok else "FAIL ") + message)
        if not ok:
            self.failures.append(message)


def job_steps(job: dict) -> list[dict]:
    return [step for step in (job.get("steps") or []) if isinstance(step, dict)]


def step_run(step: dict) -> str:
    return str(step.get("run") or "")


def job_runs(job: dict) -> str:
    return "\n".join(step_run(step) for step in job_steps(job))


def matrix_legs(job: dict) -> list[dict]:
    return ((job.get("strategy") or {}).get("matrix") or {}).get("include") or []


def matrix_tuples(job: dict, keys: tuple[str, ...]) -> set[tuple[str, ...]]:
    return {tuple(str(leg.get(key) or "") for key in keys) for leg in matrix_legs(job)}


def job_runs_on_linux(job: dict) -> bool:
    runs_on = str(job.get("runs-on") or "")
    if "ubuntu" in runs_on:
        return True
    if "matrix.runner" in runs_on:
        return any("ubuntu" in str(leg.get("runner") or "") for leg in matrix_legs(job))
    return False


def job_matrix_has_windows(job: dict) -> bool:
    runs_on = str(job.get("runs-on") or "")
    if "windows" in runs_on:
        return True
    if "matrix.runner" in runs_on:
        return any("windows" in str(leg.get("runner") or "") for leg in matrix_legs(job))
    return False


def matrix_has_linux_desktop(job: dict) -> bool:
    for leg in matrix_legs(job):
        if "ubuntu" not in str(leg.get("runner") or ""):
            continue
        if str(leg.get("kind") or "desktop") == "desktop":
            return True
    return False


def is_cgo_sensitive(run: str, job: dict) -> bool:
    """该步骤在 Linux runner 上是否会编译需要 gtk4/webkitgtk-6.0 的 cgo 代码。

    覆盖四类：直接 go vet/test/build/install（quality job 的 ./... 包含
    cmd/nexterm-desktop，默认 CGO 启用）、wails3 CLI 调用（bindings 生成、
    Windows webview2bootstrapper）、build.mjs 的 desktop/release/bindings
    子命令，以及 matrix.kind 驱动且矩阵含 Linux desktop 腿的构建步骤。
    顺序按位置判定：GitHub 逐 job 从干净 runner 开始按序执行步骤，条件跳过
    的 apt 步骤（如 linux/server 腿）不影响其它腿仍排在其后的构建。
    """
    if GO_COMPILE.search(run) or WAILS_CLI.search(run):
        return True
    if "scripts/build.mjs" not in run:
        return False
    if "matrix.kind" in run and matrix_has_linux_desktop(job):
        return True
    return any(match.group(1) in ("desktop", "release", "bindings") for match in BUILD_MJS.finditer(run))


def consumes_wails_cli(run: str) -> bool:
    if re.search(r"\bwails3\s", run):
        return True
    return "--package" in run or any(match.group(1) == "bindings" for match in BUILD_MJS.finditer(run))


def installs_gtk(run: str) -> bool:
    return "apt-get" in run and all(package in run for package in GTK_PACKAGES)


def installs_xvfb(run: str) -> bool:
    return "apt-get" in run and "xvfb" in run


def wails_install_version(run: str) -> str | None:
    match = WAILS_INSTALL.search(run)
    return match.group(1) if match else None


def audit_job_ordering(audit: Audit, label: str, job: dict) -> None:
    steps = job_steps(job)
    runs = [step_run(step) for step in steps]

    sensitive = [index for index, run in enumerate(runs) if is_cgo_sensitive(run, job)]
    if sensitive and job_runs_on_linux(job):
        first = min(sensitive)
        gtk_index = next((index for index, run in enumerate(runs) if installs_gtk(run)), None)
        audit.check(
            gtk_index is not None and gtk_index < first,
            f"{label}: '{steps[first].get('name')}' compiles Linux cgo code and every such step "
            f"is preceded by an apt step installing {'/'.join(GTK_PACKAGES)}",
        )

    smoke = [index for index, run in enumerate(runs) if "--smoke" in run]
    if smoke and job_runs_on_linux(job):
        xvfb_index = next((index for index, run in enumerate(runs) if installs_xvfb(run)), None)
        audit.check(
            xvfb_index is not None and xvfb_index < min(smoke),
            f"{label}: desktop smoke steps are preceded by an xvfb apt step",
        )

    consumers = [index for index, run in enumerate(runs) if consumes_wails_cli(run)]
    if consumers:
        install_index = next((index for index, run in enumerate(runs) if wails_install_version(run)), None)
        audit.check(
            install_index is not None and install_index < min(consumers),
            f"{label}: wails3-consuming steps are preceded by the pinned Wails CLI install",
        )

    packaging = [index for index, run in enumerate(runs) if "--package" in run]
    if packaging and job_matrix_has_windows(job):
        nsis_index = next((index for index, run in enumerate(runs) if "choco install nsis" in run), None)
        audit.check(
            nsis_index is not None and nsis_index < min(packaging),
            f"{label}: Windows packaging steps are preceded by the NSIS install",
        )


def audit_wails_pin(audit: Audit, root: Path, documents: dict[str, dict]) -> None:
    build = (root / "scripts" / "build.mjs").read_text(encoding="utf-8")
    match = re.search(r'WAILS_VERSION\s*=\s*"([^"]+)"', build)
    audit.check(match is not None, "scripts/build.mjs declares the pinned WAILS_VERSION")
    if match is None:
        return
    pinned = match.group(1)
    installs = []
    for workflow, document in documents.items():
        for job_id, job in (document.get("jobs") or {}).items():
            for step in job_steps(job):
                version = wails_install_version(step_run(step))
                if version:
                    installs.append((workflow, job_id, version))
    for workflow, job_id, version in installs:
        audit.check(
            version == pinned,
            f"{workflow}:{job_id} installs wails3 {pinned} (the scripts/build.mjs pin)",
        )
    quality = any(workflow.endswith("ci.yml") and job_id == "quality" for workflow, job_id, _ in installs)
    desktop = any(workflow.endswith("release.yml") and job_id == "desktop" for workflow, job_id, _ in installs)
    audit.check(quality, "ci.yml quality job installs the pinned Wails CLI (bindings drift gate needs it)")
    audit.check(desktop, "release.yml desktop job installs the pinned Wails CLI (bindings + NSIS packaging need it)")


def audit_matrices(audit: Audit, documents: dict[str, dict]) -> None:
    ci_jobs = documents[".github/workflows/ci.yml"].get("jobs") or {}
    release_jobs = documents[".github/workflows/release.yml"].get("jobs") or {}
    native = matrix_tuples(ci_jobs.get("native") or {}, ("os", "arch", "kind"))
    audit.check(
        native == EXPECTED_CI_NATIVE,
        f"ci.yml native matrix keeps the eight-target contract: {sorted(native)}",
    )
    desktop = matrix_tuples(release_jobs.get("desktop") or {}, ("os", "arch"))
    audit.check(
        desktop == EXPECTED_RELEASE_DESKTOP,
        f"release.yml desktop matrix keeps all six native packages: {sorted(desktop)}",
    )
    server = matrix_tuples(release_jobs.get("server") or {}, ("runner", "arch"))
    audit.check(
        server == EXPECTED_RELEASE_SERVER,
        f"release.yml server matrix keeps both Linux archives: {sorted(server)}",
    )


def audit_release_contract(audit: Audit, documents: dict[str, dict]) -> None:
    release_jobs = documents[".github/workflows/release.yml"].get("jobs") or {}
    precondition = job_runs(release_jobs.get("precondition") or {})
    audit.check(
        "GITHUB_REF_NAME" in precondition and "wails.json" in precondition,
        "release.yml keeps the tag == wails.json version precondition",
    )
    publish = job_runs(release_jobs.get("publish") or {})
    audit.check("-eq 11" in publish, "release.yml publish still gates on 8 packages + 3 evidence files")
    audit.check("release-evidence.mjs" in publish, "release.yml publish still runs the release evidence verifier")
    desktop = job_runs(release_jobs.get("desktop") or {})
    audit.check(
        all(flag in desktop for flag in ("--repro-check", "--package", "--require-evidence")),
        "release.yml desktop build keeps --repro-check/--package/--require-evidence",
    )
    server = job_runs(release_jobs.get("server") or {})
    audit.check(
        "--repro-check" in server and "--require-evidence" in server,
        "release.yml server build keeps --repro-check and --require-evidence",
    )
    ci_jobs = documents[".github/workflows/ci.yml"].get("jobs") or {}
    audit.check(
        "--repro-check" in job_runs(ci_jobs.get("native") or {}),
        "ci.yml native build keeps --repro-check",
    )


def audit_quality_gates(audit: Audit, documents: dict[str, dict]) -> None:
    quality = job_runs((documents[".github/workflows/ci.yml"].get("jobs") or {}).get("quality") or {})
    for needle, label in QUALITY_GATES:
        audit.check(needle in quality, f"ci.yml quality job keeps the {label}")


def audit_cgo_policy(audit: Audit, root: Path) -> None:
    build = (root / "scripts" / "build.mjs").read_text(encoding="utf-8")
    audit.check(CGO_POLICY in build, "scripts/build.mjs keeps cgo enabled only for darwin/linux desktop")
    audit.check('"static-linux-server"' in build, "scripts/build.mjs keeps the static Linux server assertion")


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify CI/release workflow Linux cgo prerequisite ordering")
    parser.add_argument("--root", type=Path, default=DEFAULT_ROOT, help="repository root to audit")
    args = parser.parse_args()
    root = args.root.resolve()
    yaml, Loader = load_yaml()
    documents: dict[str, dict] = {}
    for relative in WORKFLOWS:
        path = root / relative
        if not path.is_file():
            sys.exit(f"missing workflow: {path}")
        documents[relative] = yaml.load(path.read_text(encoding="utf-8"), Loader=Loader)

    audit = Audit()
    for workflow, document in documents.items():
        for job_id, job in (document.get("jobs") or {}).items():
            if isinstance(job, dict):
                audit_job_ordering(audit, f"{workflow}:{job_id}", job)
    audit_wails_pin(audit, root, documents)
    audit_matrices(audit, documents)
    audit_release_contract(audit, documents)
    audit_quality_gates(audit, documents)
    audit_cgo_policy(audit, root)

    if audit.failures:
        print(f"\n{len(audit.failures)} of {audit.checks} checks FAILED:")
        for failure in audit.failures:
            print(f"  - {failure}")
        return 1
    print(f"\n全部 {audit.checks} 条 CI 前置依赖/发布合同断言通过。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
