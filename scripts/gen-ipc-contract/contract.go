package main

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

const ipcPackagePath = "github.com/Hello-CTF/NexTerm/internal/ipc"

type scopeSpec struct {
	name     string
	prefixes []string
	commands []string
	deferred bool
}

var contractScopes = []scopeSpec{
	{name: "platform", commands: []string{"app_platform", "desktop_tray_get", "desktop_tray_set", "system_insets"}},
	{name: "update", commands: []string{"app_update_check", "app_update_install", "app_restart"}},
	{name: "biometric", commands: []string{"biometric_status", "biometric_unlock", "biometric_enable", "biometric_disable"}},
	{name: "host-share", prefixes: []string{"host_share_"}},
	{name: "exec-batch", prefixes: []string{"exec_batch_"}},
	{name: "target-set", prefixes: []string{"target_set_"}},
	{name: "runbook", prefixes: []string{"runbook_"}},
	{name: "forward", commands: []string{
		"forward_create",
		"forward_create_remote",
		"forward_create_socks",
		"forward_env",
		"forward_list",
		"forward_remove",
		"forward_retry",
	}},
	{name: "forward-rule", prefixes: []string{"forward_rule_"}},
	{name: "diagnostics", prefixes: []string{"diagnostics_"}},
	{name: "services", prefixes: []string{"services_"}},
	{name: "inventory", prefixes: []string{"inventory_"}},
	{name: "production", prefixes: []string{"production_"}},
	{name: "remote", prefixes: []string{"remote_"}},
	{name: "preview-share", prefixes: []string{"preview_share_"}},
	{name: "sync", commands: []string{
		"sync_apply_objects",
		"sync_bundle_read",
		"sync_bundle_write",
		"sync_collect_ai_grants",
		"sync_collect_ai_permission",
		"sync_collect_ai_profiles",
		"sync_collect_assets",
		"sync_collect_credentials",
		"sync_collect_known_hosts",
		"sync_collect_preferences",
		"sync_collect_tombstones",
		"sync_compare",
		"sync_digest",
		"sync_export",
		"sync_import",
		"sync_kind_opt_in_get",
		"sync_kind_opt_in_set",
		"sync_link_get",
		"sync_link_mfa",
		"sync_link_reset_password",
		"sync_link_set",
		"sync_preferences_get",
		"sync_preferences_set",
		"sync_repair_undecryptable",
		"sync_status",
		"sync_wake",
	}},
	{name: "transcript", commands: []string{
		"transcript_delete",
		"transcript_hosts",
		"transcript_list",
		"transcript_read",
		"transcript_search",
		"transcript_sync_opt_in",
	}},
	{name: "cron", prefixes: []string{"cron_"}},
	{name: "log-tail", prefixes: []string{"log_tail_"}},
	{name: "asset", commands: []string{
		"asset_create",
		"asset_delete",
		"asset_get",
		"asset_list",
		"asset_probe_batch",
		"asset_read_key_file",
		"asset_search",
		"asset_search_meta",
		"asset_update",
	}},
	{name: "group", commands: []string{"group_create", "group_delete", "group_list", "group_update"}},
	{name: "known-host", commands: []string{"known_host_accept", "known_host_list", "known_host_remove"}},
	{name: "snippet", commands: []string{"snippet_create", "snippet_delete", "snippet_list", "snippet_update"}},
	{name: "audit", commands: []string{"audit_count", "audit_query"}},
	{name: "command-log", commands: []string{"command_count", "command_query"}},
	{name: "layout", commands: []string{"layout_get", "layout_put"}},
	{name: "ssh-import", commands: []string{"ssh_import_apply", "ssh_import_preview"}},
	{name: "vault", prefixes: []string{"vault_"}},
	{name: "credential", commands: []string{"credential_update"}},
	{name: "ai-roster", prefixes: []string{"ai_roster_"}},
	{name: "ai-watch", prefixes: []string{"ai_watch_"}},
	{name: "ai-run", commands: []string{"ai_run_history", "ai_run_recovery_summary"}},
	{name: "ai-skills", prefixes: []string{"ai_skills_"}},
	{name: "ai-grant", prefixes: []string{"ai_grant_"}},
	{name: "docker", prefixes: []string{"docker_"}},
	{name: "db", prefixes: []string{"db_"}},
	{name: "redis", prefixes: []string{"redis_"}},
	{name: "fs", prefixes: []string{"fs_"}},
	{name: "local-file", prefixes: []string{"local_file_", "local_folder_"}},
	{name: "mount", prefixes: []string{"mount_"}},
	{name: "session", commands: []string{
		"session_connect_device",
		"session_connect_local",
		"session_disconnect",
		"session_list",
		"session_local_hostname",
		"session_open_line_tab",
		"session_quick_connect_user",
		"session_reconnect",
	}},
	{name: "terminal", commands: []string{
		"terminal_attach",
		"terminal_broadcast_prepare",
		"terminal_history_read",
		"terminal_rename_tab",
		"terminal_write",
	}},
	{name: "terminal-stream", commands: []string{"terminal_pause", "terminal_recover", "terminal_resume", "terminal_stream_ack"}},
	{name: "helper", commands: []string{"terminal_helper_operation", "terminal_helper_offline"}},
	{name: "memory-owner", deferred: true},
	{name: "ai-owner", deferred: true},
}

