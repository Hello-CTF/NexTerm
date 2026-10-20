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

var expectedPublicPath = []string{"/device/enroll", "/agent/sync", "/agent/current-url", "/ws/device", "/share/public", "/share/preview"}

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
					return "", fmt.Errorf("%d: unrecognized #@build condition: %q", lineno, cond)
				}
				stack = append(stack, mm[1] == profile)
			case body == "else":
				if len(stack) == 0 {
					return "", fmt.Errorf("%d: orphaned #@build else", lineno)
				}
				stack[len(stack)-1] = !stack[len(stack)-1]
			case body == "end":
				if len(stack) == 0 {
					return "", fmt.Errorf("%d: orphaned #@build end", lineno)
				}
				stack = stack[:len(stack)-1]
			default:
				return "", fmt.Errorf("%d: unrecognized #@build directive: %q", lineno, body)
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
		return "", fmt.Errorf("#@build is not closed; %d levels remain", len(stack))
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
		"console.log('" + label + ": " + strconv.Itoa(utf8.RuneCountInString(src)) + " characters, syntax OK');\n"
	cmd := exec.Command("node", "-e", code)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return fmt.Errorf("%s syntax check failed: %s", label, detail)
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
		return "", fmt.Errorf("document is missing the application section")
	}

	if got := str(app["subdomain"]); got != wantSubdomain {
		return "", fmt.Errorf("subdomain: expected %s, got %s (incorrect #@build trimming?)", wantSubdomain, got)
	}
	fmt.Printf("    subdomain = %s\n", wantSubdomain)

	if routes := app["routes"]; routes == nil || !equalStringList(routes, []string{"/=http://nexterm-server:8080/"}) {
		return "", fmt.Errorf("routes must contain exactly one short service-name route (a fully qualified name hardcodes the package ID): %v", routes)
	}
	fmt.Println("    routes = 1 short service-name route")

	if !equalStringList(app["public_path"], expectedPublicPath) {
		return "", fmt.Errorf("public_path must contain exactly the %d device-agent contract paths (widening grants the gateway header to anonymous requests; "+
			"narrowing prevents the device agent from reaching the server): %v", len(expectedPublicPath), app["public_path"])
	}
	fmt.Printf("    public_path = %v\n", expectedPublicPath)

	injects, ok := app["injects"].([]interface{})
	if !ok || len(injects) != 3 {
		return "", fmt.Errorf("injects must contain exactly 3 entries (gateway header + request bridge + browser chooser): %v", app["injects"])
	}
	byID := map[string]map[string]interface{}{}
	for _, item := range injects {
		inject := asMap(item)
		byID[str(inject["id"])] = inject
	}
	for _, id := range []string{bridgeID, chooserID, gatewayID} {
		if byID[id] == nil {
			return "", fmt.Errorf("incorrect inject id: %v", sortedKeys(byID))
		}
	}
	if len(byID) != 3 {
		return "", fmt.Errorf("incorrect inject id: %v", sortedKeys(byID))
	}

	gateway := byID[gatewayID]
	if str(gateway["on"]) != "request" {
		return "", fmt.Errorf("%s.on must be request", gatewayID)
	}
	if raw, present := gateway["auth_required"]; present {
		if b, ok := raw.(bool); !ok || !b {
			return "", fmt.Errorf("%s.auth_required must be true (false injects the gateway secret for anonymous requests too)", gatewayID)
		}
	}
	if !equalStringList(gateway["when"], []string{"/*"}) {
		return "", fmt.Errorf("%s.when must be [/*]: %v", gatewayID, gateway["when"])
	}
	gatewayScripts := asList(gateway["do"])
	if len(gatewayScripts) != 1 {
		return "", fmt.Errorf("%s.do must contain exactly one script: %v", gatewayID, gateway["do"])
	}
	gatewayScript := asMap(gatewayScripts[0])
	gatewaySrc, _ := gatewayScript["src"].(string)
	if !strings.Contains(gatewaySrc, "ctx.headers.set") || !strings.Contains(gatewaySrc, "ctx.params.key") {
		return "", fmt.Errorf("the gateway-header script must set X-NexTerm-Gateway-Auth from ctx.params.key")
	}
	gatewayKey := str(asMap(gatewayScript["params"])["key"])
	seedMatch := seedRe.FindStringSubmatch(gatewayKey)
	if seedMatch == nil {
		return "", fmt.Errorf("%s.params.key must be a stable_secret template: %q", gatewayID, gatewayKey)
	}
	environment := environmentOf(doc)
	expectedEnv := `NEXTERM_GATEWAY_AUTH={{ stable_secret "` + seedMatch[1] + `" }}`
	if !containsString(environment, expectedEnv) {
		return "", fmt.Errorf("services environment variables must use the same stable_secret seed (missing %q): %v", expectedEnv, environment)
	}
	fmt.Printf("    %s: on=request, auth_required=true, params.key shares the NEXTERM_GATEWAY_AUTH seed\n", gatewayID)
	if err := checkJS(gatewaySrc, gatewayID); err != nil {
		return "", err
	}

	if !containsString(environment, masterKeyTransportEnv) {
		return "", fmt.Errorf("services environment variables must retain %q as the platform injection channel (the image entrypoint writes it to a 0600 key file): %v",
			masterKeyTransportEnv, environment)
	}
	for _, entry := range environment {
		if strings.HasPrefix(str(entry), "NEXTERM_MASTER_KEY=") {
			return "", fmt.Errorf("do not inject the deprecated NEXTERM_MASTER_KEY directly into the service environment (the entrypoint writes a 0600 file and "+
				"passes it to the server via NEXTERM_MASTER_KEY_FILE): %v", environment)
		}
	}
	fmt.Printf("    master key: %s is platform-injected; deprecated NEXTERM_MASTER_KEY is no longer injected directly\n", masterKeyTransport)

	bridge := byID[bridgeID]
	if str(bridge["on"]) != "request" {
		return "", fmt.Errorf("%s.on must be request", bridgeID)
	}
	if !equalStringList(bridge["when"], []string{"/__lazycat_file_bridge/*"}) {
		return "", fmt.Errorf("%s.when must be [/__lazycat_file_bridge/*]: %v", bridgeID, bridge["when"])
	}
	var bridgeSrc string
	if do := asList(bridge["do"]); len(do) > 0 {
		bridgeSrc, _ = asMap(do[0])["src"].(string)
	}
	if !strings.Contains(bridgeSrc, "ctx.proxy.to") {
		return "", fmt.Errorf("bridge script is incomplete")
	}
	prefixMatch := bridgePrefix.FindStringSubmatch(bridgeSrc)
	if prefixMatch == nil {
		return "", fmt.Errorf("bridgePrefix not found in bridge script")
	}
	bridgePrefixValue := prefixMatch[1]
	fmt.Printf("    %s: on=request, bridgePrefix=%s\n", bridgeID, bridgePrefixValue)
	if err := checkJS(bridgeSrc, bridgeID); err != nil {
		return "", err
	}

	chooser := byID[chooserID]
	if str(chooser["on"]) != "browser" {
		return "", fmt.Errorf("%s.on must be browser", chooserID)
	}
	if !equalStringList(chooser["when"], []string{"/*"}) {
		return "", fmt.Errorf("%s.when must be [/*]: %v", chooserID, chooser["when"])
	}
	scripts := asList(chooser["do"])
	if len(scripts) != 1 {
		return "", fmt.Errorf("%s.do must contain exactly one script: %v", chooserID, chooser["do"])
	}
	uri, _ := asMap(scripts[0])["src"].(string)
	if !strings.HasPrefix(uri, contentPre) {
		return "", fmt.Errorf("%s script must be bundled (%s...), got %q", chooserID, contentPre, uri)
	}
	params := asMap(asMap(scripts[0])["params"])
	if str(params["fileBridgeRoot"]) != bridgePrefixValue {
		return "", fmt.Errorf("params.fileBridgeRoot (%q) must match bridgePrefix (%q)",
			str(params["fileBridgeRoot"]), bridgePrefixValue)
	}
	fmt.Printf("    %s: on=browser, fileBridgeRoot matches bridgePrefix\n", chooserID)
	return uri, nil
}

