#!/usr/bin/env python3
import copy
import hashlib
import json
import os
import re
import shlex
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
CI = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
RELEASE = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())
RESOLVER_TEXT = (ROOT / ".github/scripts/resolve-ci-reuse.sh").read_text()
PACK_TEXT = (ROOT / "scripts/pack-linux-server.sh").read_text()


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


def ci_run_document(status, conclusion, run_id=424242):
    return {
        "id": run_id,
        "name": "CI",
        "path": ".github/workflows/ci.yml",
        "status": status,
        "conclusion": conclusion,
        "created_at": "2026-10-04T00:00:00Z",
    }


def runs_reply(*runs):
    return json.dumps({"workflow_runs": list(runs)})


def run_resolver(resolver_text, stub_dir, runs_replies, artifacts_replies, extra_env=None):
    if isinstance(runs_replies, str):
        runs_replies = [runs_replies]
    if isinstance(artifacts_replies, str):
        artifacts_replies = [artifacts_replies]
    stub = stub_dir / "gh"
    stub.write_text(
        "#!/usr/bin/env bash\n"
        "dir=\"$(cd \"$(dirname \"${BASH_SOURCE[0]}\")\" && pwd)\"\n"
        "case \"$2\" in\n"
        f"  *artifacts*) n_file=\"$dir/.artifacts-count\"; replies=({' '.join(shlex.quote(reply) for reply in artifacts_replies)}) ;;\n"
        f"  *) n_file=\"$dir/.runs-count\"; replies=({' '.join(shlex.quote(reply) for reply in runs_replies)}) ;;\n"
        "esac\n"
        "n=0\n"
        "[ -f \"$n_file\" ] && n=\"$(cat \"$n_file\")\"\n"
        "printf '%s' \"$((n+1))\" > \"$n_file\"\n"
        "idx=$(( n < ${#replies[@]} ? n : ${#replies[@]} - 1 ))\n"
        "printf '%s' \"${replies[$idx]}\"\n"
    )
    stub.chmod(0o755)
    for name in (".runs-count", ".artifacts-count"):
        (stub_dir / name).unlink(missing_ok=True)
    resolver = stub_dir / "resolve-ci-reuse.sh"
    resolver.write_text(resolver_text)
    env = {
        "PATH": f"{stub_dir}:{os.environ['PATH']}",
        "GITHUB_REPOSITORY": "stub/repo",
        "GITHUB_SHA": "stubsha",
        "GITHUB_RUN_ID": "999",
        "GH_TOKEN": "stub-token",
    }
    env.update(extra_env or {})
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


def exercise_e2e_staging(release, values, download_path, report_text, staged_files, binary_bytes, manifest_sha):
    step = find_step(release["jobs"]["server"], "Stage and verify the reused CI e2e report")
    script = matrix_substitute(step["run"], values)
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        binary = base / "target/go-build/nexterm-server-linux-amd64"
        binary.parent.mkdir(parents=True, exist_ok=True)
        binary.write_bytes(binary_bytes)
        manifest = base / "target/go-build/nexterm-server-linux-amd64.manifest.json"
        manifest.write_text(json.dumps({"artifact": {"sha256": manifest_sha}}))
        if report_text is not None:
            report = base / download_path / "e2e-sync/report.json"
            report.parent.mkdir(parents=True, exist_ok=True)
            report.write_text(report_text)
        for relative in staged_files:
            target = base / download_path / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(f"stub {relative}\n")
        result = subprocess.run(
            ["bash", "-euo", "pipefail", "-c", script], cwd=base, capture_output=True, text=True, check=False
        )
        if result.returncode != 0:
            return False, f"exit {result.returncode}: {result.stderr}"
        placed = [base / "target/e2e-sync/report.json"] + [base / "target/e2e-sync" / Path(relative).name for relative in staged_files]
        missing = [str(path) for path in placed if not path.exists()]
        if missing:
            return False, f"missing staged files: {missing}"
        return True, ""


def normalized_step_if(step):
    return str(step.get("if", "")).replace(" ", "")