type commandContract struct {
	Scope   string
	Name    string
	Nested  bool
	Channel bool
	Stream  string
	Input   types.Type
	Output  types.Type
}

type channelUsage struct {
	channel       bool
	stream        bool
	jsonStream    bool
	binaryStream  bool
	unknownStream bool
}

type functionDecl struct {
	pkg  *packages.Package
	body ast.Node
}

func loadContractPackages(root string, overlay map[string][]byte) ([]*packages.Package, error) {
	config := &packages.Config{
		Dir:     root,
		Mode:    packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Overlay: overlay,
		Env:     readOnlyGoEnv(),
	}
	loaded, err := packages.Load(config, "./internal/...", "./cmd/nexterm-desktop", "./cmd/nexterm-server")
	if err != nil {
		return nil, err
	}
	if len(loaded) == 0 {
		return nil, fmt.Errorf("no IPC registration packages loaded")
	}
	reachable := reachableFromBinaries(loaded)
	filtered := make([]*packages.Package, 0, len(loaded))
	for _, pkg := range loaded {
		if pkg.TypesInfo == nil {
			return nil, fmt.Errorf("IPC registration package %s has no type information", pkg.PkgPath)
		}
		// A command counts as production-registered only when its
		// registration package is reachable from a shipped binary.
		// Registration-ready packages that no binary imports yet (deferred
		// owners) stay out of the published contract and the ratchet.
		if reachable[pkg.PkgPath] {
			filtered = append(filtered, pkg)
		}
	}
	return filtered, nil
}

// productionBinaries are the shipped entrypoints whose import graphs decide
// which IPC registrations are actually wired into the running product.
var productionBinaries = []string{
	"github.com/Hello-CTF/NexTerm/cmd/nexterm-desktop",
	"github.com/Hello-CTF/NexTerm/cmd/nexterm-server",
}

// reachableFromBinaries walks the import graph from the production binaries
// and returns the set of package paths they can reach.
func reachableFromBinaries(loaded []*packages.Package) map[string]bool {
	byPath := make(map[string]*packages.Package, len(loaded))
	for _, pkg := range loaded {
		byPath[pkg.PkgPath] = pkg
	}
	reachable := make(map[string]bool)
	queue := make([]*packages.Package, 0, len(productionBinaries))
	for _, path := range productionBinaries {
		if pkg, ok := byPath[path]; ok {
			queue = append(queue, pkg)
		}
	}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if reachable[pkg.PkgPath] {
			continue
		}
		reachable[pkg.PkgPath] = true
		for _, imported := range pkg.Imports {
			queue = append(queue, imported)
		}
	}
	return reachable
}

func readOnlyGoEnv() []string {
	environment := os.Environ()
	result := make([]string, 0, len(environment)+1)
	for _, value := range environment {
		if !strings.HasPrefix(value, "GOFLAGS=") {
			result = append(result, value)
		}
	}
	return append(result, "GOFLAGS=-mod=readonly")
}

