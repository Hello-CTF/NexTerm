package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func ciRunDocument(status string, conclusion interface{}, runID int) map[string]interface{} {
	return map[string]interface{}{
		"id":          runID,
		"name":        "CI",
		"path":        ".github/workflows/ci.yml",
		"status":      status,
		"conclusion":  conclusion,
		"created_at":  "2026-10-04T00:00:00Z",
		"head_branch": "master",
		"head_sha":    "stubsha",
	}
}

func runsReply(runs ...map[string]interface{}) string {
	if runs == nil {
		runs = []map[string]interface{}{}
	}
	reply, _ := json.Marshal(map[string]interface{}{"workflow_runs": runs})
	return string(reply)
}

func apiFailureReply(message string) string {
	return apiFailureReplyWithOutput("", message)
}

func apiFailureReplyWithOutput(stdout, message string) string {
	return "GH_STUB_API_FAILURE:" + toJSON(map[string]string{"stdout": stdout, "stderr": message})
}

func runResolver(resolverText string, runsReplies, artifactsReplies []string, extraEnv map[string]string) cmdResult {
	stubDir, err := os.MkdirTemp("", "reuse-resolver-")
	if err != nil {
		fatalf("%v", err)
	}
	defer os.RemoveAll(stubDir)

	quoted := func(replies []string) string {
		parts := make([]string, len(replies))
		for i, reply := range replies {
			parts[i] = shellQuote(reply)
		}
		return strings.Join(parts, " ")
	}
	stub := "#!/usr/bin/env bash\n" +
		"dir=\"$(cd \"$(dirname \"${BASH_SOURCE[0]}\")\" && pwd)\"\n" +
		"url=\"$2\"\n" +
		"case \"$url\" in\n" +
		"  *artifacts*)\n" +
		"    expected=\"repos/stub/repo/actions/runs/${STUB_ARTIFACT_RUN_ID}/artifacts?per_page=100\"\n" +
		"    if [ \"$url\" != \"$expected\" ]; then\n" +
		"      message=\"unexpected artifact query: $url (expected $expected)\"\n" +
		"      printf '%s\\n' \"$message\" > \"$dir/.stub-assertion\"\n" +
		"      printf '%s\\n' \"$message\" >&2\n" +
		"      exit 90\n" +
		"    fi\n" +
		"    n_file=\"$dir/.artifacts-count\"; replies=(" + quoted(artifactsReplies) + ") ;;\n" +
		"  *)\n" +
		"    if [[ \"$url\" != *\"head_sha=stubsha\"* ]]; then\n" +
		"      message=\"run query lacks head_sha=stubsha: $url\"\n" +
		"      printf '%s\\n' \"$message\" > \"$dir/.stub-assertion\"\n" +
		"      printf '%s\\n' \"$message\" >&2\n" +
		"      exit 90\n" +
		"    fi\n" +
		"    n_file=\"$dir/.runs-count\"; replies=(" + quoted(runsReplies) + ") ;;\n" +
		"esac\n" +
		"n=0\n" +
		"[ -f \"$n_file\" ] && n=\"$(cat \"$n_file\")\"\n" +
		"printf '%s' \"$((n+1))\" > \"$n_file\"\n" +
		"idx=$(( n < ${#replies[@]} ? n : ${#replies[@]} - 1 ))\n" +
		"reply=\"${replies[$idx]}\"\n" +
		"if [[ \"$reply\" == GH_STUB_API_FAILURE:* ]]; then\n" +
		"  payload=\"${reply#GH_STUB_API_FAILURE:}\"\n" +
		"  printf '%s' \"$payload\" | jq -j '.stdout'\n" +
		"  printf '%s' \"$payload\" | jq -r '.stderr' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"printf '%s' \"$reply\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "gh"), []byte(stub), 0o755); err != nil {
		fatalf("%v", err)
	}
	resolverPath := filepath.Join(stubDir, "resolve-ci-reuse.sh")
	if err := os.WriteFile(resolverPath, []byte(resolverText), 0o644); err != nil {
		fatalf("%v", err)
	}

	artifactRunID := "424242"
	if configured := extraEnv["STUB_ARTIFACT_RUN_ID"]; configured != "" {
		artifactRunID = configured
	}
	env := []string{
		"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GITHUB_REPOSITORY=stub/repo",
		"GITHUB_SHA=stubsha",
		"GITHUB_RUN_ID=999",
		"GH_TOKEN=stub-token",
		"STUB_ARTIFACT_RUN_ID=" + artifactRunID,
	}
	for _, key := range sortedSetKeys(extraEnv) {
		if key != "STUB_ARTIFACT_RUN_ID" {
			env = append(env, key+"="+extraEnv[key])
		}
	}
	cmd := exec.Command("bash", resolverPath)
	cmd.Env = env
	result := runCmd(cmd)
	if assertion, err := os.ReadFile(filepath.Join(stubDir, ".stub-assertion")); err == nil {
		result.rc = -2
		result.stderr += "\n" + string(assertion)
	}
	return result
}

