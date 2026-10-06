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

func PreviewSSHConfig(result *Result, existing []ExistingAsset, existingKeys []ExistingKey) *ImportPreview {
	preview := &ImportPreview{Source: "ssh-config", Diagnostics: result.Diagnostics}
	existingByName, existingByEndpoint := indexExistingAssets(existing)
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
		planHostAction(&item, existingByName, existingByEndpoint)
		hosts = append(hosts, item)
	}
	validateProxyJumps(hosts, existing)
	preview.Hosts = hosts
	preview.Keys = planConfigKeys(preview, hosts, existingKeys)
	return preview
}

func planHostAction(item *HostPreview, existingByName map[string]ExistingAsset, existingByEndpoint map[endpoint]ExistingAsset) {
	if conflict, ok := existingByName[strings.ToLower(item.Alias)]; ok {
		if sameEndpoint(conflict, item.Hostname, item.Port, item.Username) {
			item.Action = PlanSkipDuplicate
		} else {
			item.Action = PlanConflictAlias
			item.Warnings = append(item.Warnings, fmt.Sprintf("alias %q already used by an existing asset with a different endpoint", item.Alias))
		}
		return
	}
	if conflict, ok := existingByEndpoint[endpointOf(item.Hostname, item.Port, item.Username)]; ok {
		item.Action = PlanConflictEndpoint
		item.Warnings = append(item.Warnings, fmt.Sprintf("endpoint %s:%d already imported as %q", item.Hostname, item.Port, conflict.Name))
	}
}

func planConfigKeys(preview *ImportPreview, hosts []HostPreview, existingKeys []ExistingKey) []KeyPreview {
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

	existingByName, existingByFP := indexExistingKeys(existingKeys)
	seenFingerprints := map[string]bool{}
	keys := make([]KeyPreview, 0, len(paths))
	for _, path := range paths {
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
			Action:      PlanAdd,
		}
		decideKeyAction(&item, seenFingerprints, existingByName, existingByFP)
		seenFingerprints[info.Fingerprint] = true
		keys = append(keys, item)
	}
	return keys
}

func hasKeyName(byName map[string]ExistingKey, name string) bool {
	_, ok := byName[strings.ToLower(name)]
	return ok
}

func decideKeyAction(item *KeyPreview, seenFingerprints map[string]bool, existingByName map[string]ExistingKey, existingByFingerprint map[string]bool) {
	name := ""
	if len(item.Aliases) > 0 {
		name = item.Aliases[0]
	}
	switch {
	case seenFingerprints[item.Fingerprint]:
		item.Action = PlanSkipDuplicate
	case item.Fingerprint != "" && existingByFingerprint[item.Fingerprint]:
		item.Action = PlanSkipDuplicate
	case hasKeyName(existingByName, name):
		item.Action = PlanConflictAlias
		item.Warnings = append(item.Warnings, fmt.Sprintf("key name %q already used by a key with a different fingerprint", name))
	}
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
	for i := range hosts {
		for _, hop := range jumpHops(hosts[i].ProxyJump) {
			if strings.EqualFold(hop, hosts[i].Alias) {
				hosts[i].Warnings = append(hosts[i].Warnings, fmt.Sprintf("proxy jump target %q is the host itself", hop))
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
}

func jumpHops(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "none") {
		return nil
	}
	fields := strings.Split(raw, ",")
	hops := make([]string, 0, len(fields))
	for _, field := range fields {
		hop := jumpHostPart(field)
		if hop != "" {
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
