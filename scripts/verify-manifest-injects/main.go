package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	bridgeID   = "lazycat-file-bridge"
	chooserID  = "open-save-chooser"
	gatewayID  = "gateway-auth"
	contentPre = "file:///lzcapp/pkg/content/"

	masterKeyTransport    = "NEXTERM_LAZYCAT_VAULT_MASTER_KEY"
	masterKeyTransportEnv = masterKeyTransport + `={{ stable_secret "vault_master" }}`
)

var expectedPublicPath = []string{"/device/enroll", "/agent/sync", "/agent/current-url", "/ws/device", "/share/public"}

var (
	directiveRe  = regexp.MustCompile(`^\s*#@build\s+(.*?)\s*$`)
	condRe       = regexp.MustCompile(`^profile\s*=\s*(\S+)$`)
	seedRe       = regexp.MustCompile(`^\{\{\s*stable_secret\s+"([^"]+)"\s*\}\}$`)
	bridgePrefix = regexp.MustCompile(`bridgePrefix\s*=\s*"([^"]+)"`)
	copyInjects  = regexp.MustCompile(`cp\s+"\$HERE"/\.\./injects/\*\.js\s+"\$CONTENT/lazycat-injects/"`)
)

func repoRoot() string {
	if wd, err := os.Getwd(); err == nil {
		dir := wd
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		return filepath.Dir(filepath.Dir(filepath.Dir(file)))
	}
	return "."
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func trim(text, profile string) (string, error) {
	var out []string
	var stack []bool
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		lineno := i + 1
		if m := directiveRe.FindStringSubmatch(line); m != nil {
			body := m[1]
			switch {
			case strings.HasPrefix(body, "if "):
				cond := strings.TrimSpace(body[3:])
				mm := condRe.FindStringSubmatch(cond)
				if mm == nil {
					return "", fmt.Errorf("%d: 不认识的 #@build 条件: %q", lineno, cond)
				}
				stack = append(stack, mm[1] == profile)
			case body == "else":
				if len(stack) == 0 {
					return "", fmt.Errorf("%d: 孤立的 #@build else", lineno)
				}
				stack[len(stack)-1] = !stack[len(stack)-1]
			case body == "end":
				if len(stack) == 0 {
					return "", fmt.Errorf("%d: 孤立的 #@build end", lineno)
				}
				stack = stack[:len(stack)-1]
			default:
				return "", fmt.Errorf("%d: 不认识的 #@build 指令: %q", lineno, body)
			}
			continue
		}
		taken := true
		for _, f := range stack {
			if !f {
				taken = false
				break
			}
		}
		if taken {
			out = append(out, line)
		}
	}
	if len(stack) > 0 {
		return "", fmt.Errorf("#@build 未闭合，残余 %d 层", len(stack))
	}
	return strings.Join(out, "\n") + "\n", nil
}