func assertEntrypointWrapper(wrapper, dockerfile string) error {
	if !strings.Contains(dockerfile, `ENTRYPOINT ["/usr/local/bin/nexterm-server-entrypoint"]`) {
		return fmt.Errorf("Dockerfile ENTRYPOINT must point to the entrypoint wrapper")
	}
	if !strings.Contains(wrapper, "unset "+masterKeyTransport) {
		return fmt.Errorf("entrypoint must unset %s before exec, otherwise the key remains in the process environment", masterKeyTransport)
	}
	if !strings.Contains(wrapper, `NEXTERM_MASTER_KEY_FILE="$data_dir/master.key"`) ||
		!strings.Contains(wrapper, "export NEXTERM_MASTER_KEY_FILE") {
		return fmt.Errorf("entrypoint must pass the key-file path to the server via NEXTERM_MASTER_KEY_FILE")
	}
	if !strings.Contains(wrapper, "chmod 0600") {
		return fmt.Errorf("entrypoint must set key-file permissions to 0600")
	}
	if !strings.Contains(wrapper, "umask 077") {
		return fmt.Errorf("entrypoint must set umask 077 before writing the key file")
	}
	if !strings.Contains(wrapper, "exec /usr/local/bin/nexterm-server") {
		return fmt.Errorf("entrypoint must exec the real server")
	}
	fmt.Println("    entrypoint: writes the transport variable to a 0600 key file, hands off via NEXTERM_MASTER_KEY_FILE, then unsets it and execs the server")
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
		fatalf("self-test fixture does not contain %s inject", gatewayID)
	}

	serverEnv := func(doc map[string]interface{}) map[string]interface{} {
		return asMap(asMap(doc["services"])["nexterm-server"])
	}

	negatives := []struct {
		name   string
		mutate func(doc map[string]interface{})
	}{
		{"public_path widened", func(doc map[string]interface{}) {
			widened := make([]interface{}, 0, len(expectedPublicPath)+1)
			for _, p := range expectedPublicPath {
				widened = append(widened, p)
			}
			asMap(doc["application"])["public_path"] = append(widened, "/rpc")
		}},
		{"public_path removed", func(doc map[string]interface{}) {
			asMap(doc["application"])["public_path"] = []interface{}{}
		}},
		{"auth_required: false", func(doc map[string]interface{}) {
			injects := asList(asMap(doc["application"])["injects"])
			asMap(injects[gatewayIndex])["auth_required"] = false
		}},
		{"hardcoded gateway key", func(doc map[string]interface{}) {
			injects := asList(asMap(doc["application"])["injects"])
			params := asMap(asMap(asList(asMap(injects[gatewayIndex])["do"])[0])["params"])
			params["key"] = "hardcoded-secret"
		}},
		{"env seed mismatch", func(doc map[string]interface{}) {
			env := serverEnv(doc)
			entries := asList(env["environment"])
			for i, entry := range entries {
				s := str(entry)
				if strings.HasPrefix(s, "NEXTERM_GATEWAY_AUTH=") {
					entries[i] = strings.ReplaceAll(s, `stable_secret "gateway_auth"`, `stable_secret "other_seed"`)
				}
			}
		}},
		{"deprecated NEXTERM_MASTER_KEY injected directly", func(doc map[string]interface{}) {
			env := serverEnv(doc)
			env["environment"] = append(asList(env["environment"]), "NEXTERM_MASTER_KEY=hardcoded-secret")
		}},
		{"master-key injection channel removed", func(doc map[string]interface{}) {
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
		fatalf("positive case failed: %v", err)
	}
	fmt.Println("    positive case passed")
	for _, negative := range negatives {
		doc := parseBase()
		negative.mutate(doc)
		if _, err := assertProfile(doc, "nexterm"); err != nil {
			fmt.Printf("    negative case passed (%s rejected)\n", negative.name)
			continue
		}
		fatalf("negative case was not detected: %s should have failed the assertion", negative.name)
	}

	wrapperNegatives := []struct {
		name       string
		wrapper    string
		dockerfile string
	}{
		{"entrypoint misses unset of transport variable", strings.ReplaceAll(wrapper, "unset "+masterKeyTransport, "true"), dockerfile},
		{"entrypoint misses 0600", strings.ReplaceAll(wrapper, "chmod 0600", "chmod 0644"), dockerfile},
		{"entrypoint misses umask", strings.ReplaceAll(wrapper, "umask 077", "umask 022"), dockerfile},
		{"entrypoint misses NEXTERM_MASTER_KEY_FILE", strings.ReplaceAll(wrapper, `NEXTERM_MASTER_KEY_FILE="$data_dir/master.key"`, "true"), dockerfile},
		{"Dockerfile bypasses entrypoint", wrapper, strings.ReplaceAll(dockerfile, `ENTRYPOINT ["/usr/local/bin/nexterm-server-entrypoint"]`, `ENTRYPOINT ["/usr/local/bin/nexterm-server"]`)},
	}
	if err := assertEntrypointWrapper(wrapper, dockerfile); err != nil {
		fatalf("positive case failed: %v", err)
	}
	for _, negative := range wrapperNegatives {
		if err := assertEntrypointWrapper(negative.wrapper, negative.dockerfile); err != nil {
			fmt.Printf("    negative case passed (%s rejected)\n", negative.name)
			continue
		}
		fatalf("negative case was not detected: %s should have failed the assertion", negative.name)
	}
}

func main() {
	root := repoRoot()
	manifestPath := filepath.Join(root, "lazycat", "lzc-manifest.yml")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		fatalf("cannot find %s", manifestPath)
	}

	fmt.Println("== self-test (negative cases must be rejected) ==")
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

	fmt.Println("== in-package path consistency ==")
	rel := chooserURI[len(contentPre):]
	repoRel := strings.Replace(rel, "lazycat-injects/", "injects/", 1)
	buildSh, err := os.ReadFile(filepath.Join(root, "lazycat", "image", "build-server.sh"))
	if err != nil {
		fatalf("%v", err)
	}
	if !copyInjects.MatchString(string(buildSh)) {
		fatalf("build-server.sh does not copy injects/*.js into $CONTENT/lazycat-injects/")
	}
	fmt.Printf("    manifest reference   %s\n", rel)
	fmt.Println("    build-server.sh copies to $CONTENT/lazycat-injects/")
	src := filepath.Join(root, "lazycat", filepath.FromSlash(repoRel))
	info, err := os.Stat(src)
	if err != nil {
		fatalf("bundled script is missing from the repository: %s", src)
	}
	fmt.Printf("    repository source file exists (%d bytes)\n", info.Size())

	fmt.Println("== master-key injection chain ==")
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

	fmt.Println("\nall assertions passed.")
}
