#!/usr/bin/env python3
import copy
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
RESOLVER_TEXT = (ROOT / ".github/scripts/resolve-ci-reuse.sh").read_text()


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


def matrix_entries(job):
    return (job.get("strategy", {}).get("matrix", {}) or {}).get("include", [{}])


def artifact_step_names(workflow, action_prefix):
    names = set()
    for job in workflow["jobs"].values():
        for values in matrix_entries(job):
            for step in job.get("steps") or []:
                with_options = step.get("with") or {}
                if str(step.get("uses", "")).startswith(action_prefix) and with_options.get("name"):
                    names.add(matrix_substitute(with_options["name"], values))
    return names


def ci_produced_artifacts(ci):
    return artifact_step_names(ci, "actions/upload-artifact")


def release_download_artifacts(release):
    return artifact_step_names(release, "actions/download-artifact")


def resolver_required_artifacts(resolver_text):
    match = re.search(r"required=\(([^)]*)\)", resolver_text)
    if not match:
        raise AssertionError("resolve-ci-reuse.sh has no required=(...) list")
    return set(match.group(1).split())


def find_step(job, name_part):
    for step in job.get("steps") or []:
        if name_part in str(step.get("name", "")):
            return step
    raise AssertionError(f"step not found: {name_part}")


def find_native_download_step(release, job_key):
    for step in release["jobs"][job_key].get("steps") or []:
        with_options = step.get("with") or {}
        if str(step.get("uses", "")).startswith("actions/download-artifact") and str(with_options.get("name", "")).startswith("native-"):
            return step
    raise AssertionError(f"native download step not found in {job_key}")


def evaluate_release(release, scenario):
    results = {"ci-source": scenario["ci-source"]}
    outputs = {"reused": scenario["reused"]} if scenario["ci-source"] == "success" else {}
    for job_id in ("ci", "precondition", "desktop", "server", "publish"):
        job = release["jobs"][job_id]
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


def run_resolver(resolver_text, stub_dir, runs_reply, artifacts_reply):
    stub = stub_dir / "gh"
    stub.write_text(
        "#!/usr/bin/env bash\n"
        "case \"$2\" in\n"
        f"  *artifacts*) printf '%s' '{artifacts_reply}' ;;\n"
        f"  *) printf '%s' '{runs_reply}' ;;\n"
        "esac\n"
    )
    stub.chmod(0o755)
    resolver = stub_dir / "resolve-ci-reuse.sh"
    resolver.write_text(resolver_text)
    env = {
        "PATH": f"{stub_dir}:{os.environ['PATH']}",
        "GITHUB_REPOSITORY": "stub/repo",
        "GITHUB_SHA": "stubsha",
        "GITHUB_RUN_ID": "999",
        "GH_TOKEN": "stub-token",
    }
    return subprocess.run(
        ["bash", str(resolver)], capture_output=True, text=True, env=env, check=False
    )


def exercise_staging(release, job_key, step_name_part, values, staged_files, expected_placed, download_path):
    step = find_step(release["jobs"][job_key], step_name_part)
    script = matrix_substitute(step["run"], values)
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        for relative in staged_files:
            target = base / download_path / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(f"stub {relative}\n")
        result = subprocess.run(
            ["bash", "-euo", "pipefail", "-c", script], cwd=base, capture_output=True, text=True, check=False
        )
        if result.returncode != 0:
            return False, f"exit {result.returncode}: {result.stderr}"
        placed = [base / "target" / name for name in expected_placed]
        if not all(path.exists() for path in placed):
            return False, f"missing: {[str(path) for path in placed if not path.exists()]}"
        nesting = base / "target" / "go-build" / "go-build"
        if nesting.exists():
            return False, f"nested {nesting}"
        evidence = base / "target" / "release-assets"
        if evidence.exists():
            return False, f"CI evidence mixed into target/release-assets: {list(evidence.iterdir())}"
        if values.get("os", "linux") != "windows" and not os.access(placed[0], os.X_OK):
            return False, f"{placed[0]} is not executable"
        return True, ""


