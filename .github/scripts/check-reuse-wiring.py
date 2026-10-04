#!/usr/bin/env python3
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
CI = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
RELEASE = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())
RESOLVER = ROOT / ".github/scripts/resolve-ci-reuse.sh"

failures = []


def check(name, ok, detail=""):
    if ok:
        print(f"ok - {name}")
    else:
        failures.append(f"{name}: {detail}")
        print(f"FAIL - {name}: {detail}")


def ci_produced_artifacts():
    names = set()
    for step in CI["jobs"]["quality"]["steps"]:
        if str(step.get("uses", "")).startswith("actions/upload-artifact"):
            names.add(step["with"]["name"])
    for entry in CI["jobs"]["native"]["strategy"]["matrix"]["include"]:
        names.add(f"native-{entry['os']}-{entry['arch']}-{entry['kind']}")
    return names


def resolver_required_artifacts():
    match = re.search(r"required=\(([^)]*)\)", RESOLVER.read_text())
    if not match:
        raise AssertionError("resolve-ci-reuse.sh has no required=(...) list")
    return set(match.group(1).split())


def release_download_artifacts():
    names = set()
    for job in RELEASE["jobs"].values():
        matrix_entries = (job.get("strategy", {}).get("matrix", {}) or {}).get("include", [{}])
        for step in job.get("steps") or []:
            with_options = step.get("with") or {}
            if not (str(step.get("uses", "")).startswith("actions/download-artifact") and with_options.get("name")):
                continue
            for values in matrix_entries:
                names.add(matrix_substitute(with_options["name"], values))
    return names


def evaluate_release(scenario):
    results = {"ci-source": scenario["ci-source"]}
    outputs = {"reused": scenario["reused"]} if scenario["ci-source"] == "success" else {}
    for job_id in ("ci", "precondition", "desktop", "server", "publish"):
        job = RELEASE["jobs"][job_id]
        needs = job.get("needs")
        need_list = [needs] if isinstance(needs, str) else list(needs or [])
        if any(results.get(need) in ("failure", "cancelled") for need in need_list):
            results[job_id] = "skipped"
            continue
        condition = job.get("if")
        if condition is None:
            runs = all(results.get(need) == "success" for need in need_list)
        else:
            expr = condition.replace("${{", "").replace("}}", "").strip()
            if expr == "!failure() && !cancelled()":
                runs = True
            elif expr == "needs.ci-source.outputs.reused != 'true'":
                runs = outputs.get("reused") != "true"
            else:
                raise AssertionError(f"unhandled job-level if: {expr}")
        results[job_id] = scenario[job_id] if (runs and job_id == "ci") else ("success" if runs else "skipped")
    return results


def matrix_substitute(script, values):
    def repl(match):
        expr = match.group(1).strip()
        simple = re.fullmatch(r"matrix\.(\w+)", expr)
        if simple:
            return str(values[simple.group(1)])
        ternary = re.fullmatch(r"matrix\.(\w+) == '([\w-]+)' && '([^']*)' \|\| '([^']*)'", expr)
        if ternary:
            return ternary.group(3) if values.get(ternary.group(1)) == ternary.group(2) else ternary.group(4)
        raise AssertionError(f"unhandled matrix expression: {expr}")

    return re.sub(r"\$\{\{(.*?)\}\}", repl, script)


def find_step(job, name_part):
    for step in job.get("steps") or []:
        if name_part in str(step.get("name", "")):
            return step
    raise AssertionError(f"step not found: {name_part}")


def run_resolver(stub_dir, runs_reply, artifacts_reply):
    stub = stub_dir / "gh"
    stub.write_text(
        "#!/usr/bin/env bash\n"
        "case \"$2\" in\n"
        f"  *artifacts*) printf '%s' '{artifacts_reply}' ;;\n"
        f"  *) printf '%s' '{runs_reply}' ;;\n"
        "esac\n"
    )
    stub.chmod(0o755)
    env = {
        "PATH": f"{stub_dir}:{os.environ['PATH']}",
        "GITHUB_REPOSITORY": "stub/repo",
        "GITHUB_SHA": "stubsha",
        "GITHUB_RUN_ID": "999",
        "GH_TOKEN": "stub-token",
    }
    return subprocess.run(
        ["bash", str(RESOLVER)], capture_output=True, text=True, env=env, check=False
    )


def exercise_staging(step_name_part, job_key, values, staged_files, expected_placed):
    step = find_step(RELEASE["jobs"][job_key], step_name_part)
    script = matrix_substitute(step["run"], values)
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        for relative in staged_files:
            target = base / "reused-native" / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(f"stub {relative}\n")
        result = subprocess.run(
            ["bash", "-euo", "pipefail", "-c", script], cwd=base, capture_output=True, text=True, check=False
        )
        if result.returncode != 0:
            check(f"staging {step_name_part} ({values})", False, f"exit {result.returncode}: {result.stderr}")
            return
        placed = [base / "target" / name for name in expected_placed]
        ok = all(path.exists() for path in placed)
        detail = "" if ok else f"missing: {[str(path) for path in placed if not path.exists()]}"
        nesting = base / "target" / "go-build" / "go-build"
        if nesting.exists():
            ok, detail = False, f"nested {nesting}"
        evidence = base / "target" / "release-assets"
        if evidence.exists():
            ok, detail = False, f"CI evidence mixed into target/release-assets: {list(evidence.iterdir())}"
        if ok and values.get("os", "linux") != "windows" and not os.access(placed[0], os.X_OK):
            ok, detail = False, f"{placed[0]} is not executable"
        check(f"staging {step_name_part} ({values})", ok, detail)