func extractContracts(loaded []*packages.Package) ([]commandContract, error) {
	declarations := make(map[*types.Func]functionDecl)
	for _, pkg := range loaded {
		for _, file := range pkg.Syntax {
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok {
					continue
				}
				object, _ := pkg.TypesInfo.Defs[function.Name].(*types.Func)
				if object != nil {
					declarations[object] = functionDecl{pkg: pkg, body: function.Body}
				}
			}
		}
	}

	nestedOverrides := registrationNestedOverrides(loaded)
	commandsByName := make(map[string]commandContract)
	seenScopes := make(map[string]bool)
	for _, pkg := range loaded {
		for _, file := range pkg.Syntax {
			var extractionErr error
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || (len(call.Args) != 3 && len(call.Args) != 4) {
					return true
				}
				registration := ""
				var names []string
				nested := false
				wrapped := false
				handlerIndex := 2
				inputIndex := 2
				parameterCount := 3
				if len(call.Args) == 3 {
					registration = registrationName(pkg, call)
					if registration != "" {
						names = commandNames(pkg, call.Args[1], loaded, declarations, nil)
						nested = registration == "RegisterNested"
					}
				} else {
					var name string
					name, nested, wrapped = databaseCommandRegistration(pkg, call)
					if wrapped {
						if name != "" {
							names = []string{name}
						}
						handlerIndex = 3
						inputIndex = 1
						parameterCount = 2
					}
				}
				if registration == "" && !wrapped {
					return true
				}
				if len(names) == 0 {
					extractionErr = fmt.Errorf("non-constant IPC command registration at %s", pkg.Fset.Position(call.Args[1].Pos()))
					return false
				}
				for _, name := range names {
					if registration != "" && pkg.PkgPath == "github.com/Hello-CTF/NexTerm/internal/db" {
						if _, ok := nestedOverrides[name]; ok {
							continue
						}
					}
					scope, selected := selectedScope(name)
					usage := inspectChannelUsage(pkg, call.Args[handlerIndex], declarations)
					commandNested := nested
					if override, ok := nestedOverrides[name]; ok {
						commandNested = override
					}
					contract := commandContract{
						Name: name, Nested: commandNested,
						Channel: usage.channel, Stream: streamKind(usage),
					}
					if selected {
						signature, ok := pkg.TypesInfo.TypeOf(call.Args[handlerIndex]).Underlying().(*types.Signature)
						if !ok || signature.Params().Len() != parameterCount || signature.Results().Len() != 2 {
							commandsByName[name] = commandContract{Name: name, Scope: "invalid"}
							continue
						}
						contract.Scope = scope
						input, err := projectConsumerCommandInput(name, signature.Params().At(inputIndex).Type())
						if err != nil {
							extractionErr = fmt.Errorf("project %s input: %w", name, err)
							return false
						}
						contract.Input = input
						contract.Output = signature.Results().At(0).Type()
					}
					if existing, exists := commandsByName[name]; exists {
						if selected || existing.Scope != "" {
							commandsByName[name] = commandContract{Name: name, Scope: "duplicate"}
						}
						continue
					}
					commandsByName[name] = contract
					if selected {
						seenScopes[scope] = true
					}
				}
				return true
			})
			if extractionErr != nil {
				return nil, extractionErr
			}
		}
	}

	var commands []commandContract
	for _, command := range commandsByName {
		if command.Scope == "duplicate" {
			return nil, fmt.Errorf("command %q has multiple selected registrations", command.Name)
		}
		if command.Scope == "invalid" {
			return nil, fmt.Errorf("command %q does not use the IPC handler signature", command.Name)
		}
		commands = append(commands, command)
	}
	for _, scope := range contractScopes {
		if !scope.deferred && !seenScopes[scope.name] {
			return nil, fmt.Errorf("selected IPC scope %q has no registrations", scope.name)
		}
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })
	return commands, nil
}

func databaseCommandRegistration(pkg *packages.Package, call *ast.CallExpr) (string, bool, bool) {
	if pkg.PkgPath != "github.com/Hello-CTF/NexTerm/internal/db" || len(call.Args) != 4 {
		return "", false, false
	}
	function := calledFunction(pkg, call.Fun)
	if function == nil || function.Name() != "registerCommand" || function.Pkg() == nil ||
		function.Pkg().Path() != "github.com/Hello-CTF/NexTerm/internal/db" {
		return "", false, false
	}
	value := pkg.TypesInfo.Types[call.Args[2]].Value
	if value == nil || value.Kind() != constant.Bool {
		return "", false, false
	}
	return commandName(pkg, call.Args[1]), constant.BoolVal(value), true
}

func registrationNestedOverrides(loaded []*packages.Package) map[string]bool {
	overrides := make(map[string]bool)
	for _, pkg := range loaded {
		if pkg.PkgPath != "github.com/Hello-CTF/NexTerm/internal/db" {
			continue
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, nested, ok := databaseCommandRegistration(pkg, call)
				if ok && name != "" {
					overrides[name] = nested
				}
				return true
			})
		}
	}
	return overrides
}

func registrationName(pkg *packages.Package, call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	object, _ := pkg.TypesInfo.Uses[selector.Sel].(*types.Func)
	if object == nil || object.Pkg() == nil || object.Pkg().Path() != ipcPackagePath {
		return ""
	}
	if object.Name() == "Register" || object.Name() == "RegisterNested" {
		return object.Name()
	}
	return ""
}

func commandName(pkg *packages.Package, expression ast.Expr) string {
	value := pkg.TypesInfo.Types[expression].Value
	if value == nil || value.Kind() != constant.String {
		return ""
	}
	return constant.StringVal(value)
}

