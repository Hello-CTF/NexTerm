#!/usr/bin/env python3
import copy
import hashlib
import json
import os
import platform
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
CI = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
RELEASE = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())
RESOLVER_TEXT = (ROOT / ".github/scripts/resolve-ci-reuse.sh").read_text()
PACK_TEXT = (ROOT / "scripts/pack-linux-server.sh").read_text()
UPLOAD_TEXT = (ROOT / ".github/scripts/release-upload.mjs").read_text()
UPLOAD_TEST = ROOT / ".github/scripts/release-upload.test.mjs"
HOST_GOOS = {"Darwin": "darwin", "Linux": "linux"}.get(platform.system())
HOST_GOARCH = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}.get(platform.machine())


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


def job_needs(job):
    needs = job.get("needs")
    return [needs] if isinstance(needs, str) else list(needs or [])


def evaluate_release(release, scenario):
    results = {"ci-source": scenario["ci-source"]}
    outputs = {"reused": scenario["reused"]} if scenario["ci-source"] == "success" else {}
    order = ("ci", "precondition", "desktop", "server", "publish")
    ancestors = {"ci-source": set()}
    for job_id in order:
        closure = set()
        for need in job_needs(release["jobs"][job_id]):
            closure.add(need)
            closure |= ancestors[need]
        ancestors[job_id] = closure
    status_functions = ("success(", "failure(", "cancelled(", "always(", "skipped(")
    for job_id in order:
        condition = release["jobs"][job_id].get("if")
        expr = condition.replace("${{", "").replace("}}", "").strip() if condition is not None else None
        upstream = [results[ancestor] for ancestor in ancestors[job_id]]
        has_status_check = expr is not None and any(fn in expr for fn in status_functions)
        if has_status_check:
            if expr != "!failure() && !cancelled()":
                raise AssertionError(f"unhandled job-level if: {expr}")
            runs = "failure" not in upstream and "cancelled" not in upstream
        else:
            runs = all(result == "success" for result in upstream)
            if expr is not None:
                if expr != "needs.ci-source.outputs.reused != 'true'":
                    raise AssertionError(f"unhandled job-level if: {expr}")
                runs = runs and outputs.get("reused") != "true"
        results[job_id] = scenario.get(job_id, "success") if runs else "skipped"
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


