package sshconfig

import (
	"fmt"
	"slices"
	"strings"
)

type PlanAction string

const (
	PlanAdd              PlanAction = "add"
	PlanSkipDuplicate    PlanAction = "skip-duplicate"
	PlanConflictAlias    PlanAction = "conflict-alias"
	PlanConflictEndpoint PlanAction = "conflict-endpoint"
	PlanBlockedJump      PlanAction = "blocked-jump"
)

type ExistingAsset struct {
	Name     string
	Host     string
	Port     int32
	Username string
}

type ExistingKey struct {
	Name        string
	Fingerprint string
	Path        string
}

type HostPreview struct {
	Alias         string
	Hostname      string
	Port          int
	Username      string
	IdentityFiles []string
	KeyName       string
	ProxyJump     string
	AuthMethod    string
	Source        string
	Action        PlanAction
	Warnings      []string
}

type KeyPreview struct {
	Aliases     []string
	Fingerprint string
	KeyType     string
	Path        string
	Source      string
	Action      PlanAction
	Warnings    []string
}

type ImportPreview struct {
	Source      string
	Hosts       []HostPreview
	Keys        []KeyPreview
	Diagnostics []Diagnostic
	Truncated   bool
}

type endpoint struct {
	host     string
	port     int
	username string
}

func PreviewSSHConfig(result *Result, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) *ImportPreview {
	limits = withDefaultLimits(limits)
	preview := &ImportPreview{Source: "ssh-config", Diagnostics: result.Diagnostics}
	existingByName, existingByEndpoint := indexExistingAssets(existing)
	batchNames := map[string]endpoint{}
	batchEndpoints := map[endpoint]string{}
	hosts := make([]HostPreview, 0, len(result.Hosts))
	for _, host := range result.Hosts {
		item := HostPreview{
			Alias:         host.Alias,
			Hostname:      host.Hostname,
			Port:          host.Port,
			Username:      host.Username,
			IdentityFiles: DedupeIdentityFiles(host.IdentityFiles),
			ProxyJump:     host.ProxyJump,
			Source:        host.Source,
			Action:        PlanAdd,
		}
		if len(item.IdentityFiles) > 0 {
			item.AuthMethod = "key"
		}
		planHostAction(&item, existingByName, existingByEndpoint, batchNames, batchEndpoints)
		ep := endpointOf(item.Hostname, item.Port, item.Username)
		if _, ok := batchNames[strings.ToLower(item.Alias)]; !ok {
			batchNames[strings.ToLower(item.Alias)] = ep
		}
		if _, ok := batchEndpoints[ep]; !ok {
			batchEndpoints[ep] = item.Alias
		}
		hosts = append(hosts, item)
	}
	validateProxyJumps(hosts, existing)
	preview.Hosts = hosts
	preview.Keys = planConfigKeys(preview, hosts, existingKeys, limits)
	return preview
}

func planHostAction(item *HostPreview, existingByName map[string]ExistingAsset, existingByEndpoint map[endpoint]ExistingAsset, batchNames map[string]endpoint, batchEndpoints map[endpoint]string) {
	lower := strings.ToLower(item.Alias)
	ep := endpointOf(item.Hostname, item.Port, item.Username)
	if conflict, ok := existingByName[lower]; ok {
		if sameEndpoint(conflict, item.Hostname, item.Port, item.Username) {
			item.Action = PlanSkipDuplicate
		} else {
			item.Action = PlanConflictAlias
			item.Warnings = append(item.Warnings, fmt.Sprintf("alias %q already used by an existing asset with a different endpoint", item.Alias))
		}
		return
	}
	if conflict, ok := existingByEndpoint[ep]; ok {
		item.Action = PlanConflictEndpoint
		item.Warnings = append(item.Warnings, fmt.Sprintf("endpoint %s:%d already imported as %q", item.Hostname, item.Port, conflict.Name))
		return
	}
	if prevEp, ok := batchNames[lower]; ok {
		if prevEp == ep {
			item.Action = PlanSkipDuplicate
			item.Warnings = append(item.Warnings, fmt.Sprintf("alias %q duplicates an earlier entry in this import", item.Alias))
		} else {
			item.Action = PlanConflictAlias
			item.Warnings = append(item.Warnings, fmt.Sprintf("alias %q is also used by a different host in this import", item.Alias))
		}
		return
	}
	if _, ok := batchEndpoints[ep]; ok {
		item.Action = PlanConflictEndpoint
		item.Warnings = append(item.Warnings, fmt.Sprintf("endpoint %s:%d appears more than once in this import", item.Hostname, item.Port))
	}
}