def run_checks(ci, release, resolver_text, pack_text):
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

    quality_steps = ci["jobs"]["quality"]["steps"]
    typecheck_offenders = [str(step.get("name")) for step in quality_steps if "pnpm typecheck" in str(step.get("run", ""))]
    check("quality has no standalone pnpm typecheck step", not typecheck_offenders, f"offenders={typecheck_offenders}")
    repro_steps = [step for step in quality_steps if "build.mjs frontend --repro-check" in str(step.get("run", ""))]
    check("quality runs the reproducible frontend build that carries tsc", len(repro_steps) == 1,
          f"count={len(repro_steps)}")
    go_test_steps = [step for step in quality_steps
                     if str(step.get("run", "")).strip().startswith("go test ") and "-tags" not in str(step.get("run", ""))]
    check("quality runs the Go suite once with -race",
          len(go_test_steps) == 1 and "-race" in go_test_steps[0]["run"] and "./..." in go_test_steps[0]["run"],
          f"steps={[str(step.get('name')) for step in go_test_steps]}")

    pack_report_lines = [line for line in pack_text.splitlines()
                         if re.search(r"node scripts/build\.mjs report\b", line)]
    check("pack generates the server archive evidence report exactly once",
          len(pack_report_lines) == 1 and "--kind=server-archive" in pack_text,
          f"lines={pack_report_lines}")
    check("pack report keeps --require-evidence enforcement",
          "report_args+=(--require-evidence)" in pack_text and 'if [ "$REQUIRE_EVIDENCE" -ne 0 ]' in pack_text,
          "missing the conditional --require-evidence append")

    server_job = release["jobs"]["server"]
    e2e_stage_step = find_step(server_job, "Stage and verify the reused CI e2e report")
    check("server stages the reused CI e2e report only on reuse-hit",
          normalized_step_if(e2e_stage_step) == "needs.ci-source.outputs.reused=='true'",
          f"if={e2e_stage_step.get('if')!r}")
    e2e_exercise_step = find_step(server_job, "Exercise the packaged server before archiving")
    check("server reruns e2e only without reuse",
          normalized_step_if(e2e_exercise_step) == "needs.ci-source.outputs.reused!='true'",
          f"if={e2e_exercise_step.get('if')!r}")

    publish_step = find_step(release["jobs"]["publish"], "Create or update the draft release")
    check("release upload runs with parallelism 8",
          "xargs -0 -P 8 -n 1 gh release upload" in str(publish_step.get("run", "")),
          "missing the -P 8 upload parallelism")

    desktop_steps = release["jobs"]["desktop"]["steps"]
    cache_index = next((index for index, step in enumerate(desktop_steps) if step.get("id") == "wails-cache"), None)
    setup_go_index = next((index for index, step in enumerate(desktop_steps)
                           if str(step.get("uses", "")).startswith("actions/setup-go")), None)
    check("windows wails cache restores before setup-go",
          cache_index is not None and setup_go_index is not None and cache_index < setup_go_index,
          f"cache_index={cache_index} setup_go_index={setup_go_index}")
    setup_go_if = str(desktop_steps[setup_go_index].get("if", "")) if setup_go_index is not None else ""
    check("setup-go is skipped on a verified wails cache hit",
          "steps.wails-cache.outputs.cache-hit" in setup_go_if, f"if={setup_go_if!r}")
    wails_install_step = find_step(release["jobs"]["desktop"], "Install the pinned Wails CLI")
    check("wails install step does not require go on PATH",
          "go env GOPATH" not in str(wails_install_step.get("run", "")) and "$HOME/go/bin" in str(wails_install_step.get("run", "")),
          "install step still resolves GOPATH through go")

    full_names = sorted(resolver_required_artifacts(resolver_text))
    green = runs_reply(ci_run_document("completed", "success"))
    in_progress = runs_reply(ci_run_document("in_progress", None))
    wait_env = {"CI_REUSE_WAIT_TIMEOUT": "30", "CI_REUSE_POLL_INTERVAL": "1"}
    with tempfile.TemporaryDirectory() as stub_dir:
        stub = Path(stub_dir)
        result = run_resolver(resolver_text, stub, green, json.dumps(full_names))
        check("resolver full set reuses the green run",
              result.returncode == 0 and "reused=true" in result.stdout and "artifact-run-id=424242" in result.stdout,
              f"stdout={result.stdout!r} stderr={result.stderr!r}")
        for missing in full_names:
            partial = json.dumps([name for name in full_names if name != missing])
            result = run_resolver(resolver_text, stub, green, partial)
            ok = result.returncode == 0 and "reused=false" in result.stdout and "artifact-run-id=999" in result.stdout
            check(f"resolver falls back when {missing} is missing", ok, f"stdout={result.stdout!r}")
        result = run_resolver(resolver_text, stub, runs_reply(), json.dumps(full_names))
        check("resolver falls back when no green run exists",
              result.returncode == 0 and "reused=false" in result.stdout and "artifact-run-id=999" in result.stdout,
              f"stdout={result.stdout!r}")
        result = run_resolver(resolver_text, stub, [in_progress, green], json.dumps(full_names), wait_env)
        check("resolver waits for an in-progress run and reuses its green completion",
              result.returncode == 0 and "reused=true" in result.stdout and "artifact-run-id=424242" in result.stdout
              and "still in progress" in result.stderr,
              f"stdout={result.stdout!r} stderr={result.stderr!r}")
        result = run_resolver(resolver_text, stub, in_progress, json.dumps(full_names),
                              {"CI_REUSE_WAIT_TIMEOUT": "2", "CI_REUSE_POLL_INTERVAL": "1"})
        check("resolver times out waiting and falls back to the full CI",
              result.returncode == 0 and "reused=false" in result.stdout and "artifact-run-id=999" in result.stdout
              and "timed out" in result.stderr and "reused=true" not in result.stdout,
              f"stdout={result.stdout!r} stderr={result.stderr!r}")
        for conclusion in ("failure", "cancelled"):
            completed = runs_reply(ci_run_document("completed", conclusion))
            result = run_resolver(resolver_text, stub, [in_progress, completed], json.dumps(full_names), wait_env)
            check(f"resolver falls back when the waited run ends as {conclusion}",
                  result.returncode == 0 and "reused=false" in result.stdout and "artifact-run-id=999" in result.stdout
                  and f"conclusion {conclusion}" in result.stderr and "reused=true" not in result.stdout,
                  f"stdout={result.stdout!r} stderr={result.stderr!r}")
        partial = json.dumps([name for name in full_names if name != "frontend-dist"])
        result = run_resolver(resolver_text, stub, [in_progress, green], partial, wait_env)
        check("resolver falls back when the waited green run lacks artifacts",
              result.returncode == 0 and "reused=false" in result.stdout and "artifact-run-id=999" in result.stdout
              and "missing reusable artifacts" in result.stderr,
              f"stdout={result.stdout!r} stderr={result.stderr!r}")

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

    binary_bytes = b"stub server binary bytes\n"
    binary_sha = hashlib.sha256(binary_bytes).hexdigest()
    passed_report = json.dumps({"schema_version": 1, "mode": "go-self-consistency", "status": "passed",
                                "passed": 20, "failed": 0, "assertions": {"passed": ["a"], "failed": []}})
    failed_report = json.dumps({"schema_version": 1, "mode": "go-self-consistency", "status": "failed",
                                "passed": 18, "failed": 2, "assertions": {"passed": ["a"], "failed": ["b", "c"]}})
    server_download_path = download_paths["server"]
    e2e_cases = [
        ("e2e report staging passes a clean report bound to the binary manifest",
         {"arch": "amd64"}, passed_report, ["e2e-sync/e2e-a.log", "e2e-sync/e2e-b.log"], binary_sha, True),
        ("e2e report staging rejects a failed report",
         {"arch": "amd64"}, failed_report, ["e2e-sync/e2e-a.log"], binary_sha, False),
        ("e2e report staging rejects a binary that differs from the manifest",
         {"arch": "amd64"}, passed_report, ["e2e-sync/e2e-a.log"], "0" * 64, False),
        ("e2e report staging rejects a missing report",
         {"arch": "amd64"}, None, ["e2e-sync/e2e-a.log"], binary_sha, False),
    ]
    for name, values, report_text, staged_files, manifest_sha, expected_ok in e2e_cases:
        ok, detail = exercise_e2e_staging(release, values, server_download_path, report_text, staged_files, binary_bytes, manifest_sha)
        check(name, ok == expected_ok, "" if ok == expected_ok else f"expected_ok={expected_ok} actual_ok={ok}: {detail}")

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