produced = ci_produced_artifacts()
check("resolver required set matches CI producers", resolver_required_artifacts() == produced,
      f"resolver={sorted(resolver_required_artifacts())} producers={sorted(produced)}")
check("release downloads are a subset of CI producers", release_download_artifacts() <= produced,
      f"downloads={sorted(release_download_artifacts())}")
check("ci-source checks out the resolver script",
      any("actions/checkout" in str(step.get("uses", "")) for step in RELEASE["jobs"]["ci-source"]["steps"]))

scenarios = [
    ("reuse: ci skipped and release proceeds",
     {"ci-source": "success", "reused": "true", "ci": "success"},
     {"ci-source": "success", "ci": "skipped", "precondition": "success", "desktop": "success", "server": "success", "publish": "success"}),
    ("fallback: full CI success and release proceeds",
     {"ci-source": "success", "reused": "false", "ci": "success"},
     {"ci-source": "success", "ci": "success", "precondition": "success", "desktop": "success", "server": "success", "publish": "success"}),
    ("fallback: CI failure gates the release",
     {"ci-source": "success", "reused": "false", "ci": "failure"},
     {"ci-source": "success", "ci": "failure", "precondition": "skipped", "desktop": "skipped", "server": "skipped", "publish": "skipped"}),
    ("ci-source failure gates the release",
     {"ci-source": "failure", "reused": "false", "ci": "success"},
     {"ci-source": "failure", "ci": "skipped", "precondition": "skipped", "desktop": "skipped", "server": "skipped", "publish": "skipped"}),
]
for name, scenario, expected in scenarios:
    actual = evaluate_release(scenario)
    check(f"job graph: {name}", actual == expected, f"actual={actual}")

full_names = sorted(produced)
with tempfile.TemporaryDirectory() as stub_dir:
    stub = Path(stub_dir)
    result = run_resolver(stub, "424242", json.dumps(full_names))
    check("resolver full set reuses the green run",
          result.returncode == 0 and "reused=true" in result.stdout and "artifact-run-id=424242" in result.stdout,
          f"stdout={result.stdout!r} stderr={result.stderr!r}")
    for missing in full_names:
        partial = json.dumps([name for name in full_names if name != missing])
        result = run_resolver(stub, "424242", partial)
        ok = result.returncode == 0 and "reused=false" in result.stdout and "artifact-run-id=999" in result.stdout
        check(f"resolver falls back when {missing} is missing", ok, f"stdout={result.stdout!r}")
    result = run_resolver(stub, "", json.dumps(full_names))
    check("resolver falls back when no green run exists",
          result.returncode == 0 and "reused=false" in result.stdout and "artifact-run-id=999" in result.stdout,
          f"stdout={result.stdout!r}")

upload_step = find_step(CI["jobs"]["native"], "Upload native runtime")
upload_paths = [line.strip() for line in upload_step["with"]["path"].splitlines() if line.strip()]
check("native upload keeps target/ as the ZIP root", all(path.startswith("target/") for path in upload_paths),
      f"paths={upload_paths}")
for job_key, step_part, values, staged, placed in [
    ("desktop", "Stage the reused production binary", {"os": "windows", "arch": "amd64"},
     ["go-build/nexterm-desktop-windows-amd64.exe", "go-build/nexterm-desktop-windows-amd64.exe.manifest.json",
      "go-build/nexterm-desktop-windows-amd64.exe.artifact.json", "release-assets/desktop-smoke-windows-amd64.json",
      "e2e-sync/report.json"],
     ["go-build/nexterm-desktop-windows-amd64.exe", "go-build/nexterm-desktop-windows-amd64.exe.manifest.json"]),
    ("desktop", "Stage the reused production binary", {"os": "darwin", "arch": "arm64"},
     ["go-build/nexterm-desktop-darwin-arm64", "go-build/nexterm-desktop-darwin-arm64.manifest.json",
      "go-build/nexterm-desktop-darwin-arm64.artifact.json", "release-assets/desktop-smoke-darwin-arm64.json"],
     ["go-build/nexterm-desktop-darwin-arm64", "go-build/nexterm-desktop-darwin-arm64.manifest.json"]),
    ("server", "Stage the reused production server binary", {"arch": "amd64"},
     ["go-build/nexterm-server-linux-amd64", "go-build/nexterm-server-linux-amd64.manifest.json",
      "go-build/nexterm-server-linux-amd64.artifact.json", "e2e-sync/report.json", "e2e-sync/e2e.log"],
     ["go-build/nexterm-server-linux-amd64", "go-build/nexterm-server-linux-amd64.manifest.json"]),
]:
    exercise_staging(step_part, job_key, values, staged, placed)

for workflow, label in ((CI, "ci.yml"), (RELEASE, "release.yml")):
    offenders = []
    for job_id, job in workflow["jobs"].items():
        runners = [entry.get("runner", "") for entry in (job.get("strategy", {}).get("matrix", {}) or {}).get("include", [])]
        if not any("windows" in runner for runner in runners):
            continue
        for step in job.get("steps") or []:
            script = step.get("run")
            if script and "if [" in script and step.get("shell") != "bash":
                offenders.append(f"{job_id}/{step.get('name')}")
    check(f"{label} windows bash-syntax steps declare shell: bash", not offenders, f"offenders={offenders}")

if failures:
    print(f"\n{len(failures)} check(s) failed")
    sys.exit(1)
print("\nall workflow reuse wiring checks passed")