func planConfigKeys(preview *ImportPreview, hosts []HostPreview, existingKeys []ExistingKey, limits Limits) []KeyPreview {
	byPath := map[string]bool{}
	for _, host := range hosts {
		for _, path := range host.IdentityFiles {
			byPath[path] = true
		}
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	slices.Sort(paths)

	existingByName, existingFPNames := indexExistingKeys(existingKeys)
	seenFingerprints := map[string]bool{}
	batchNames := map[string]bool{}
	keys := make([]KeyPreview, 0, len(paths))
	inspected := 0
	for _, path := range paths {
		if inspected >= limits.MaxKeys {
			truncate(preview, "key count exceeds %d", limits.MaxKeys)
			break
		}
		inspected++
		info, err := InspectKeyFile(path)
		if err != nil {
			preview.Diagnostics = append(preview.Diagnostics, Diagnostic{
				Code:    "key-unreadable",
				Source:  path,
				Message: "identity file cannot be inspected",
			})
			continue
		}
		item := KeyPreview{
			Aliases:     []string{info.Name},
			Fingerprint: info.Fingerprint,
			KeyType:     info.KeyType,
			Path:        path,
			Source:      "ssh-config",
			Action:      PlanAdd,
		}
		decideKeyAction(&item, seenFingerprints, existingByName, existingFPNames, batchNames)
		seenFingerprints[info.Fingerprint] = true
		if len(item.Aliases) > 0 {
			batchNames[strings.ToLower(item.Aliases[0])] = true
		}
		keys = append(keys, item)
	}
	return keys
}

func decideKeyAction(item *KeyPreview, seenFingerprints map[string]bool, existingByName map[string]ExistingKey, existingFPNames map[string]string, batchNames map[string]bool) {
	name := ""
	if len(item.Aliases) > 0 {
		name = item.Aliases[0]
	}
	switch {
	case seenFingerprints[item.Fingerprint]:
		item.Action = PlanSkipDuplicate
	case item.Fingerprint != "" && existingFPNames[item.Fingerprint] != "":
		item.Action = PlanSkipDuplicate
	case hasKeyName(existingByName, name):
		item.Action = PlanConflictAlias
		item.Warnings = append(item.Warnings, fmt.Sprintf("key name %q already used by a key with a different fingerprint", name))
	case batchNames[strings.ToLower(name)]:
		item.Action = PlanConflictAlias
		item.Warnings = append(item.Warnings, fmt.Sprintf("key name %q is also used by a different key in this import", name))
	}
}

func hasKeyName(byName map[string]ExistingKey, name string) bool {
	_, ok := byName[strings.ToLower(name)]
	return ok
}

func validateProxyJumps(hosts []HostPreview, existing []ExistingAsset) {
	byAlias := map[string]int{}
	for i := range hosts {
		byAlias[strings.ToLower(hosts[i].Alias)] = i
	}
	existingNames := map[string]bool{}
	for _, asset := range existing {
		existingNames[strings.ToLower(asset.Name)] = true
	}
	edges := make([][]int, len(hosts))
	blocked := make([]bool, len(hosts))
	for i := range hosts {
		for _, hop := range jumpHops(hosts[i].ProxyJump) {
			if strings.EqualFold(hop, hosts[i].Alias) {
				hosts[i].Warnings = append(hosts[i].Warnings, fmt.Sprintf("proxy jump target %q is the host itself", hop))
				blocked[i] = true
				continue
			}
			if target, ok := byAlias[strings.ToLower(hop)]; ok {
				edges[i] = append(edges[i], target)
				continue
			}
			if existingNames[strings.ToLower(hop)] {
				continue
			}
			hosts[i].Warnings = append(hosts[i].Warnings, fmt.Sprintf("proxy jump target %q matches no imported or existing host", hop))
			blocked[i] = true
		}
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make([]int, len(hosts))
	var stack []int
	var visit func(int)
	visit = func(node int) {
		color[node] = gray
		stack = append(stack, node)
		for _, next := range edges[node] {
			if color[next] == gray {
				start := 0
				for i, n := range stack {
					if n == next {
						start = i
						break
					}
				}
				for _, n := range stack[start:] {
					warning := fmt.Sprintf("proxy jump cycle through %q", hosts[next].Alias)
					if !containsString(hosts[n].Warnings, warning) {
						hosts[n].Warnings = append(hosts[n].Warnings, warning)
					}
					blocked[n] = true
				}
				continue
			}
			if color[next] == white {
				visit(next)
			}
		}
		stack = stack[:len(stack)-1]
		color[node] = black
	}
	for i := range hosts {
		if color[i] == white {
			visit(i)
		}
	}
	for {
		changed := false
		for i := range hosts {
			if blocked[i] {
				continue
			}
			for _, target := range edges[i] {
				if !blocked[target] && importableAction(hosts[target].Action) {
					continue
				}
				blocked[i] = true
				hosts[i].Warnings = append(hosts[i].Warnings, fmt.Sprintf("proxy jump target %q is not importable", hosts[target].Alias))
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	for i := range hosts {
		if blocked[i] && hosts[i].Action == PlanAdd {
			hosts[i].Action = PlanBlockedJump
		}
	}
}

func importableAction(action PlanAction) bool {
	return action == PlanAdd || action == PlanSkipDuplicate
}

func jumpHops(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "none") {
		return nil
	}
	fields := strings.Split(raw, ",")
	hops := make([]string, 0, len(fields))
	for _, field := range fields {
		if hop := jumpHostPart(field); hop != "" {
			hops = append(hops, hop)
		}
	}
	return hops
}

func jumpHostPart(hop string) string {
	hop = strings.TrimSpace(hop)
	if at := strings.LastIndex(hop, "@"); at >= 0 {
		hop = hop[at+1:]
	}
	if strings.HasPrefix(hop, "[") {
		if end := strings.Index(hop, "]"); end > 0 {
			return hop[1:end]
		}
	}
	if colon := strings.LastIndex(hop, ":"); colon > 0 {
		hop = hop[:colon]
	}
	return hop
}

func endpointOf(host string, port int, username string) endpoint {
	return endpoint{
		host:     strings.ToLower(strings.TrimSpace(host)),
		port:     port,
		username: strings.ToLower(strings.TrimSpace(username)),
	}
}

func sameEndpoint(asset ExistingAsset, host string, port int, username string) bool {
	return endpointOf(asset.Host, int(asset.Port), asset.Username) == endpointOf(host, port, username)
}