func runCmd(cmd *exec.Cmd) cmdResult {
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	rc := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		rc = exitErr.ExitCode()
	} else if err != nil {
		rc = -1
		stderr.WriteString(err.Error())
	}
	return cmdResult{rc: rc, stdout: stdout.String(), stderr: stderr.String()}
}

func runResolverChecks(resolverText string) []checkResult {
	c := &collector{}
	check := c.check

	required := resolverRequiredArtifacts(resolverText)
	expectedRequired := []string{
		"frontend-dist",
		"native-windows-amd64-desktop",
		"native-windows-arm64-desktop",
		"native-darwin-arm64-desktop",
		"native-darwin-amd64-desktop",
		"native-linux-amd64-desktop",
		"native-linux-arm64-desktop",
		"native-linux-amd64-server",
		"native-linux-arm64-server",
		"native-linux-amd64-agent",
		"native-linux-arm64-agent",
		"native-darwin-arm64-agent",
		"native-darwin-amd64-agent",
		"native-windows-amd64-agent",
		"mobile-android",
		"mobile-ios",
	}
	requiredMatches := len(required) == len(expectedRequired)
	for _, name := range expectedRequired {
		requiredMatches = requiredMatches && required[name]
	}
	check("resolver required artifacts match the independent 16-artifact contract", requiredMatches,
		fmt.Sprintf("actual=%v", sortedSet(required)))
	fullNames := sortedStringSlice(expectedRequired)
	green := runsReply(ciRunDocument("completed", "success", 424242))
	inProgress := runsReply(ciRunDocument("in_progress", nil, 424242))
	waitEnv := map[string]string{"CI_REUSE_WAIT_TIMEOUT": "30", "CI_REUSE_POLL_INTERVAL": "1"}

	masterEnv := map[string]string{"CI_REUSE_EXPECTED_REF": "master"}
	result := runResolver(resolverText, []string{green}, []string{toJSON(fullNames)}, masterEnv)
	check("resolver full set reuses the green run on the matching master ref",
		result.rc == 0 && strings.Contains(result.stdout, "reused=true") && strings.Contains(result.stdout, "artifact-run-id=424242"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	wrongRefGreen := ciRunDocument("completed", "success", 424242)
	wrongRefGreen["head_branch"] = "v1.2.3"
	result = runResolver(resolverText, []string{runsReply(wrongRefGreen)}, []string{toJSON(fullNames)}, masterEnv)
	check("resolver never reuses a same-SHA green run from a different ref",
		result.rc == 0 && strings.Contains(result.stdout, "reused=false") && strings.Contains(result.stdout, "artifact-run-id=999"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	wrongRefPending := ciRunDocument("in_progress", nil, 424242)
	wrongRefPending["head_branch"] = "v1.2.3"
	result = runResolver(resolverText, []string{runsReply(wrongRefPending)}, []string{toJSON(fullNames)},
		map[string]string{"CI_REUSE_EXPECTED_REF": "master", "CI_REUSE_WAIT_TIMEOUT": "2", "CI_REUSE_POLL_INTERVAL": "1"})
	check("resolver does not wait for a same-SHA pending run from a different ref",
		result.rc == 0 && strings.Contains(result.stdout, "reused=false") && strings.Contains(result.stdout, "artifact-run-id=999") &&
			!strings.Contains(result.stderr, "still in progress") && !strings.Contains(result.stderr, "timed out"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	tagRun := ciRunDocument("completed", "success", 525252)
	tagRun["head_branch"] = "v1.2.3"
	result = runResolver(resolverText, []string{runsReply(tagRun)}, []string{toJSON(fullNames)},
		map[string]string{"CI_REUSE_EXPECTED_REF": "v1.2.3", "STUB_ARTIFACT_RUN_ID": "525252"})
	check("resolver reuses a green run on the matching tag ref",
		result.rc == 0 && strings.Contains(result.stdout, "reused=true") && strings.Contains(result.stdout, "artifact-run-id=525252"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	fallbackResult := func(result cmdResult, errorLogs ...string) bool {
		ok := result.rc == 0 && strings.Contains(result.stdout, "reused=false") &&
			strings.Contains(result.stdout, "artifact-run-id=999") && !strings.Contains(result.stdout, "reused=true")
		for _, log := range errorLogs {
			ok = ok && strings.Contains(result.stderr, log)
		}
		return ok
	}

	wrongSHAGreen := ciRunDocument("completed", "success", 626262)
	wrongSHAGreen["head_sha"] = "othersha"
	result = runResolver(resolverText, []string{runsReply(wrongSHAGreen)}, []string{toJSON(fullNames)}, masterEnv)
	check("resolver never reuses a same-ref green run from a different SHA", fallbackResult(result),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	for _, missing := range fullNames {
		var partial []string
		for _, name := range fullNames {
			if name != missing {
				partial = append(partial, name)
			}
		}
		result := runResolver(resolverText, []string{green}, []string{toJSON(partial)}, nil)
		check(fmt.Sprintf("resolver falls back when %s is missing", missing),
			result.rc == 0 && strings.Contains(result.stdout, "reused=false") && strings.Contains(result.stdout, "artifact-run-id=999"),
			fmt.Sprintf("stdout=%q", result.stdout))
	}

	result = runResolver(resolverText, []string{runsReply()}, []string{toJSON(fullNames)}, nil)
	check("resolver falls back when no green run exists",
		result.rc == 0 && strings.Contains(result.stdout, "reused=false") && strings.Contains(result.stdout, "artifact-run-id=999"),
		fmt.Sprintf("stdout=%q", result.stdout))

	result = runResolver(resolverText, []string{apiFailureReply("initial runs API failure")}, []string{toJSON(fullNames)}, masterEnv)
	check("resolver falls back cleanly when the initial run query fails",
		fallbackResult(result, "failed to list CI runs for stubsha", "initial runs API failure"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	result = runResolver(resolverText, []string{inProgress, apiFailureReply("polling runs API failure")}, []string{toJSON(fullNames)}, waitEnv)
	check("resolver falls back cleanly when a polling run query fails",
		fallbackResult(result, "failed to list CI runs while waiting for 424242", "polling runs API failure"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	result = runResolver(resolverText, []string{green}, []string{apiFailureReply("artifact API failure")}, masterEnv)
	check("resolver falls back cleanly when the artifact query fails",
		fallbackResult(result, "failed to inspect reusable artifacts for green CI run 424242", "artifact API failure"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	result = runResolver(resolverText, []string{apiFailureReplyWithOutput(green, "initial runs API failure with green stdout")}, []string{toJSON(fullNames)}, masterEnv)
	check("resolver ignores valid green stdout when the initial run query fails",
		fallbackResult(result, "failed to list CI runs for stubsha", "initial runs API failure with green stdout"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	result = runResolver(resolverText, []string{inProgress, apiFailureReplyWithOutput(green, "polling runs API failure with green stdout")}, []string{toJSON(fullNames)}, waitEnv)
	check("resolver ignores valid green stdout when a polling run query fails",
		fallbackResult(result, "failed to list CI runs while waiting for 424242", "polling runs API failure with green stdout"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	result = runResolver(resolverText, []string{green}, []string{apiFailureReplyWithOutput(toJSON(fullNames), "artifact API failure with full stdout")}, masterEnv)
	check("resolver ignores valid artifact stdout when the artifact query fails",
		fallbackResult(result, "failed to inspect reusable artifacts for green CI run 424242", "artifact API failure with full stdout"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	result = runResolver(resolverText, []string{inProgress, green}, []string{toJSON(fullNames)}, waitEnv)
	check("resolver waits for an in-progress run and reuses its green completion",
		result.rc == 0 && strings.Contains(result.stdout, "reused=true") && strings.Contains(result.stdout, "artifact-run-id=424242") &&
			strings.Contains(result.stderr, "still in progress"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	result = runResolver(resolverText, []string{inProgress}, []string{toJSON(fullNames)},
		map[string]string{"CI_REUSE_WAIT_TIMEOUT": "2", "CI_REUSE_POLL_INTERVAL": "1"})
	check("resolver times out waiting and falls back to the full CI",
		result.rc == 0 && strings.Contains(result.stdout, "reused=false") && strings.Contains(result.stdout, "artifact-run-id=999") &&
			strings.Contains(result.stderr, "timed out") && !strings.Contains(result.stdout, "reused=true"),
		fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))

	for _, conclusion := range []string{"failure", "cancelled"} {
		completed := runsReply(ciRunDocument("completed", conclusion, 424242))
		result := runResolver(resolverText, []string{inProgress, completed}, []string{toJSON(fullNames)}, waitEnv)
		check(fmt.Sprintf("resolver falls back when the waited run ends as %s", conclusion),
			result.rc == 0 && strings.Contains(result.stdout, "reused=false") && strings.Contains(result.stdout, "artifact-run-id=999") &&
				strings.Contains(result.stderr, "conclusion "+conclusion) && !strings.Contains(result.stdout, "reused=true"),
			fmt.Sprintf("stdout=%q stderr=%q", result.stdout, result.stderr))
	}

	var withoutFrontend []string
	for _, name := range fullNames {
		if name != "frontend-dist" {
			withoutFrontend = append(withoutFrontend, name)
		}
	}
	result = runResolver(resolverText, []string{inProgress, green}, []string{toJSON(withoutFrontend)}, waitEnv)
	check("resolver falls back when the waited green run lacks artifacts",
		result.rc == 0 && strings.Contains(result.stdout, "reused=false") && strings.Contains(result.stdout, "artifact-run-id=999") &&
			strings.Contains(result.stderr, "missing reusable artifacts"),
		fmt.Sprintf("stdout=%q", result.stdout))

	return c.results
}

func toJSON(v interface{}) string {
	data, _ := json.Marshal(v)
	return string(data)
}
