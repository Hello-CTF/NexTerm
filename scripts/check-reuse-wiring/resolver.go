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
		"id":         runID,
		"name":       "CI",
		"path":       ".github/workflows/ci.yml",
		"status":     status,
		"conclusion": conclusion,
		"created_at": "2026-10-04T00:00:00Z",
	}
}

func runsReply(runs ...map[string]interface{}) string {
	if runs == nil {
		runs = []map[string]interface{}{}
	}
	reply, _ := json.Marshal(map[string]interface{}{"workflow_runs": runs})
	return string(reply)
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
		"case \"$2\" in\n" +
		"  *artifacts*) n_file=\"$dir/.artifacts-count\"; replies=(" + quoted(artifactsReplies) + ") ;;\n" +
		"  *) n_file=\"$dir/.runs-count\"; replies=(" + quoted(runsReplies) + ") ;;\n" +
		"esac\n" +
		"n=0\n" +
		"[ -f \"$n_file\" ] && n=\"$(cat \"$n_file\")\"\n" +
		"printf '%s' \"$((n+1))\" > \"$n_file\"\n" +
		"idx=$(( n < ${#replies[@]} ? n : ${#replies[@]} - 1 ))\n" +
		"printf '%s' \"${replies[$idx]}\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "gh"), []byte(stub), 0o755); err != nil {
		fatalf("%v", err)
	}
	resolverPath := filepath.Join(stubDir, "resolve-ci-reuse.sh")
	if err := os.WriteFile(resolverPath, []byte(resolverText), 0o644); err != nil {
		fatalf("%v", err)
	}

	env := []string{
		"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GITHUB_REPOSITORY=stub/repo",
		"GITHUB_SHA=stubsha",
		"GITHUB_RUN_ID=999",
		"GH_TOKEN=stub-token",
	}
	for _, key := range sortedSetKeys(extraEnv) {
		env = append(env, key+"="+extraEnv[key])
	}
	cmd := exec.Command("bash", resolverPath)
	cmd.Env = env
	return runCmd(cmd)
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
	fullNames := sortedSet(required)
	green := runsReply(ciRunDocument("completed", "success", 424242))
	inProgress := runsReply(ciRunDocument("in_progress", nil, 424242))
	waitEnv := map[string]string{"CI_REUSE_WAIT_TIMEOUT": "30", "CI_REUSE_POLL_INTERVAL": "1"}

	result := runResolver(resolverText, []string{green}, []string{toJSON(fullNames)}, nil)
	check("resolver full set reuses the green run",
		result.rc == 0 && strings.Contains(result.stdout, "reused=true") && strings.Contains(result.stdout, "artifact-run-id=424242"),
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