def mutate_upload_name_order(ci, release, pack_text):
    for step in ci["jobs"]["native"]["steps"]:
        if str(step.get("uses", "")).startswith("actions/upload-artifact"):
            step["with"]["name"] = "native-${{ matrix.kind }}-${{ matrix.os }}-${{ matrix.arch }}"


def mutate_native_download_path(ci, release, pack_text):
    for job_key in ("desktop", "server"):
        step = find_native_download_step(release, job_key)
        step["with"]["path"] = "target/go-build"


def mutate_precondition_if(ci, release, pack_text):
    release["jobs"]["precondition"].pop("if", None)


def mutate_windows_shell(ci, release, pack_text):
    for step in release["jobs"]["desktop"]["steps"]:
        if step.get("name") == "Install the pinned Wails CLI":
            step.pop("shell", None)


def mutate_readd_typecheck(ci, release, pack_text):
    steps = ci["jobs"]["quality"]["steps"]
    for index, step in enumerate(steps):
        if str(step.get("name", "")).startswith("Frontend lint and tests"):
            steps.insert(index, {"name": "Frontend typecheck", "run": "pnpm typecheck"})
            return


def mutate_readd_plain_go_test(ci, release, pack_text):
    steps = ci["jobs"]["quality"]["steps"]
    for index, step in enumerate(steps):
        if str(step.get("name", "")).startswith("Go tests"):
            steps.insert(index, {"name": "Go tests", "run": "go test -mod=readonly ./..."})
            return