func commandNames(pkg *packages.Package, expression ast.Expr, loaded []*packages.Package, declarations map[*types.Func]functionDecl, visited map[*types.Var]bool) []string {
	if name := commandName(pkg, expression); name != "" {
		return []string{name}
	}
	identifier, ok := expression.(*ast.Ident)
	if !ok {
		return nil
	}
	parameter, _ := pkg.TypesInfo.Uses[identifier].(*types.Var)
	if parameter == nil {
		return nil
	}
	if visited[parameter] {
		return nil
	}
	var owner *types.Func
	parameterIndex := -1
	for function := range declarations {
		params := function.Signature().Params()
		for index := 0; index < params.Len(); index++ {
			if params.At(index) == parameter {
				owner = function
				parameterIndex = index
				break
			}
		}
		if owner != nil {
			break
		}
	}
	if owner == nil {
		return nil
	}
	if visited == nil {
		visited = make(map[*types.Var]bool)
	}
	visited[parameter] = true
	defer delete(visited, parameter)

	names := make(map[string]bool)
	for _, candidatePkg := range loaded {
		for _, file := range candidatePkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || parameterIndex >= len(call.Args) || calledFunction(candidatePkg, call.Fun) != owner {
					return true
				}
				for _, name := range commandNames(candidatePkg, call.Args[parameterIndex], loaded, declarations, visited) {
					names[name] = true
				}
				return true
			})
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func selectedScope(name string) (string, bool) {
	for _, scope := range contractScopes {
		if scope.deferred {
			continue
		}
		for _, command := range scope.commands {
			if name == command {
				return scope.name, true
			}
		}
		for _, prefix := range scope.prefixes {
			if strings.HasPrefix(name, prefix) {
				return scope.name, true
			}
		}
	}
	return "", false
}

func inspectChannelUsage(pkg *packages.Package, handler ast.Expr, declarations map[*types.Func]functionDecl) channelUsage {
	var usage channelUsage
	visited := make(map[*types.Func]bool)
	var inspect func(*packages.Package, ast.Node)
	inspect = func(owner *packages.Package, body ast.Node) {
		ast.Inspect(body, func(node ast.Node) bool {
			switch current := node.(type) {
			case *ast.SelectorExpr:
				if isIPCCall(owner.TypesInfo.TypeOf(current.X)) {
					switch current.Sel.Name {
					case "Channel":
						usage.channel = true
					case "Streams":
						usage.stream = true
						usage.unknownStream = true
					}
				}
			case *ast.CallExpr:
				object := calledFunction(owner, current.Fun)
				if object != nil && object.Pkg() != nil && object.Pkg().Path() == ipcPackagePath {
					switch object.Name() {
					case "OpenJSON":
						usage.stream = true
						usage.jsonStream = true
					case "OpenBinary":
						usage.stream = true
						usage.binaryStream = true
					}
				}
				if object != nil && !visited[object] {
					if declaration, ok := declarations[object]; ok {
						visited[object] = true
						inspect(declaration.pkg, declaration.body)
					}
				}
			}
			return true
		})
	}

	switch current := handler.(type) {
	case *ast.FuncLit:
		inspect(pkg, current.Body)
	default:
		if object := calledFunction(pkg, handler); object != nil {
			if declaration, ok := declarations[object]; ok {
				visited[object] = true
				inspect(declaration.pkg, declaration.body)
			}
		}
	}
	return usage
}

func calledFunction(pkg *packages.Package, expression ast.Expr) *types.Func {
	switch current := expression.(type) {
	case *ast.Ident:
		object, _ := pkg.TypesInfo.Uses[current].(*types.Func)
		return object
	case *ast.SelectorExpr:
		if selection := pkg.TypesInfo.Selections[current]; selection != nil {
			object, _ := selection.Obj().(*types.Func)
			return object
		}
		object, _ := pkg.TypesInfo.Uses[current.Sel].(*types.Func)
		return object
	case *ast.IndexExpr:
		return calledFunction(pkg, current.X)
	case *ast.IndexListExpr:
		return calledFunction(pkg, current.X)
	default:
		return nil
	}
}

func isIPCCall(value types.Type) bool {
	pointer, ok := types.Unalias(value).(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := types.Unalias(pointer.Elem()).(*types.Named)
	return ok && named.Obj().Name() == "Call" && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == ipcPackagePath
}

func streamKind(usage channelUsage) string {
	if usage.jsonStream && usage.binaryStream {
		return "unknown"
	}
	if usage.jsonStream {
		return "json"
	}
	if usage.binaryStream {
		return "binary"
	}
	if usage.stream || usage.unknownStream {
		return "unknown"
	}
	return "none"
}
