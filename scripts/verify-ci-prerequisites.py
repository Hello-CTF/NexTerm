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

匹配语义（round-1 评审后收紧）：

1. 只审计**可执行** shell 命令 —— 每个 run 块先做引号感知的 shell 注释剔除
   （整行注释与行内注释），被注释掉的 apt/go test/--require-evidence 不再算数。
2. 显式禁用的步骤（`if: false`、`if: ${{ false }}`）视为不存在：被禁用的
   前置步骤不能满足顺序要求，被禁用的 gate 等同于被删除。
3. 矩阵 job 按腿（matrix include leg）评估前置步骤的 `if` 条件覆盖：
   只支持 `matrix.K == 'V'` / `!=` 与 `&&`/`||` 组合（本仓库用到的形式）；
   无法判定的表达式按可满足处理（不误伤），但任何需要 gtk 的 Linux 腿
   （desktop 腿编译 cgo；server 腿按 build.mjs cgo 策略为 CGO_ENABLED=0）
   都必须有覆盖该腿的启用前置步骤排在第一个敏感步骤之前。

`--self-test` 运行变异负对照：对解析后的工作流做注释化/禁用/删腿等变异，
断言对应审计必然失败，防止本检查器自身退化成橡皮图章。

依赖：PyYAML（`pip install pyyaml`）。用法：

    python3 scripts/verify-ci-prerequisites.py [--root PATH] [--self-test]
"""

from __future__ import annotations

import argparse
import copy
import re
import sys
from pathlib import Path

if not __debug__:
    sys.exit("禁止使用 python -O/PYTHONOPTIMIZE：优化模式会删除门禁断言")

DEFAULT_ROOT = Path(__file__).resolve().parent.parent
CI_WORKFLOW = ".github/workflows/ci.yml"
RELEASE_WORKFLOW = ".github/workflows/release.yml"
WORKFLOWS = (CI_WORKFLOW, RELEASE_WORKFLOW, ".github/workflows/pages.yml")

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
MATRIX_ATOM = re.compile(r"""^matrix\.([A-Za-z_][A-Za-z0-9_-]*)\s*(==|!=)\s*(?:'([^']*)'|"([^"]*)")$""")
TRUE_ATOMS = {"true", "always()", "success()"}
FALSE_ATOMS = {"false"}


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


def strip_shell_comments(text: str) -> str:
    """剔除 bash 风格的注释（整行与行内），保留引号内的 `#` 与 `${#var}`。

    `#` 只在行首或空白之后才开始注释（与 bash/pwsh 的 token 规则一致），
    因此 `printf '### x'` 与 `${#files[@]}` 不受影响。
    """
    lines = []
    for line in text.splitlines():
        kept = []
        quote = None
        index = 0
        while index < len(line):
            char = line[index]
            if quote:
                kept.append(char)
                if quote == '"' and char == "\\" and index + 1 < len(line):
                    kept.append(line[index + 1])
                    index += 2
                    continue
                if char == quote:
                    quote = None
                index += 1
                continue
            if char in ("'", '"'):
                quote = char
                kept.append(char)
                index += 1
                continue
            if char == "#" and (index == 0 or line[index - 1] in " \t"):
                break
            kept.append(char)
            index += 1
        lines.append("".join(kept))
    return "\n".join(lines)


def split_boolean(text: str, operator: str) -> list[str]:
    """按引号外的 `&&` / `||` 拆分条件表达式。"""
    parts = []
    current = []
    in_quote = False
    index = 0
    while index < len(text):
        char = text[index]
        if char == "'":
            in_quote = not in_quote
            current.append(char)
            index += 1
            continue
        if not in_quote and text.startswith(operator, index):
            parts.append("".join(current))
            current = []
            index += len(operator)
            continue
        current.append(char)
        index += 1
    parts.append("".join(current))
    return parts


