package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/Hello-CTF/NexTerm/internal/applog"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

const updateOverallTimeout = 20 * time.Minute

var (
	// cliUpgraderFactory builds the Upgrader used by `update`; it is always pinned to the fixed official release source.
	cliUpgraderFactory = defaultUpgrader
	cliExecutable      = os.Executable
	cliStdin           = io.Reader(os.Stdin)
	cliStdinIsTerminal = func() bool {
		info, err := os.Stdin.Stat()
		return err == nil && info.Mode()&os.ModeCharDevice != 0
	}
)

// latestReleaseURL derives the "latest release" page from the fixed release download base.
func latestReleaseURL(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/download") + "/latest"
}

// resolveLatestVersion asks the official release source (never the control server) for the latest non-prerelease
// tag by reading the redirect target of /releases/latest; nothing is downloaded or executed at this step.
func resolveLatestVersion(ctx context.Context, baseURL string) (string, error) {
	client := &http.Client{
		Timeout:       cliTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL(baseURL), nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	_ = response.Body.Close()
	location := response.Header.Get("Location")
	index := strings.LastIndex(location, "/tag/v")
	if response.StatusCode < 300 || response.StatusCode > 399 || index < 0 {
		return "", fmt.Errorf("无法从 %s 解析最新版本 (HTTP %d)", latestReleaseURL(baseURL), response.StatusCode)
	}
	latest := location[index+len("/tag/v"):]
	if err := validateReleaseVersion(latest); err != nil {
		return "", err
	}
	return latest, nil
}

// compareReleaseVersions returns -1/0/1 for current vs target; ok is false when either is not a semantic version.
func compareReleaseVersions(current, target string) (int, bool) {
	c, t := "v"+strings.TrimPrefix(current, "v"), "v"+strings.TrimPrefix(target, "v")
	if !semver.IsValid(c) || !semver.IsValid(t) {
		return 0, false
	}
	return semver.Compare(c, t), true
}

func cliUpdate(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	set := newFlagSet("update", stderr)
	target := set.String("version", "", "目标版本（默认为官方 release 的最新版本）")
	var yes, force bool
	set.BoolVar(&yes, "yes", false, "跳过确认提示")
	set.BoolVar(&yes, "y", false, "同 --yes")
	set.BoolVar(&force, "force", false, "允许重装同版本或降级")
	set.BoolVar(&force, "f", false, "同 --force")
	check := set.Bool("check", false, "仅检查版本，不下载、不替换")
	if err := set.Parse(args); err != nil {
		return ExitUsage
	}
	if set.NArg() > 0 {
		fmt.Fprintf(stderr, "agent update: 多余的参数 %q\n", applog.RedactText(set.Arg(0)))
		return ExitUsage
	}
	if len(cliServicePrefix) > 0 {
		fmt.Fprintln(stderr, "agent update: 内嵌发行版（nexterm-server agent）不支持 update，请升级 nexterm-server")
		return ExitError
	}
	wanted := strings.TrimPrefix(*target, "v")
	if *target != "" {
		if err := validateReleaseVersion(wanted); err != nil {
			printCLIError(stderr, "agent update:", err)
			return ExitUsage
		}
	}
	executable, err := cliExecutable()
	if err != nil {
		printCLIError(stderr, "agent update:", err)
		return ExitError
	}
	upgrader := cliUpgraderFactory(executable)
	ctx, cancel := context.WithTimeout(context.Background(), updateOverallTimeout)
	defer cancel()

	current := version.Version
	if wanted == "" {
		resolveCtx, resolveCancel := context.WithTimeout(ctx, cliTimeout)
		latest, err := resolveLatestVersion(resolveCtx, upgrader.BaseURL)
		resolveCancel()
		if err != nil {
			printCLIError(stderr, "agent update: 查询最新版本失败：", err)
			return ExitError
		}
		wanted = latest
	}
	cmp, comparable := compareReleaseVersions(current, wanted)
	fmt.Fprintf(stdout, "当前版本: %s\n目标版本: %s\n", current, wanted)
	if *check {
		switch {
		case !comparable:
			fmt.Fprintln(stdout, "当前版本为非正式发行版本，无法与目标版本比较")
		case cmp < 0:
			fmt.Fprintf(stdout, "有可用更新: %s -> %s\n", current, wanted)
		case cmp == 0:
			fmt.Fprintln(stdout, "已是最新版本")
		default:
			fmt.Fprintln(stdout, "当前版本高于目标版本")
		}
		return ExitOK
	}
	switch {
	case !comparable && !force:
		fmt.Fprintln(stderr, "agent update: 当前版本为非正式发行版本，无法比较；如需替换，请添加 --force")
		return ExitUsage
	case comparable && cmp == 0 && !force:
		fmt.Fprintf(stdout, "已是最新版本（%s），无需更新；如需重装，请添加 --force\n", current)
		return ExitOK
	case comparable && cmp > 0 && !force:
		if *target == "" {
			fmt.Fprintf(stdout, "当前版本 %s 不低于最新发行版 %s，无需更新\n", current, wanted)
			return ExitOK
		}
		fmt.Fprintf(stderr, "agent update: 目标版本 %s 低于当前版本 %s；如需降级，请添加 --force\n", wanted, current)
		return ExitUsage
	}
	if !yes {
		if !cliStdinIsTerminal() {
			fmt.Fprintln(stderr, "agent update: 非交互环境下需添加 --yes 才能继续")
			return ExitUsage
		}
		fmt.Fprintf(stdout, "将把设备代理从 %s 更新到 %s，并重启后台服务。是否继续？[y/N] ", current, wanted)
		line, _ := bufio.NewReader(cliStdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "已取消")
			return ExitError
		}
	}
	if err := checkWritableDir(filepath.Dir(upgrader.Executable)); err != nil {
		fmt.Fprintf(stderr, "agent update: 无权写入 %s：%s\n请使用安装该程序的用户重新执行（以管理员或 root 安装的，需提升权限）\n", filepath.Dir(upgrader.Executable), applog.RedactText(err.Error()))
		return ExitError
	}
	if upgrader.Logger == nil {
		upgrader.Logger = slog.New(applog.NewRedactingHandler(slog.NewTextHandler(stderr, nil)))
	}
	fmt.Fprintf(stdout, "正在从官方 release 下载 %s 并校验 SHA256SUMS...\n", wanted)
	if err := upgrader.Upgrade(ctx, wanted); err != nil {
		printCLIError(stderr, "agent update: 升级失败，当前版本保持不变：", err)
		return ExitError
	}
	fmt.Fprintf(stdout, "升级已就绪: %s -> %s\n", current, wanted)
	state := cliNewManager(getenv).Status(ctx)
	switch {
	case runtime.GOOS == "windows" && state.Active:
		fmt.Fprintf(stdout, "约 %d 秒后替换程序文件；随后请重启设备代理的计划任务或服务，以载入新版本\n", replaceScriptDelaySeconds)
	case state.Installed:
		fmt.Fprintf(stdout, "约 %d 秒后替换程序文件并自动重启后台服务\n", replaceScriptDelaySeconds)
	default:
		fmt.Fprintf(stdout, "约 %d 秒后替换程序文件；后台服务未安装，如设备代理正在手动运行，请自行重启\n", replaceScriptDelaySeconds)
	}
	return ExitOK
}

func checkWritableDir(dir string) error {
	file, err := os.CreateTemp(dir, ".nexterm-writecheck-*")
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return pathErr.Err
		}
		return err
	}
	name := file.Name()
	_ = file.Close()
	return os.Remove(name)
}