def mutate_duplicate_pack_report(ci, release, pack_text):
    return pack_text.replace(
        "(cd \"$ROOT\" && node scripts/build.mjs report \"${report_args[@]}\")",
        "(cd \"$ROOT\" && node scripts/build.mjs report \"${report_args[@]}\")\n"
        "(cd \"$ROOT\" && node scripts/build.mjs report --kind=server-archive --flavor=full --os=linux \"--arch=$ARCH\" \"--file=$ARTIFACT\")",
    )


def mutate_e2e_stage_if(ci, release, pack_text):
    find_step(release["jobs"]["server"], "Stage and verify the reused CI e2e report").pop("if", None)


def mutate_e2e_exercise_if(ci, release, pack_text):
    find_step(release["jobs"]["server"], "Exercise the packaged server before archiving").pop("if", None)


def mutate_upload_parallelism(ci, release, pack_text):
    step = find_step(release["jobs"]["publish"], "Create or update the draft release")
    step["run"] = str(step["run"]).replace("-P 8", "-P 4")


def mutate_wails_cache_order(ci, release, pack_text):
    steps = release["jobs"]["desktop"]["steps"]
    index = next(i for i, step in enumerate(steps) if step.get("id") == "wails-cache")
    step = steps.pop(index)
    setup_go_index = next(i for i, s in enumerate(steps) if str(s.get("uses", "")).startswith("actions/setup-go"))
    steps.insert(setup_go_index + 1, step)


def mutate_setup_go_skip(ci, release, pack_text):
    for step in release["jobs"]["desktop"]["steps"]:
        if str(step.get("uses", "")).startswith("actions/setup-go"):
            step.pop("if", None)


NEGATIVE_CONTROLS = [
    ("round-1 upload name order regression is caught", mutate_upload_name_order,
     "resolver required artifacts are all produced by CI"),
    ("round-1 native download path regression is caught", mutate_native_download_path,
     "native download stages outside target/ (desktop)"),
    ("precondition if removal is caught", mutate_precondition_if,
     "job graph: reuse: ci skipped and release proceeds"),
    ("windows shell removal is caught", mutate_windows_shell,
     "release.yml windows bash-syntax steps declare shell: bash"),
    ("standalone pnpm typecheck re-addition is caught", mutate_readd_typecheck,
     "quality has no standalone pnpm typecheck step"),
    ("plain go test re-addition is caught", mutate_readd_plain_go_test,
     "quality runs the Go suite once with -race"),
    ("duplicate pack evidence report is caught", mutate_duplicate_pack_report,
     "pack generates the server archive evidence report exactly once"),
    ("e2e staging if removal is caught", mutate_e2e_stage_if,
     "server stages the reused CI e2e report only on reuse-hit"),
    ("e2e exercise if removal is caught", mutate_e2e_exercise_if,
     "server reruns e2e only without reuse"),
    ("upload parallelism regression is caught", mutate_upload_parallelism,
     "release upload runs with parallelism 8"),
    ("wails cache order regression is caught", mutate_wails_cache_order,
     "windows wails cache restores before setup-go"),
    ("setup-go skip removal is caught", mutate_setup_go_skip,
     "setup-go is skipped on a verified wails cache hit"),
]


def main():
    results = run_checks(CI, RELEASE, RESOLVER_TEXT, PACK_TEXT)
    for name, ok, detail in results:
        print(f"{'ok' if ok else 'FAIL'} - {name}" + (f": {detail}" if not ok else ""))
    failures = [f"{name}: {detail}" for name, ok, detail in results if not ok]
    for label, mutate, expected in NEGATIVE_CONTROLS:
        ci_copy = copy.deepcopy(CI)
        release_copy = copy.deepcopy(RELEASE)
        pack_copy = mutate(ci_copy, release_copy, PACK_TEXT) or PACK_TEXT
        caught = any(expected in name and not ok for name, ok, _ in run_checks(ci_copy, release_copy, RESOLVER_TEXT, pack_copy))
        print(f"{'ok' if caught else 'FAIL'} - negative control: {label}")
        if not caught:
            failures.append(f"negative control: {label}: mutation not detected by {expected!r}")
    if failures:
        print(f"\n{len(failures)} check(s) failed")
        sys.exit(1)
    print(f"\nall {len(results) + len(NEGATIVE_CONTROLS)} workflow reuse wiring checks passed")


if __name__ == "__main__":
    main()