def run_checks(ci, release, resolver_text):
    results = []

    def check(name, ok, detail=""):
        results.append((name, ok, detail))

    produced = ci_produced_artifacts(ci)
    required = resolver_required_artifacts(resolver_text)
    check("resolver required artifacts are all produced by CI", required <= produced,
          f"missing from CI: {sorted(required - produced)}")
    check("release downloads are a subset of CI producers", release_download_artifacts(release) <= produced,
          f"downloads={sorted(release_download_artifacts(release))}")
    check("ci-source checks out the resolver script",
          any("actions/checkout" in str(step.get("uses", "")) for step in release["jobs"]["ci-source"]["steps"]))

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
        actual = evaluate_release(release, scenario)
        check(f"job graph: {name}", actual == expected, f"actual={actual}")

    full_names = sorted(resolver_required_artifacts(resolver_text))
    with tempfile.TemporaryDirectory() as stub_dir:
        stub = Path(stub_dir)
        result = run_resolver(resolver_text, stub, "424242", json.dumps(full_names))
        check("resolver full set reuses the green run",
              result.returncode == 0 and "reused=true" in result.stdout and "artifact-run-id=424242" in result.stdout,
              f"stdout={result.stdout!r} stderr={result.stderr!r}")
        for missing in full_names:
            partial = json.dumps([name for name in full_names if name != missing])
            result = run_resolver(resolver_text, stub, "424242", partial)
            ok = result.returncode == 0 and "reused=false" in result.stdout and "artifact-run-id=999" in result.stdout
            check(f"resolver falls back when {missing} is missing", ok, f"stdout={result.stdout!r}")
        result = run_resolver(resolver_text, stub, "", json.dumps(full_names))
        check("resolver falls back when no green run exists",
              result.returncode == 0 and "reused=false" in result.stdout and "artifact-run-id=999" in result.stdout,
              f"stdout={result.stdout!r}")

    upload_step = find_step(ci["jobs"]["native"], "Upload native runtime")
    upload_paths = [line.strip() for line in upload_step["with"]["path"].splitlines() if line.strip()]
    check("native upload keeps target/ as the ZIP root", all(path.startswith("target/") for path in upload_paths),
          f"paths={upload_paths}")

    staging_cases = [
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
    ]
    download_paths = {}
    for job_key in ("desktop", "server"):
        step = find_native_download_step(release, job_key)
        path = str((step.get("with") or {}).get("path", ""))
        download_paths[job_key] = path
        check(f"native download stages outside target/ ({job_key})",
              bool(path) and not path.startswith("target/"), f"path={path!r}")
    for job_key, step_part, values, staged, placed in staging_cases:
        ok, detail = exercise_staging(release, job_key, step_part, values, staged, placed, download_paths[job_key])
        check(f"staging {step_part} ({values})", ok, detail)

    for workflow, label in ((ci, "ci.yml"), (release, "release.yml")):
        offenders = []
        for job_id, job in workflow["jobs"].items():
            runners = [entry.get("runner", "") for entry in matrix_entries(job)]
            if not any("windows" in runner for runner in runners):
                continue
            for step in job.get("steps") or []:
                script = step.get("run")
                if script and "if [" in script and step.get("shell") != "bash":
                    offenders.append(f"{job_id}/{step.get('name')}")
        check(f"{label} windows bash-syntax steps declare shell: bash", not offenders, f"offenders={offenders}")

    return results


def mutate_upload_name_order(ci, release):
    for step in ci["jobs"]["native"]["steps"]:
        if str(step.get("uses", "")).startswith("actions/upload-artifact"):
            step["with"]["name"] = "native-${{ matrix.kind }}-${{ matrix.os }}-${{ matrix.arch }}"


def mutate_native_download_path(ci, release):
    for job_key in ("desktop", "server"):
        step = find_native_download_step(release, job_key)
        step["with"]["path"] = "target/go-build"


def mutate_precondition_if(ci, release):
    release["jobs"]["precondition"].pop("if", None)


def mutate_windows_shell(ci, release):
    for step in release["jobs"]["desktop"]["steps"]:
        if step.get("name") == "Install the pinned Wails CLI":
            step.pop("shell", None)


NEGATIVE_CONTROLS = [
    ("round-1 upload name order regression is caught", mutate_upload_name_order,
     "resolver required artifacts are all produced by CI"),
    ("round-1 native download path regression is caught", mutate_native_download_path,
     "native download stages outside target/ (desktop)"),
    ("precondition if removal is caught", mutate_precondition_if,
     "job graph: reuse: ci skipped and release proceeds"),
    ("windows shell removal is caught", mutate_windows_shell,
     "release.yml windows bash-syntax steps declare shell: bash"),
]


def main():
    results = run_checks(CI, RELEASE, RESOLVER_TEXT)
    for name, ok, detail in results:
        print(f"{'ok' if ok else 'FAIL'} - {name}" + (f": {detail}" if not ok else ""))
    failures = [f"{name}: {detail}" for name, ok, detail in results if not ok]
    for label, mutate, expected in NEGATIVE_CONTROLS:
        ci_copy = copy.deepcopy(CI)
        release_copy = copy.deepcopy(RELEASE)
        mutate(ci_copy, release_copy)
        caught = any(expected in name and not ok for name, ok, _ in run_checks(ci_copy, release_copy, RESOLVER_TEXT))
        print(f"{'ok' if caught else 'FAIL'} - negative control: {label}")
        if not caught:
            failures.append(f"negative control: {label}: mutation not detected by {expected!r}")
    if failures:
        print(f"\n{len(failures)} check(s) failed")
        sys.exit(1)
    print(f"\nall {len(results) + len(NEGATIVE_CONTROLS)} workflow reuse wiring checks passed")


if __name__ == "__main__":
    main()