def eval_condition_on_leg(condition, leg: dict):
    """在一条 matrix 腿上求值 `if` 条件；无法判定时返回 None（按可满足处理）。

    只判定 `true`/`false`/`always()`/`success()` 字面量与
    `matrix.K == 'V'` / `matrix.K != 'V'`（可 && / || 组合）；其余表达式
    （github.*、env.*、函数调用等）返回 None，避免对合法条件误报。
    """
    text = str(condition).strip()
    wrapped = re.fullmatch(r"\$\{\{(.*)\}\}", text, re.DOTALL)
    if wrapped:
        text = wrapped.group(1).strip()
    if not text:
        return None
    for or_part in split_boolean(text, "||"):
        and_value = True
        and_known = True
        for and_part in split_boolean(or_part, "&&"):
            atom = and_part.strip()
            lowered = atom.lower()
            if lowered in TRUE_ATOMS:
                atom_value = True
            elif lowered in FALSE_ATOMS:
                atom_value = False
            else:
                match = MATRIX_ATOM.match(atom)
                if match:
                    key, op = match.group(1), match.group(2)
                    wanted = match.group(3) if match.group(3) is not None else match.group(4)
                    if key not in leg:
                        atom_value = None
                    else:
                        actual = str(leg[key])
                        atom_value = (actual == wanted) if op == "==" else (actual != wanted)
                else:
                    atom_value = None
            if atom_value is None:
                if and_value:
                    and_known = False
                    break
            else:
                and_value = and_value and atom_value
        if not and_known:
            return None
        if and_value:
            return True
    return False


def step_is_enabled(step: dict) -> bool:
    """步骤是否可能执行：无 `if`，或条件不是可判定的恒假。"""
    if "if" not in step:
        return True
    return eval_condition_on_leg(step["if"], {}) is not False


def step_covers_leg(step: dict, leg: dict) -> bool:
    """步骤在指定 matrix 腿上是否真的会执行（无法判定按会执行处理）。"""
    if "if" not in step:
        return True
    return eval_condition_on_leg(step["if"], leg) is not False


class Audit:
    def __init__(self, quiet: bool = False) -> None:
        self.failures: list[str] = []
        self.checks = 0
        self.quiet = quiet

    def check(self, ok: bool, message: str) -> None:
        self.checks += 1
        if not self.quiet:
            print(("ok   " if ok else "FAIL ") + message)
        if not ok:
            self.failures.append(message)


def job_steps(job: dict) -> list[dict]:
    return [step for step in (job.get("steps") or []) if isinstance(step, dict)]


def step_run(step: dict) -> str:
    """返回剔除 shell 注释后的可执行命令文本。"""
    return strip_shell_comments(str(step.get("run") or ""))


def enabled_job_steps(job: dict) -> list[dict]:
    return [step for step in job_steps(job) if step_is_enabled(step)]


def job_runs(job: dict) -> str:
    return "\n".join(step_run(step) for step in enabled_job_steps(job))


def matrix_legs(job: dict) -> list[dict]:
    return ((job.get("strategy") or {}).get("matrix") or {}).get("include") or []


def legs_of(job: dict) -> list[dict]:
    return matrix_legs(job) or [{}]


def leg_runner(leg: dict, job: dict) -> str:
    return str(leg.get("runner") or job.get("runs-on") or "")


def leg_label(leg: dict) -> str:
    if not leg:
        return "default runner"
    return ",".join(f"{key}={leg[key]}" for key in sorted(leg))


def matrix_tuples(job: dict, keys: tuple[str, ...]) -> set[tuple[str, ...]]:
    return {tuple(str(leg.get(key) or "") for key in keys) for leg in matrix_legs(job)}


def job_runs_on_linux(job: dict) -> bool:
    runs_on = str(job.get("runs-on") or "")
    if "ubuntu" in runs_on:
        return True
    if "matrix.runner" in runs_on:
        return any("ubuntu" in str(leg.get("runner") or "") for leg in matrix_legs(job))
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


def legs_needing_gtk(job: dict, sensitive_runs: list[str]) -> list[dict]:
    """需要 gtk 开发包的 matrix 腿：Ubuntu 腿；若所有敏感步骤都由 matrix.kind
    驱动且每条 Ubuntu 腿都声明了 kind，则只有 desktop 腿编译 cgo 桌面二进制
    （server 腿按 build.mjs cgo 策略为 CGO_ENABLED=0）。"""
    ubuntu_legs = [leg for leg in legs_of(job) if "ubuntu" in leg_runner(leg, job)]
    if not ubuntu_legs:
        return []
    if (
        sensitive_runs
        and all("matrix.kind" in run for run in sensitive_runs)
        and all(leg.get("kind") for leg in ubuntu_legs)
    ):
        return [leg for leg in ubuntu_legs if leg.get("kind") == "desktop"]
    return ubuntu_legs