func parseYAML(data []byte) (map[string]interface{}, error) {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

func asList(v interface{}) []interface{} {
	l, _ := v.([]interface{})
	return l
}

func str(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func equalStringList(v interface{}, want []string) bool {
	l, ok := v.([]interface{})
	if !ok || len(l) != len(want) {
		return false
	}
	for i, item := range l {
		s, ok := item.(string)
		if !ok || s != want[i] {
			return false
		}
	}
	return true
}

func checkJS(src, label string) error {
	encoded, err := json.Marshal(src)
	if err != nil {
		return err
	}
	code := "const code = " + string(encoded) + ";\n" +
		"try { new Function('ctx', code); }" +
		" catch (e) { console.error(e.message); process.exit(3); }\n" +
		"console.log('" + label + ": " + strconv.Itoa(utf8.RuneCountInString(src)) + " 字符，语法 OK');\n"
	cmd := exec.Command("node", "-e", code)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return fmt.Errorf("%s 语法检查失败: %s", label, detail)
	}
	fmt.Println("    " + strings.TrimSpace(stdout.String()))
	return nil
}

func environmentOf(doc map[string]interface{}) []interface{} {
	services := asMap(doc["services"])
	if services == nil {
		return nil
	}
	server := asMap(services["nexterm-server"])
	if server == nil {
		return nil
	}
	return asList(server["environment"])
}

func assertProfile(doc map[string]interface{}, wantSubdomain string) (string, error) {
	app := asMap(doc["application"])
	if app == nil {
		return "", fmt.Errorf("document 缺少 application 节")
	}

	if got := str(app["subdomain"]); got != wantSubdomain {
		return "", fmt.Errorf("subdomain 期望 %s，实际 %s（#@build 裁剪错了？）", wantSubdomain, got)
	}
	fmt.Printf("    subdomain = %s\n", wantSubdomain)

	if routes := app["routes"]; routes == nil || !equalStringList(routes, []string{"/=http://nexterm-server:8080/"}) {
		return "", fmt.Errorf("routes 应当只有一条短服务名路由（全限定名会把包 ID 焊死）：%v", routes)
	}
	fmt.Println("    routes = 1 条短服务名路由")

	if !equalStringList(app["public_path"], expectedPublicPath) {
		return "", fmt.Errorf("public_path 应当恰好是设备 agent 合同 %d 条（被改宽会让匿名请求也拿到网关头，"+
			"被收窄会让设备 agent 够不到服务器）：%v", len(expectedPublicPath), app["public_path"])
	}
	fmt.Printf("    public_path = %v\n", expectedPublicPath)

	injects, ok := app["injects"].([]interface{})
	if !ok || len(injects) != 3 {
		return "", fmt.Errorf("injects 应当正好 3 条（网关头 + request 桥接 + browser 选择器）：%v", app["injects"])
	}
	byID := map[string]map[string]interface{}{}
	for _, item := range injects {
		inject := asMap(item)
		byID[str(inject["id"])] = inject
	}
	for _, id := range []string{bridgeID, chooserID, gatewayID} {
		if byID[id] == nil {
			return "", fmt.Errorf("inject id 不对：%v", sortedKeys(byID))
		}
	}
	if len(byID) != 3 {
		return "", fmt.Errorf("inject id 不对：%v", sortedKeys(byID))
	}

	gateway := byID[gatewayID]
	if str(gateway["on"]) != "request" {
		return "", fmt.Errorf("%s.on 应为 request", gatewayID)
	}
	if raw, present := gateway["auth_required"]; present {
		if b, ok := raw.(bool); !ok || !b {
			return "", fmt.Errorf("%s.auth_required 必须为 true（false 会给匿名请求也注入网关密钥）", gatewayID)
		}
	}
	if !equalStringList(gateway["when"], []string{"/*"}) {
		return "", fmt.Errorf("%s.when 应为 [/*]：%v", gatewayID, gateway["when"])
	}
	gatewayScripts := asList(gateway["do"])
	if len(gatewayScripts) != 1 {
		return "", fmt.Errorf("%s.do 应当只有一条脚本：%v", gatewayID, gateway["do"])
	}
	gatewayScript := asMap(gatewayScripts[0])
	gatewaySrc, _ := gatewayScript["src"].(string)
	if !strings.Contains(gatewaySrc, "ctx.headers.set") || !strings.Contains(gatewaySrc, "ctx.params.key") {
		return "", fmt.Errorf("网关头脚本必须通过 ctx.params.key 设置 X-NexTerm-Gateway-Auth")
	}
	gatewayKey := str(asMap(gatewayScript["params"])["key"])
	seedMatch := seedRe.FindStringSubmatch(gatewayKey)
	if seedMatch == nil {
		return "", fmt.Errorf("%s.params.key 必须是 stable_secret 模板：%q", gatewayID, gatewayKey)
	}
	environment := environmentOf(doc)
	expectedEnv := `NEXTERM_GATEWAY_AUTH={{ stable_secret "` + seedMatch[1] + `" }}`
	if !containsString(environment, expectedEnv) {
		return "", fmt.Errorf("services 环境变量必须使用同一 stable_secret seed（缺 %q）：%v", expectedEnv, environment)
	}
	fmt.Printf("    %s: on=request, auth_required=true, params.key 与 NEXTERM_GATEWAY_AUTH 同 seed\n", gatewayID)
	if err := checkJS(gatewaySrc, gatewayID); err != nil {
		return "", err
	}

	if !containsString(environment, masterKeyTransportEnv) {
		return "", fmt.Errorf("services 环境变量必须保留 %q 作为平台注入通道（镜像 entrypoint 把它落成 0600 密钥文件）：%v",
			masterKeyTransportEnv, environment)
	}
	for _, entry := range environment {
		if strings.HasPrefix(str(entry), "NEXTERM_MASTER_KEY=") {
			return "", fmt.Errorf("不得把已弃用的 NEXTERM_MASTER_KEY 直注服务环境（entrypoint 落 0600 文件后"+
				"经 NEXTERM_MASTER_KEY_FILE 交给服务端）：%v", environment)
		}
	}
	fmt.Printf("    master key: %s 平台注入, 已弃用的 NEXTERM_MASTER_KEY 不再直注\n", masterKeyTransport)

	bridge := byID[bridgeID]
	if str(bridge["on"]) != "request" {
		return "", fmt.Errorf("%s.on 应为 request", bridgeID)
	}
	if !equalStringList(bridge["when"], []string{"/__lazycat_file_bridge/*"}) {
		return "", fmt.Errorf("%s.when 应为 [/__lazycat_file_bridge/*]：%v", bridgeID, bridge["when"])
	}
	var bridgeSrc string
	if do := asList(bridge["do"]); len(do) > 0 {
		bridgeSrc, _ = asMap(do[0])["src"].(string)
	}
	if !strings.Contains(bridgeSrc, "ctx.proxy.to") {
		return "", fmt.Errorf("桥接脚本不完整")
	}
	prefixMatch := bridgePrefix.FindStringSubmatch(bridgeSrc)
	if prefixMatch == nil {
		return "", fmt.Errorf("桥接脚本里找不到 bridgePrefix")
	}
	bridgePrefixValue := prefixMatch[1]
	fmt.Printf("    %s: on=request, bridgePrefix=%s\n", bridgeID, bridgePrefixValue)
	if err := checkJS(bridgeSrc, bridgeID); err != nil {
		return "", err
	}

	chooser := byID[chooserID]
	if str(chooser["on"]) != "browser" {
		return "", fmt.Errorf("%s.on 应为 browser", chooserID)
	}
	if !equalStringList(chooser["when"], []string{"/*"}) {
		return "", fmt.Errorf("%s.when 应为 [/*]：%v", chooserID, chooser["when"])
	}
	scripts := asList(chooser["do"])
	if len(scripts) != 1 {
		return "", fmt.Errorf("%s.do 应当只有一条脚本：%v", chooserID, chooser["do"])
	}
	uri, _ := asMap(scripts[0])["src"].(string)
	if !strings.HasPrefix(uri, contentPre) {
		return "", fmt.Errorf("%s 的脚本必须随包提供（%s...），实际 %q", chooserID, contentPre, uri)
	}
	params := asMap(asMap(scripts[0])["params"])
	if str(params["fileBridgeRoot"]) != bridgePrefixValue {
		return "", fmt.Errorf("params.fileBridgeRoot(%q) 必须与 bridgePrefix(%q) 一致",
			str(params["fileBridgeRoot"]), bridgePrefixValue)
	}
	fmt.Printf("    %s: on=browser, fileBridgeRoot 与 bridgePrefix 一致\n", chooserID)
	return uri, nil
}

func assertEntrypointWrapper(wrapper, dockerfile string) error {
	if !strings.Contains(dockerfile, `ENTRYPOINT ["/usr/local/bin/nexterm-server-entrypoint"]`) {
		return fmt.Errorf("Dockerfile ENTRYPOINT 必须指向 entrypoint 包装脚本")
	}
	if !strings.Contains(wrapper, "unset "+masterKeyTransport) {
		return fmt.Errorf("entrypoint 必须在 exec 前 unset %s，否则密钥留在进程环境里", masterKeyTransport)
	}
	if !strings.Contains(wrapper, `NEXTERM_MASTER_KEY_FILE="$data_dir/master.key"`) ||
		!strings.Contains(wrapper, "export NEXTERM_MASTER_KEY_FILE") {
		return fmt.Errorf("entrypoint 必须把密钥文件路径经 NEXTERM_MASTER_KEY_FILE 传给服务端")
	}
	if !strings.Contains(wrapper, "chmod 0600") {
		return fmt.Errorf("entrypoint 必须把密钥文件权限设为 0600")
	}
	if !strings.Contains(wrapper, "umask 077") {
		return fmt.Errorf("entrypoint 必须在写密钥文件前 umask 077")
	}
	if !strings.Contains(wrapper, "exec /usr/local/bin/nexterm-server") {
		return fmt.Errorf("entrypoint 必须 exec 真正的服务端")
	}
	fmt.Println("    entrypoint: 通道变量落 0600 密钥文件, NEXTERM_MASTER_KEY_FILE 交接, unset 后 exec 服务端")
	return nil
}

func containsString(list []interface{}, want string) bool {
	for _, item := range list {
		if s, ok := item.(string); ok && s == want {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func selftest(raw []byte) {
	root := repoRoot()
	wrapperBytes, err := os.ReadFile(filepath.Join(root, "lazycat", "image", "entrypoint.sh"))
	if err != nil {
		fatalf("%v", err)
	}
	dockerfileBytes, err := os.ReadFile(filepath.Join(root, "lazycat", "image", "Dockerfile"))
	if err != nil {
		fatalf("%v", err)
	}
	wrapper := string(wrapperBytes)
	dockerfile := string(dockerfileBytes)

	parseBase := func() map[string]interface{} {
		trimmed, err := trim(string(raw), "release")
		if err != nil {
			fatalf("%v", err)
		}
		doc, err := parseYAML([]byte(trimmed))
		if err != nil {
			fatalf("%v", err)
		}
		return doc
	}

	base := parseBase()
	gatewayIndex := -1
	for i, item := range asList(asMap(base["application"])["injects"]) {
		if str(asMap(item)["id"]) == gatewayID {
			gatewayIndex = i
			break
		}
	}
	if gatewayIndex < 0 {
		fatalf("自测夹具里找不到 %s inject", gatewayID)
	}

	serverEnv := func(doc map[string]interface{}) map[string]interface{} {
		return asMap(asMap(doc["services"])["nexterm-server"])
	}

	negatives := []struct {
		name   string
		mutate func(doc map[string]interface{})
	}{
		{"public_path 改宽", func(doc map[string]interface{}) {
			widened := make([]interface{}, 0, len(expectedPublicPath)+1)
			for _, p := range expectedPublicPath {
				widened = append(widened, p)
			}
			asMap(doc["application"])["public_path"] = append(widened, "/rpc")
		}},
		{"public_path 删除", func(doc map[string]interface{}) {
			asMap(doc["application"])["public_path"] = []interface{}{}
		}},
		{"auth_required: false", func(doc map[string]interface{}) {
			injects := asList(asMap(doc["application"])["injects"])
			asMap(injects[gatewayIndex])["auth_required"] = false
		}},
		{"网关密钥写死", func(doc map[string]interface{}) {
			injects := asList(asMap(doc["application"])["injects"])
			params := asMap(asMap(asList(asMap(injects[gatewayIndex])["do"])[0])["params"])
			params["key"] = "hardcoded-secret"
		}},
		{"env seed 不一致", func(doc map[string]interface{}) {
			env := serverEnv(doc)
			entries := asList(env["environment"])
			for i, entry := range entries {
				s := str(entry)
				if strings.HasPrefix(s, "NEXTERM_GATEWAY_AUTH=") {
					entries[i] = strings.ReplaceAll(s, `stable_secret "gateway_auth"`, `stable_secret "other_seed"`)
				}
			}
		}},
		{"已弃用 NEXTERM_MASTER_KEY 直注", func(doc map[string]interface{}) {
			env := serverEnv(doc)
			env["environment"] = append(asList(env["environment"]), "NEXTERM_MASTER_KEY=hardcoded-secret")
		}},
		{"主密钥注入通道被删", func(doc map[string]interface{}) {
			env := serverEnv(doc)
			var kept []interface{}
			for _, entry := range asList(env["environment"]) {
				if !strings.HasPrefix(str(entry), masterKeyTransport+"=") {
					kept = append(kept, entry)
				}
			}
			env["environment"] = kept
		}},
	}

	if _, err := assertProfile(parseBase(), "nexterm"); err != nil {
		fatalf("正例未通过：%v", err)
	}
	fmt.Println("    正例通过")
	for _, negative := range negatives {
		doc := parseBase()
		negative.mutate(doc)
		if _, err := assertProfile(doc, "nexterm"); err != nil {
			fmt.Printf("    负例通过（%s 被拒绝）\n", negative.name)
			continue
		}
		fatalf("负例未被发现：%s 应当断言失败", negative.name)
	}

	wrapperNegatives := []struct {
		name       string
		wrapper    string
		dockerfile string
	}{
		{"entrypoint 漏 unset 通道变量", strings.ReplaceAll(wrapper, "unset "+masterKeyTransport, "true"), dockerfile},
		{"entrypoint 漏 0600", strings.ReplaceAll(wrapper, "chmod 0600", "chmod 0644"), dockerfile},
		{"entrypoint 漏 umask", strings.ReplaceAll(wrapper, "umask 077", "umask 022"), dockerfile},
		{"entrypoint 漏 NEXTERM_MASTER_KEY_FILE", strings.ReplaceAll(wrapper, `NEXTERM_MASTER_KEY_FILE="$data_dir/master.key"`, "true"), dockerfile},
		{"Dockerfile 绕开 entrypoint", wrapper, strings.ReplaceAll(dockerfile, `ENTRYPOINT ["/usr/local/bin/nexterm-server-entrypoint"]`, `ENTRYPOINT ["/usr/local/bin/nexterm-server"]`)},
	}
	if err := assertEntrypointWrapper(wrapper, dockerfile); err != nil {
		fatalf("正例未通过：%v", err)
	}
	for _, negative := range wrapperNegatives {
		if err := assertEntrypointWrapper(negative.wrapper, negative.dockerfile); err != nil {
			fmt.Printf("    负例通过（%s 被拒绝）\n", negative.name)
			continue
		}
		fatalf("负例未被发现：%s 应当断言失败", negative.name)
	}
}

func main() {
	root := repoRoot()
	manifestPath := filepath.Join(root, "lazycat", "lzc-manifest.yml")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		fatalf("找不到 %s", manifestPath)
	}

	fmt.Println("== 自测（负例必须被拒绝）==")
	selftest(raw)

	var chooserURI string
	for _, profile := range []struct{ profile, subdomain string }{{"dev", "nexterm-dev"}, {"release", "nexterm"}} {
		fmt.Printf("== profile=%s ==\n", profile.profile)
		trimmed, err := trim(string(raw), profile.profile)
		if err != nil {
			fatalf("%v", err)
		}
		doc, err := parseYAML([]byte(trimmed))
		if err != nil {
			fatalf("%v", err)
		}
		chooserURI, err = assertProfile(doc, profile.subdomain)
		if err != nil {
			fatalf("%v", err)
		}
	}

	fmt.Println("== 包内路径一致性 ==")
	rel := chooserURI[len(contentPre):]
	repoRel := strings.Replace(rel, "lazycat-injects/", "injects/", 1)
	buildSh, err := os.ReadFile(filepath.Join(root, "lazycat", "image", "build-server.sh"))
	if err != nil {
		fatalf("%v", err)
	}
	if !copyInjects.MatchString(string(buildSh)) {
		fatalf("build-server.sh 里没有把 injects/*.js 拷进 $CONTENT/lazycat-injects/")
	}
	fmt.Printf("    manifest 引用   %s\n", rel)
	fmt.Println("    build-server.sh 拷到 $CONTENT/lazycat-injects/")
	src := filepath.Join(root, "lazycat", filepath.FromSlash(repoRel))
	info, err := os.Stat(src)
	if err != nil {
		fatalf("仓库里没有随包脚本：%s", src)
	}
	fmt.Printf("    仓库源文件存在（%d 字节）\n", info.Size())

	fmt.Println("== 主密钥注入链 ==")
	wrapper, err := os.ReadFile(filepath.Join(root, "lazycat", "image", "entrypoint.sh"))
	if err != nil {
		fatalf("%v", err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(root, "lazycat", "image", "Dockerfile"))
	if err != nil {
		fatalf("%v", err)
	}
	if err := assertEntrypointWrapper(string(wrapper), string(dockerfile)); err != nil {
		fatalf("%v", err)
	}

	fmt.Println("\n全部断言通过；外部真机验收仍以 evidence gap 单独记录。")
}