def run_wiring_checks(ci, release, resolver_text, pack_text):
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
        ("reuse: successful ci-source + skipped ci + successful precondition proceeds to packaging and publish",
         {"ci-source": "success", "reused": "true", "ci": "success"},
         {"ci-source": "success", "ci": "skipped", "precondition": "success", "desktop": "success", "server": "success", "publish": "success"}),
        ("fallback: full CI success and release proceeds",
         {"ci-source": "success", "reused": "false", "ci": "success"},
         {"ci-source": "success", "ci": "success", "precondition": "success", "desktop": "success", "server": "success", "publish": "success"}),
        ("fallback: CI failure gates the release",
         {"ci-source": "success", "reused": "false", "ci": "failure"},
         {"ci-source": "success", "ci": "failure", "precondition": "skipped", "desktop": "skipped", "server": "skipped", "publish": "skipped"}),
        ("reuse: desktop packaging failure still gates publish",
         {"ci-source": "success", "reused": "true", "desktop": "failure"},
         {"ci-source": "success", "ci": "skipped", "precondition": "success", "desktop": "failure", "server": "success", "publish": "skipped"}),
        ("reuse: server packaging failure still gates publish",
         {"ci-source": "success", "reused": "true", "server": "failure"},
         {"ci-source": "success", "ci": "skipped", "precondition": "success", "desktop": "success", "server": "failure", "publish": "skipped"}),
        ("ci-source failure gates the release",
         {"ci-source": "failure", "reused": "false", "ci": "success"},
         {"ci-source": "failure", "ci": "skipped", "precondition": "skipped", "desktop": "skipped", "server": "skipped", "publish": "skipped"}),
        ("ci-source cancellation gates the release",
         {"ci-source": "cancelled", "reused": "false", "ci": "success"},
         {"ci-source": "cancelled", "ci": "skipped", "precondition": "skipped", "desktop": "skipped", "server": "skipped", "publish": "skipped"}),
    ]
    for name, scenario, expected in scenarios:
        actual = evaluate_release(release, scenario)
        check(f"job graph: {name}", actual == expected, f"actual={actual}")

    for job_key in ("desktop", "server", "publish"):
        job_if = str(release["jobs"][job_key].get("if", "")).replace(" ", "")
        check(f"{job_key} continues past a skipped reused CI without hiding failures",
              job_if == "${{!failure()&&!cancelled()}}", f"if={release['jobs'][job_key].get('if')!r}")

    quality_steps = ci["jobs"]["quality"]["steps"]
    typecheck_offenders = [str(step.get("name")) for step in quality_steps if "pnpm typecheck" in str(step.get("run", ""))]
    check("quality has no standalone pnpm typecheck step", not typecheck_offenders, f"offenders={typecheck_offenders}")
    repro_steps = [step for step in quality_steps if "build.mjs frontend --repro-check" in str(step.get("run", ""))]
    check("quality runs the reproducible frontend build that carries tsc", len(repro_steps) == 1,
          f"count={len(repro_steps)}")
    go_test_steps = [step for step in quality_steps
                     if str(step.get("run", "")).strip().startswith("go test ") and "-tags" not in str(step.get("run", ""))]
    check("quality runs the Go suite once without -race",
          len(go_test_steps) == 1 and "-race" not in go_test_steps[0]["run"] and "./..." in go_test_steps[0]["run"],
          f"steps={[str(step.get('name')) for step in go_test_steps]}")
    production_suite_steps = [str(step.get("name")) for step in quality_steps if "-tags production" in str(step.get("run", ""))]
    check("quality runs no production-tagged Go suite", not production_suite_steps, f"steps={production_suite_steps}")
    harness_test_steps = [str(step.get("name")) for step in quality_steps if "node --test" in str(step.get("run", ""))]
    check("quality runs no build harness unit tests", not harness_test_steps, f"steps={harness_test_steps}")
    removed_jobs = sorted({"browser", "windows-supervisor", "ssh-windows"} & set(ci["jobs"]))
    check("ci.yml drops the browser acceptance and standalone windows native jobs", not removed_jobs, f"jobs={removed_jobs}")

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
    publish_run = str(publish_step.get("run", ""))
    check("release publish uploads through release-upload.mjs",
          "node .github/scripts/release-upload.mjs candidate" in publish_run
          and "gh release upload" not in publish_run and "xargs" not in publish_run,
          f"run={publish_run!r}")
    publish_env = publish_step.get("env") or {}
    check("release publish passes the token and repository to the uploader",
          "GH_TOKEN" in publish_env and "GITHUB_REPOSITORY" in publish_env,
          f"env={sorted(publish_env)}")
    evidence_text = (ROOT / ".github/scripts/release-evidence.mjs").read_text()
    asset_templates = [
        "NexTerm_${version}_x64-setup.exe",
        "NexTerm_${version}_arm64-setup.exe",
        "NexTerm_${version}_aarch64.dmg",
        "NexTerm_${version}_x86_64.dmg",
        "NexTerm-desktop_${version}_linux_${arch}.tar.gz",
        "NexTerm-server_${version}_linux_${arch}.tar.gz",
    ]
    drifted = [template for template in asset_templates
               if template not in evidence_text or template not in UPLOAD_TEXT]
    check("release-upload expects the same 8 packages as the evidence gate", not drifted,
          f"drifted={drifted}")
    missing_evidence = [name for name in ("SHA256SUMS", "release-evidence.json", "real-target-gaps.json")
                        if name not in UPLOAD_TEXT]
    check("release-upload expects the 3 evidence files", not missing_evidence,
          f"missing={missing_evidence}")
    for env_name in ("GITHUB_REF_NAME", "GITHUB_REPOSITORY", "GH_TOKEN", "GITHUB_API_URL"):
        check(f"release-upload.mjs reads {env_name}", env_name in UPLOAD_TEXT)
    check("release-upload resolves releases through the list API, not the draft-blind tags endpoint",
          "releases/tags/" not in UPLOAD_TEXT and "releases?per_page=" in UPLOAD_TEXT,
          "drafts 404 on the tags endpoint; the uploader must discover them via the paginated releases list")

    desktop_steps = release["jobs"]["desktop"]["steps"]
    cache_index = next((index for index, step in enumerate(desktop_steps) if step.get("id") == "wails-cache"), None)
    setup_go_index = next((index for index, step in enumerate(desktop_steps)
                           if str(step.get("uses", "")).startswith("actions/setup-go")), None)
    check("windows wails cache restores before setup-go",
          cache_index is not None and setup_go_index is not None and cache_index < setup_go_index,
          f"cache_index={cache_index} setup_go_index={setup_go_index}")
    setup_go_condition = str(desktop_steps[setup_go_index].get("if", "")) if setup_go_index is not None else ""
    check("desktop setup-go is never skipped by the wails cache hit",
          "wails-cache" not in setup_go_condition, f"if={setup_go_condition!r}")

    upload_step = find_step(ci["jobs"]["native"], "Upload native runtime")
    upload_paths = [line.strip() for line in upload_step["with"]["path"].splitlines() if line.strip()]
    check("native upload keeps target/ as the ZIP root", all(path.startswith("target/") for path in upload_paths),
          f"paths={upload_paths}")
    smoke_steps = [str(step.get("name")) for step in ci["jobs"]["native"]["steps"]
                   if "--smoke" in str(step.get("run", ""))]
    check("native job builds and runs no smoke desktop binary", not smoke_steps, f"steps={smoke_steps}")
    check("native upload carries no desktop smoke evidence", all("desktop-smoke" not in path for path in upload_paths),
          f"paths={upload_paths}")
    native_step_names = [str(step.get("name", "")) for step in ci["jobs"]["native"]["steps"]]
    check("native job keeps the repro-checked production build",
          any("Build the repro-checked production binary" in name for name in native_step_names))
    check("native job keeps the two-process server acceptance",
          any("Run Go two-process server acceptance" in name for name in native_step_names))

    native_steps = ci["jobs"]["native"]["steps"]
    dist_download = next((step for step in native_steps
                          if str(step.get("uses", "")).startswith("actions/download-artifact")
                          and str((step.get("with") or {}).get("name", "")) == "frontend-dist"), None)
    check("native job downloads the verified frontend dist for every desktop build",
          dist_download is not None and normalized_step_if(dist_download) == "matrix.kind=='desktop'",
          f"step={dist_download!r}")
    dist_download_path = str((dist_download.get("with") or {}).get("path", "")) if dist_download else ""
    check("native frontend dist downloads outside target/",
          bool(dist_download_path) and not dist_download_path.startswith("target/"),
          f"path={dist_download_path!r}")
    build_step = find_step(ci["jobs"]["native"], "Build the repro-checked production binary")
    check("native frontend dist download precedes the production build",
          dist_download is not None and native_steps.index(dist_download) < native_steps.index(build_step),
          "the dist download must run before the build that consumes it")
    desktop_build_run = matrix_substitute(str(build_step["run"]), {"kind": "desktop", "os": "linux", "arch": "amd64"})
    server_build_run = matrix_substitute(str(build_step["run"]), {"kind": "server", "os": "linux", "arch": "amd64"})
    check("desktop production build consumes the downloaded verified dist",
          bool(dist_download_path)
          and f"--consume-dist={dist_download_path}/dist" in desktop_build_run
          and f"--dist-manifest={dist_download_path}/target/dist-manifest.json" in desktop_build_run,
          f"run={desktop_build_run!r}")
    check("desktop production build does not skip the frontend without a staged dist",
          "--skip-frontend" not in desktop_build_run, f"run={desktop_build_run!r}")
    check("server production build stays free of frontend dist handling",
          "--consume-dist" not in server_build_run and "--skip-frontend" not in server_build_run,
          f"run={server_build_run!r}")
    apt_install_runs = [str(step.get("run", "")) for step in native_steps if "apt-get install" in str(step.get("run", ""))]
    check("native job installs no virtual display for removed smoke runs",
          all("xvfb" not in run for run in apt_install_runs), f"runs={apt_install_runs}")

    staging_cases = [
        ("desktop", "Stage the reused production binary", {"os": "windows", "arch": "amd64"},
         ["go-build/nexterm-desktop-windows-amd64.exe", "go-build/nexterm-desktop-windows-amd64.exe.manifest.json",
          "go-build/nexterm-desktop-windows-amd64.exe.artifact.json",
          "e2e-sync/report.json"],
         ["go-build/nexterm-desktop-windows-amd64.exe", "go-build/nexterm-desktop-windows-amd64.exe.manifest.json"]),
        ("desktop", "Stage the reused production binary", {"os": "darwin", "arch": "arm64"},
         ["go-build/nexterm-desktop-darwin-arm64", "go-build/nexterm-desktop-darwin-arm64.manifest.json",
          "go-build/nexterm-desktop-darwin-arm64.artifact.json"],
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


def run_resolver_checks(resolver_text):
    results = []

    def check(name, ok, detail=""):
        results.append((name, ok, detail))

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
    return results


def path_without_go():
    return os.pathsep.join(
        entry for entry in os.environ.get("PATH", "").split(os.pathsep)
        if entry and not (Path(entry) / "go").exists()
    )


def build_stub_desktop_binary(directory):
    (directory / "main.go").write_text("package main\n\nfunc main() {}\n")
    subprocess.run(["go", "mod", "init", "stub"], cwd=directory, capture_output=True, check=True)
    env = dict(os.environ)
    env["CGO_ENABLED"] = "1"
    subprocess.run(["go", "build", "-ldflags", "-s -w", "-o", "stub-desktop", "."],
                   cwd=directory, env=env, capture_output=True, check=True)
    return directory / "stub-desktop"


def run_evidence_checks(release):
    results = []

    def check(name, ok, detail=""):
        results.append((name, ok, detail))

    install_step = find_step(release["jobs"]["desktop"], "Install the pinned Wails CLI")
    script = str(install_step["run"]).replace("${{ steps.wails-cache.outputs.cache-hit }}", "true")
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        home = base / "home"
        wails_dir = home / "go/bin"
        wails_dir.mkdir(parents=True)
        wails = wails_dir / "wails3"
        wails.write_text("#!/usr/bin/env bash\necho wails3 v3.0.0-beta.27\n")
        wails.chmod(0o755)
        (base / "ghpath").write_text("")
        env = {"HOME": str(home), "PATH": "/usr/bin:/bin", "GITHUB_PATH": str(base / "ghpath")}
        hidden = subprocess.run(["bash", "-euo", "pipefail", "-c", script], cwd=base, env=env,
                                capture_output=True, text=True, check=False)
        check("wails3 stays invisible to the install step when only GITHUB_PATH is extended",
              hidden.returncode != 0, f"rc={hidden.returncode} stdout={hidden.stdout!r} stderr={hidden.stderr!r}")
        env["PATH"] = f"{wails_dir}:/usr/bin:/bin"
        visible = subprocess.run(["bash", "-euo", "pipefail", "-c", script], cwd=base, env=env,
                                 capture_output=True, text=True, check=False)
        check("install step passes when the restored wails bin dir is already on PATH",
              visible.returncode == 0 and "wails3 v3.0.0-beta.27" in visible.stdout,
              f"rc={visible.returncode} stderr={visible.stderr!r}")

    build_text = (ROOT / "scripts/build.mjs").read_text()
    check("embedded dist assertions stay wired into the desktop release report",
          'requireEmbedded: kind === "desktop" && release' in build_text
          and 'assertion("embedded-index"' in build_text and 'assertion("embedded-script"' in build_text,
          "build.mjs no longer wires embedded dist assertions into the desktop release report")

    if HOST_GOOS is None or HOST_GOARCH is None:
        check("desktop evidence checks require a darwin/linux amd64/arm64 host", False,
              f"host={platform.system()}/{platform.machine()}")
        return results
    if shutil.which("go") is None:
        check("desktop evidence checks require a Go toolchain", False, "go is not on PATH")
        return results

    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        binary = build_stub_desktop_binary(base)
        digest = hashlib.sha256(binary.read_bytes()).hexdigest()
        head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
        version = json.loads((ROOT / "wails.json").read_text())["info"]["version"]
        cgo = "1"
        cli = ["node", str(ROOT / "scripts/build.mjs"), "report", "--kind=desktop",
               f"--os={HOST_GOOS}", f"--arch={HOST_GOARCH}", f"--cgo={cgo}",
               f"--file={binary}", "--require-evidence"]
        report_path = Path(f"{binary}.artifact.json")
        no_go_env = dict(os.environ)
        no_go_env["PATH"] = path_without_go()
        closed = subprocess.run(cli, cwd=ROOT, env=no_go_env, capture_output=True, text=True, check=False)
        closed_assertions = {}
        if report_path.exists():
            closed_assertions = {item["id"]: item["status"]
                                 for item in json.loads(report_path.read_text()).get("assertions", [])}
        check("desktop package-only evidence fails closed without go on PATH",
              closed.returncode != 0 and closed_assertions.get("cgo-boundary") == "failed",
              f"rc={closed.returncode} assertions={closed_assertions} stderr={closed.stderr[-300:]!r}")
        passing = subprocess.run(cli, cwd=ROOT, capture_output=True, text=True, check=False)
        passed_report = json.loads(report_path.read_text())
        assertions = {item["id"]: item["status"] for item in passed_report.get("assertions", [])}
        expected_assertions = ["binary-format", "binary-architecture", "cgo-boundary", "stripped-release"]
        check("desktop binary evidence assertions pass with the toolchain present",
              passing.returncode == 0
              and passed_report.get("status") == "passed"
              and all(assertions.get(name) == "passed" for name in expected_assertions)
              and passed_report.get("commit") == head
              and passed_report.get("artifact", {}).get("sha256") == digest,
              f"rc={passing.returncode} status={passed_report.get('status')} assertions={assertions} stderr={passing.stderr[-300:]!r}")

        manifest_path = Path(f"{binary}.manifest.json")

        def write_manifest(commit):
            manifest_path.write_text(json.dumps({
                "schema_version": 1,
                "kind": "go-binary",
                "id": f"desktop-{HOST_GOOS}-{HOST_GOARCH}",
                "platform": {"os": HOST_GOOS, "arch": HOST_GOARCH, "cgo": cgo},
                "tags": ["production"],
                "stripped": True,
                "version": version,
                "commit": commit,
                "source_date_epoch": 1,
                "artifact": {"size_bytes": binary.stat().st_size, "sha256": digest},
            }))

        verifier = ["node", str(ROOT / ".github/scripts/verify-reused-binary.mjs"), str(binary),
                    f"desktop-{HOST_GOOS}-{HOST_GOARCH}", HOST_GOOS, HOST_GOARCH, cgo]
        write_manifest("0" * 40)
        mismatch = subprocess.run(verifier, cwd=ROOT, capture_output=True, text=True, check=False)
        check("verify-reused-binary rejects a commit mismatch",
              mismatch.returncode != 0 and "commit" in mismatch.stderr,
              f"rc={mismatch.returncode} stderr={mismatch.stderr!r}")
        write_manifest(head)
        accepted = subprocess.run(verifier, cwd=ROOT, capture_output=True, text=True, check=False)
        check("verify-reused-binary accepts the byte-identical manifest",
              accepted.returncode == 0, f"rc={accepted.returncode} stderr={accepted.stderr!r}")
    return results


def run_upload_checks():
    results = []

    def check(name, ok, detail=""):
        results.append((name, ok, detail))

    if not UPLOAD_TEST.exists():
        check("release-upload stub tests exist", False, str(UPLOAD_TEST))
        return results
    result = subprocess.run(["node", "--test", str(UPLOAD_TEST)], cwd=ROOT,
                            capture_output=True, text=True, check=False)
    ok = result.returncode == 0
    check("release-upload stub tests pass", ok,
          "" if ok else f"rc={result.returncode} stdout tail={result.stdout[-400:]!r} stderr tail={result.stderr[-400:]!r}")
    return results


def run_checks(ci, release, resolver_text, pack_text, include_resolver=True, include_evidence=True):
    results = run_wiring_checks(ci, release, resolver_text, pack_text)
    if include_resolver:
        results.extend(run_resolver_checks(resolver_text))
    if include_evidence:
        results.extend(run_evidence_checks(release))
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


def mutate_packaging_gate_if(ci, release, pack_text):
    for job_key in ("desktop", "server", "publish"):
        release["jobs"][job_key].pop("if", None)


def mutate_publish_gate_if(ci, release, pack_text):
    release["jobs"]["publish"].pop("if", None)


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


def mutate_restore_race(ci, release, pack_text):
    for step in ci["jobs"]["quality"]["steps"]:
        if str(step.get("name", "")).startswith("Go tests"):
            step["run"] = str(step["run"]).replace("go test -mod=readonly", "go test -mod=readonly -race", 1)
            return


def mutate_readd_smoke_step(ci, release, pack_text):
    ci["jobs"]["native"]["steps"].insert(0, {
        "name": "Build the smoke desktop binary and run the native smoke",
        "if": "matrix.kind == 'desktop'",
        "run": "node scripts/build.mjs desktop --release --smoke --os=${{ matrix.os }} --arch=${{ matrix.arch }}",
    })


def mutate_remove_dist_download(ci, release, pack_text):
    steps = ci["jobs"]["native"]["steps"]
    index = next(i for i, step in enumerate(steps)
                 if str(step.get("uses", "")).startswith("actions/download-artifact")
                 and str((step.get("with") or {}).get("name", "")) == "frontend-dist")
    steps.pop(index)


def mutate_restore_skip_frontend(ci, release, pack_text):
    for step in ci["jobs"]["native"]["steps"]:
        if "Build the repro-checked production binary" in str(step.get("name", "")):
            step["run"] = re.sub(r"&& '--consume-dist=[^']*'", "&& '--skip-frontend'", str(step["run"]))


def mutate_readd_xvfb(ci, release, pack_text):
    for step in ci["jobs"]["native"]["steps"]:
        if "apt-get install" in str(step.get("run", "")):
            step["run"] = str(step["run"]).replace("libwebkitgtk-6.0-dev", "libwebkitgtk-6.0-dev xvfb")


def mutate_readd_production_go_test(ci, release, pack_text):
    steps = ci["jobs"]["quality"]["steps"]
    for index, step in enumerate(steps):
        if str(step.get("name", "")).startswith("Go self-consistency"):
            steps.insert(index, {"name": "Go production tests with embedded desktop assets",
                                 "run": "go test -mod=readonly -tags production ./..."})
            return


def mutate_readd_harness_tests(ci, release, pack_text):
    steps = ci["jobs"]["quality"]["steps"]
    for index, step in enumerate(steps):
        if str(step.get("name", "")).startswith("Wails bindings drift"):
            steps.insert(index, {"name": "Build harness unit tests", "run": "node --test scripts/lib/*.test.mjs"})
            return


def mutate_readd_windows_jobs(ci, release, pack_text):
    ci["jobs"]["ssh-windows"] = {"name": "Windows SSH native tests", "runs-on": "windows-latest",
                                 "steps": [{"uses": "actions/checkout@v7"}]}


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


def mutate_restore_xargs_upload(ci, release, pack_text):
    step = find_step(release["jobs"]["publish"], "Create or update the draft release")
    step["run"] = "printf '%s\\0' candidate/* | xargs -0 -P 8 -n 1 gh release upload \"$GITHUB_REF_NAME\" --clobber"


def mutate_wails_cache_order(ci, release, pack_text):
    steps = release["jobs"]["desktop"]["steps"]
    index = next(i for i, step in enumerate(steps) if step.get("id") == "wails-cache")
    step = steps.pop(index)
    setup_go_index = next(i for i, s in enumerate(steps) if str(s.get("uses", "")).startswith("actions/setup-go"))
    steps.insert(setup_go_index + 1, step)


def mutate_setup_go_cache_skip(ci, release, pack_text):
    for step in release["jobs"]["desktop"]["steps"]:
        if str(step.get("uses", "")).startswith("actions/setup-go"):
            step["if"] = "matrix.os != 'windows' || steps.wails-cache.outputs.cache-hit != 'true'"


NEGATIVE_CONTROLS = [
    ("round-1 upload name order regression is caught", mutate_upload_name_order,
     "resolver required artifacts are all produced by CI", False),
    ("round-1 native download path regression is caught", mutate_native_download_path,
     "native download stages outside target/ (desktop)", False),
    ("precondition if removal is caught", mutate_precondition_if,
     "job graph: reuse: successful ci-source", False),
    ("packaging gate if removal is caught", mutate_packaging_gate_if,
     "job graph: reuse: successful ci-source", False),
    ("publish gate if removal is caught", mutate_publish_gate_if,
     "job graph: reuse: successful ci-source", False),
    ("windows shell removal is caught", mutate_windows_shell,
     "release.yml windows bash-syntax steps declare shell: bash", False),
    ("standalone pnpm typecheck re-addition is caught", mutate_readd_typecheck,
     "quality has no standalone pnpm typecheck step", False),
    ("plain go test re-addition is caught", mutate_readd_plain_go_test,
     "quality runs the Go suite once without -race", False),
    ("race re-instrumentation of the quality suite is caught", mutate_restore_race,
     "quality runs the Go suite once without -race", False),
    ("smoke desktop step re-addition is caught", mutate_readd_smoke_step,
     "native job builds and runs no smoke desktop binary", False),
    ("frontend dist download removal is caught", mutate_remove_dist_download,
     "native job downloads the verified frontend dist for every desktop build", False),
    ("desktop skip-frontend restore is caught", mutate_restore_skip_frontend,
     "desktop production build consumes the downloaded verified dist", False),
    ("xvfb reintroduction is caught", mutate_readd_xvfb,
     "native job installs no virtual display for removed smoke runs", False),
    ("production-tagged Go suite re-addition is caught", mutate_readd_production_go_test,
     "quality runs no production-tagged Go suite", False),
    ("build harness unit test re-addition is caught", mutate_readd_harness_tests,
     "quality runs no build harness unit tests", False),
    ("standalone windows job re-addition is caught", mutate_readd_windows_jobs,
     "ci.yml drops the browser acceptance and standalone windows native jobs", False),
    ("duplicate pack evidence report is caught", mutate_duplicate_pack_report,
     "pack generates the server archive evidence report exactly once", False),
    ("e2e staging if removal is caught", mutate_e2e_stage_if,
     "server stages the reused CI e2e report only on reuse-hit", False),
    ("e2e exercise if removal is caught", mutate_e2e_exercise_if,
     "server reruns e2e only without reuse", False),
    ("xargs upload restore is caught", mutate_restore_xargs_upload,
     "release publish uploads through release-upload.mjs", False),
    ("wails cache order regression is caught", mutate_wails_cache_order,
     "windows wails cache restores before setup-go", False),
    ("setup-go cache-hit skip re-addition is caught", mutate_setup_go_cache_skip,
     "desktop setup-go is never skipped by the wails cache hit", False),
]


def main():
    started = time.monotonic()
    wiring = run_wiring_checks(CI, RELEASE, RESOLVER_TEXT, PACK_TEXT)
    wiring_elapsed = time.monotonic() - started
    resolver_started = time.monotonic()
    resolver = run_resolver_checks(RESOLVER_TEXT)
    resolver_elapsed = time.monotonic() - resolver_started
    evidence_started = time.monotonic()
    evidence = run_evidence_checks(RELEASE)
    evidence_elapsed = time.monotonic() - evidence_started
    upload_started = time.monotonic()
    upload = run_upload_checks()
    upload_elapsed = time.monotonic() - upload_started
    results = wiring + resolver + upload + evidence
    for name, ok, detail in results:
        print(f"{'ok' if ok else 'FAIL'} - {name}" + (f": {detail}" if not ok else ""))
    failures = [f"{name}: {detail}" for name, ok, detail in results if not ok]
    controls_started = time.monotonic()
    for label, mutate, expected, needs_resolver in NEGATIVE_CONTROLS:
        ci_copy = copy.deepcopy(CI)
        release_copy = copy.deepcopy(RELEASE)
        pack_copy = mutate(ci_copy, release_copy, PACK_TEXT) or PACK_TEXT
        mutated = run_checks(ci_copy, release_copy, RESOLVER_TEXT, pack_copy,
                             include_resolver=needs_resolver, include_evidence=False)
        caught = any(expected in name and not ok for name, ok, _ in mutated)
        print(f"{'ok' if caught else 'FAIL'} - negative control: {label}")
        if not caught:
            failures.append(f"negative control: {label}: mutation not detected by {expected!r}")
    controls_elapsed = time.monotonic() - controls_started
    if failures:
        print(f"\n{len(failures)} check(s) failed")
        sys.exit(1)
    print(f"\nall {len(results) + len(NEGATIVE_CONTROLS)} workflow reuse wiring checks passed")
    print(f"sections: wiring {len(wiring)} checks {wiring_elapsed:.1f}s; "
          f"resolver {len(resolver)} scenarios {resolver_elapsed:.1f}s; "
          f"upload stub {len(upload)} checks {upload_elapsed:.1f}s; "
          f"desktop evidence {len(evidence)} checks {evidence_elapsed:.1f}s; "
          f"{len(NEGATIVE_CONTROLS)} negative controls (wiring-only re-runs) {controls_elapsed:.1f}s; "
          f"total {time.monotonic() - started:.1f}s")


if __name__ == "__main__":
    main()