def audit_job_ordering(audit: Audit, label: str, job: dict) -> None:
    steps = job_steps(job)
    enabled = [(index, step) for index, step in enumerate(steps) if step_is_enabled(step)]

    sensitive = [(index, step_run(step)) for index, step in enabled if is_cgo_sensitive(step_run(step), job)]
    if sensitive and job_runs_on_linux(job):
        first = sensitive[0][0]
        gtk_steps = [(index, step) for index, step in enabled if installs_gtk(step_run(step))]
        need_legs = legs_needing_gtk(job, [run for _, run in sensitive])
        uncovered = [
            leg
            for leg in need_legs
            if not any(index < first and step_covers_leg(step, leg) for index, step in gtk_steps)
        ]
        covered = ", ".join(leg_label(leg) for leg in need_legs)
        detail = (
            f"uncovered legs: {', '.join(leg_label(leg) for leg in uncovered)}"
            if uncovered
            else "all legs covered"
        )
        audit.check(
            not uncovered,
            f"{label}: Linux cgo legs [{covered}] require an enabled apt step installing "
            f"{'/'.join(GTK_PACKAGES)} before '{steps[first].get('name')}' ({detail})",
        )

    smoke = [(index, step_run(step)) for index, step in enabled if "--smoke" in step_run(step)]
    if smoke and job_runs_on_linux(job):
        first = smoke[0][0]
        xvfb_steps = [(index, step) for index, step in enabled if installs_xvfb(step_run(step))]
        need_legs = legs_needing_gtk(job, [run for _, run in smoke])
        uncovered = [
            leg
            for leg in need_legs
            if not any(index < first and step_covers_leg(step, leg) for index, step in xvfb_steps)
        ]
        detail = (
            f"uncovered legs: {', '.join(leg_label(leg) for leg in uncovered)}"
            if uncovered
            else "all legs covered"
        )
        audit.check(
            not uncovered,
            f"{label}: desktop smoke legs require an enabled xvfb apt step before "
            f"'{steps[first].get('name')}' ({detail})",
        )

    consumers = [(index, step) for index, step in enabled if consumes_wails_cli(step_run(step))]
    if consumers:
        first = consumers[0][0]
        install_steps = [(index, step) for index, step in enabled if wails_install_version(step_run(step))]
        legs_to_cover = [leg for leg in legs_of(job) if any(step_covers_leg(step, leg) for _, step in consumers)]
        uncovered = [
            leg
            for leg in legs_to_cover
            if not any(index < first and step_covers_leg(step, leg) for index, step in install_steps)
        ]
        detail = (
            f"uncovered legs: {', '.join(leg_label(leg) for leg in uncovered)}"
            if uncovered
            else "all legs covered"
        )
        audit.check(
            not uncovered,
            f"{label}: wails3-consuming steps require the pinned Wails CLI install enabled "
            f"before '{steps[first].get('name')}' ({detail})",
        )

    packaging = [(index, step) for index, step in enabled if "--package" in step_run(step)]
    if packaging:
        first = packaging[0][0]
        nsis_steps = [(index, step) for index, step in enabled if "choco install nsis" in step_run(step)]
        legs_to_cover = [
            leg
            for leg in legs_of(job)
            if "windows" in leg_runner(leg, job) and any(step_covers_leg(step, leg) for _, step in packaging)
        ]
        uncovered = [
            leg
            for leg in legs_to_cover
            if not any(index < first and step_covers_leg(step, leg) for index, step in nsis_steps)
        ]
        detail = (
            f"uncovered legs: {', '.join(leg_label(leg) for leg in uncovered)}"
            if uncovered
            else "all legs covered"
        )
        audit.check(
            not uncovered,
            f"{label}: Windows packaging steps require the NSIS install enabled before "
            f"'{steps[first].get('name')}' ({detail})",
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
            if not isinstance(job, dict):
                continue
            for step in enabled_job_steps(job):
                version = wails_install_version(step_run(step))
                if version:
                    installs.append((workflow, job_id, version))
    for workflow, job_id, version in installs:
        audit.check(
            version == pinned,
            f"{workflow}:{job_id} installs wails3 {pinned} (the scripts/build.mjs pin)",
        )
    quality = any(workflow == CI_WORKFLOW and job_id == "quality" for workflow, job_id, _ in installs)
    desktop = any(workflow == RELEASE_WORKFLOW and job_id == "desktop" for workflow, job_id, _ in installs)
    audit.check(quality, "ci.yml quality job installs the pinned Wails CLI (bindings drift gate needs it)")
    audit.check(desktop, "release.yml desktop job installs the pinned Wails CLI (bindings + NSIS packaging need it)")


def audit_matrices(audit: Audit, documents: dict[str, dict]) -> None:
    ci_jobs = documents[CI_WORKFLOW].get("jobs") or {}
    release_jobs = documents[RELEASE_WORKFLOW].get("jobs") or {}
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
    release_jobs = documents[RELEASE_WORKFLOW].get("jobs") or {}
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
    ci_jobs = documents[CI_WORKFLOW].get("jobs") or {}
    audit.check(
        "--repro-check" in job_runs(ci_jobs.get("native") or {}),
        "ci.yml native build keeps --repro-check",
    )


def audit_quality_gates(audit: Audit, documents: dict[str, dict]) -> None:
    quality = job_runs((documents[CI_WORKFLOW].get("jobs") or {}).get("quality") or {})
    for needle, label in QUALITY_GATES:
        audit.check(needle in quality, f"ci.yml quality job keeps the {label}")


def audit_cgo_policy(audit: Audit, root: Path) -> None:
    build = (root / "scripts" / "build.mjs").read_text(encoding="utf-8")
    audit.check(CGO_POLICY in build, "scripts/build.mjs keeps cgo enabled only for darwin/linux desktop")
    audit.check('"static-linux-server"' in build, "scripts/build.mjs keeps the static Linux server assertion")


def run_all_audits(audit: Audit, root: Path, documents: dict[str, dict]) -> None:
    for workflow, document in documents.items():
        for job_id, job in (document.get("jobs") or {}).items():
            if isinstance(job, dict):
                audit_job_ordering(audit, f"{workflow}:{job_id}", job)
    audit_wails_pin(audit, root, documents)
    audit_matrices(audit, documents)
    audit_release_contract(audit, documents)
    audit_quality_gates(audit, documents)
    audit_cgo_policy(audit, root)


def _find_step(document: dict, job_id: str, needle: str) -> dict:
    job = (document.get("jobs") or {}).get(job_id) or {}
    for step in job_steps(job):
        if needle in step_run(step):
            return step
    raise AssertionError(f"no enabled step containing {needle!r} in job {job_id!r}")


def _replace(step: dict, old: str, new: str) -> None:
    run = str(step.get("run") or "")
    if old not in run:
        raise AssertionError(f"anchor {old!r} not found in step run: {run!r}")
    step["run"] = run.replace(old, new)


def _mutate_comment_gtk_apt(documents: dict[str, dict]) -> None:
    step = _find_step(documents[CI_WORKFLOW], "quality", "libgtk-4-dev")
    step["run"] = "# sudo apt-get update\n# sudo apt-get install -y --no-install-recommends libgtk-4-dev libwebkitgtk-6.0-dev"


def _mutate_disable_gtk_apt(documents: dict[str, dict]) -> None:
    _find_step(documents[CI_WORKFLOW], "quality", "libgtk-4-dev")["if"] = "false"


def _mutate_disable_gtk_apt_expression(documents: dict[str, dict]) -> None:
    _find_step(documents[CI_WORKFLOW], "quality", "libgtk-4-dev")["if"] = "${{ false }}"


def _mutate_comment_go_test(documents: dict[str, dict]) -> None:
    step = _find_step(documents[CI_WORKFLOW], "quality", "go test -mod=readonly ./...")
    step["run"] = "# go test -mod=readonly ./..."


def _mutate_inline_comment_require_evidence(documents: dict[str, dict]) -> None:
    step = _find_step(documents[RELEASE_WORKFLOW], "desktop", "--package")
    _replace(step, "--package --require-evidence", "--package # --require-evidence")


def _mutate_remove_xvfb(documents: dict[str, dict]) -> None:
    step = _find_step(documents[CI_WORKFLOW], "native", "libgtk-4-dev")
    _replace(step, "libwebkitgtk-6.0-dev xvfb", "libwebkitgtk-6.0-dev")


def _mutate_disable_wails_install(documents: dict[str, dict]) -> None:
    _find_step(documents[RELEASE_WORKFLOW], "desktop", "cmd/wails3@")["if"] = "false"


def _mutate_comment_wails_install(documents: dict[str, dict]) -> None:
    step = _find_step(documents[CI_WORKFLOW], "quality", "cmd/wails3@")
    _replace(step, "go install", "# go install")


def _mutate_wails_pin_drift(documents: dict[str, dict]) -> None:
    step = _find_step(documents[CI_WORKFLOW], "quality", "cmd/wails3@")
    _replace(step, "cmd/wails3@v3.0.0-alpha.98", "cmd/wails3@v3.0.0-alpha.99")


def _mutate_drop_matrix_leg(documents: dict[str, dict]) -> None:
    legs = documents[CI_WORKFLOW]["jobs"]["native"]["strategy"]["matrix"]["include"]
    if len(legs) <= 1:
        raise AssertionError("native matrix has no leg to drop")
    legs.pop()


def _mutate_weaken_publish_gate(documents: dict[str, dict]) -> None:
    step = _find_step(documents[RELEASE_WORKFLOW], "publish", "-eq 11")
    _replace(step, "-eq 11", "-eq 10")


# (名称, 变异函数, 期望出现的失败消息子串) —— 覆盖 round-1 评审的四个误判场景，
# 外加前置/CLI/矩阵/发布门的关键变异，确保本检查器自身不退化。
MUTATIONS = (
    ("commented-out GTK apt install is rejected", _mutate_comment_gtk_apt, "libgtk-4-dev"),
    ("if:false GTK apt step is rejected", _mutate_disable_gtk_apt, "libgtk-4-dev"),
    ("${{ false }} GTK apt step is rejected", _mutate_disable_gtk_apt_expression, "libgtk-4-dev"),
    ("commented-out go test gate is rejected", _mutate_comment_go_test, "go test gate"),
    ("inline-commented --require-evidence is rejected", _mutate_inline_comment_require_evidence, "--require-evidence"),
    ("xvfb removed from the native apt step is rejected", _mutate_remove_xvfb, "xvfb"),
    ("disabled pinned Wails CLI install is rejected", _mutate_disable_wails_install, "pinned Wails CLI install"),
    ("commented-out Wails CLI install is rejected", _mutate_comment_wails_install, "pinned Wails CLI install"),
    ("wails3 pin drift is rejected", _mutate_wails_pin_drift, "the scripts/build.mjs pin"),
    ("dropped native matrix leg is rejected", _mutate_drop_matrix_leg, "eight-target contract"),
    ("weakened publish file-count gate is rejected", _mutate_weaken_publish_gate, "8 packages + 3 evidence files"),
)


def self_test(root: Path, documents: dict[str, dict]) -> int:
    print("== self-test: mutation coverage ==")
    failures: list[str] = []

    control = Audit(quiet=True)
    run_all_audits(control, root, copy.deepcopy(documents))
    if control.failures:
        failures.append("positive control")
        print(f"FAIL self-test: unmutated documents must pass, got: {control.failures}")
    else:
        print("ok   self-test: unmutated documents pass (positive control)")

    for name, mutate, expected in MUTATIONS:
        mutated = copy.deepcopy(documents)
        try:
            mutate(mutated)
        except AssertionError as error:
            failures.append(name)
            print(f"FAIL self-test: {name} setup error: {error}")
            continue
        audit = Audit(quiet=True)
        run_all_audits(audit, root, mutated)
        if any(expected in failure for failure in audit.failures):
            print(f"ok   self-test: {name}")
        else:
            failures.append(name)
            print(f"FAIL self-test: {name} was NOT rejected (expected a failure containing {expected!r}; got {audit.failures or 'NO FAILURES'})")

    if failures:
        print(f"\nself-test FAILED for: {', '.join(failures)}")
        return 1
    print(f"\n全部 {len(MUTATIONS)} 个变异负对照均被正确拒绝。")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify CI/release workflow Linux cgo prerequisite ordering")
    parser.add_argument("--root", type=Path, default=DEFAULT_ROOT, help="repository root to audit")
    parser.add_argument("--self-test", action="store_true", help="run mutation negative controls instead of the plain audit")
    args = parser.parse_args()
    root = args.root.resolve()
    yaml, Loader = load_yaml()
    documents: dict[str, dict] = {}
    for relative in WORKFLOWS:
        path = root / relative
        if not path.is_file():
            sys.exit(f"missing workflow: {path}")
        documents[relative] = yaml.load(path.read_text(encoding="utf-8"), Loader=Loader)

    if args.self_test:
        return self_test(root, documents)

    audit = Audit()
    run_all_audits(audit, root, documents)
    if audit.failures:
        print(f"\n{len(audit.failures)} of {audit.checks} checks FAILED:")
        for failure in audit.failures:
            print(f"  - {failure}")
        return 1
    print(f"\n全部 {audit.checks} 条 CI 前置依赖/发布合同断言通过。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
