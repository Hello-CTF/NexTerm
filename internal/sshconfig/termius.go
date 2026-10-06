package sshconfig

import (
	"errors"
	"fmt"
	"strings"

	"github.com/huangzheng2016/termius_exporter/pkg/exporter"
	"github.com/huangzheng2016/termius_exporter/pkg/parser"
)

var ErrConfirmationRequired = errors.New("sshconfig: reading local Termius data requires explicit user confirmation")

type TermiusOptions struct {
	DBPath    string
	Confirmed bool
}

// PreviewTermius synchronously reads the local Termius database and builds an
// import preview. It must only be called after the user explicitly confirmed
// the read; never invoke it from background scans. Private key material and
// passwords from the Termius data are never retained in the preview.
func PreviewTermius(opts TermiusOptions, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) (*ImportPreview, error) {
	if !opts.Confirmed {
		return nil, ErrConfirmationRequired
	}
	hostRecords, keyRecords, err := exporter.Export(opts.DBPath)
	if err != nil {
		return nil, err
	}
	return buildTermiusPreview(hostRecords, keyRecords, existing, existingKeys, withDefaultLimits(limits)), nil
}

func buildTermiusPreview(hostRecords []parser.HostRecord, keyRecords []parser.KeyRecord, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) *ImportPreview {
	preview := &ImportPreview{Source: "termius"}
	existingByName, existingByEndpoint := indexExistingAssets(existing)
	for _, rec := range hostRecords {
		if len(preview.Hosts) >= limits.MaxHosts {
			truncate(preview, "host count exceeds %d", limits.MaxHosts)
			break
		}
		alias := ""
		if len(rec.Aliases) > 0 {
			alias = sanitizeValue(rec.Aliases[0])
		}
		port := rec.Port
		if port == 0 {
			port = 22
		}
		if alias == "" {
			alias = fmt.Sprintf("%s@%s:%d", rec.Username, rec.Host, port)
		}
		item := HostPreview{
			Alias:    alias,
			Hostname: sanitizeValue(rec.Host),
			Port:     port,
			Username: sanitizeValue(rec.Username),
			KeyName:  sanitizeValue(rec.KeyName),
			Source:   "termius",
			Action:   PlanAdd,
		}
		switch {
		case item.KeyName != "":
			item.AuthMethod = "key"
		case rec.Password != "":
			item.AuthMethod = "password"
		default:
			item.AuthMethod = "agent"
		}
		planHostAction(&item, existingByName, existingByEndpoint)
		preview.Hosts = append(preview.Hosts, item)
	}

	existingKeyNames, existingKeyFPs := indexExistingKeys(existingKeys)
	seenFingerprints := map[string]bool{}
	for _, rec := range keyRecords {
		if len(preview.Keys) >= limits.MaxKeys {
			truncate(preview, "key count exceeds %d", limits.MaxKeys)
			break
		}
		fingerprint, keyType, err := FingerprintPrivateKey([]byte(rec.PrivateKey))
		if err != nil {
			preview.Diagnostics = append(preview.Diagnostics, Diagnostic{
				Code:    "termius-key-unparsed",
				Source:  "termius",
				Message: "a Termius key could not be parsed and is excluded from the preview",
			})
			continue
		}
		aliases := make([]string, 0, len(rec.Aliases))
		for _, alias := range rec.Aliases {
			if alias = sanitizeValue(alias); alias != "" {
				aliases = append(aliases, alias)
			}
		}
		if len(aliases) == 0 {
			continue
		}
		item := KeyPreview{
			Aliases:     aliases,
			Fingerprint: fingerprint,
			KeyType:     keyType,
			Source:      "termius",
			Action:      PlanAdd,
		}
		decideKeyAction(&item, seenFingerprints, existingKeyNames, existingKeyFPs)
		seenFingerprints[fingerprint] = true
		preview.Keys = append(preview.Keys, item)
	}
	return preview
}

func indexExistingAssets(existing []ExistingAsset) (map[string]ExistingAsset, map[endpoint]ExistingAsset) {
	byName := map[string]ExistingAsset{}
	byEndpoint := map[endpoint]ExistingAsset{}
	for _, asset := range existing {
		byName[strings.ToLower(asset.Name)] = asset
		byEndpoint[endpointOf(asset.Host, int(asset.Port), asset.Username)] = asset
	}
	return byName, byEndpoint
}

func indexExistingKeys(existing []ExistingKey) (map[string]ExistingKey, map[string]bool) {
	byName := map[string]ExistingKey{}
	byFingerprint := map[string]bool{}
	for _, key := range existing {
		byName[strings.ToLower(key.Name)] = key
		if key.Fingerprint != "" {
			byFingerprint[key.Fingerprint] = true
		}
	}
	return byName, byFingerprint
}

func truncate(preview *ImportPreview, format string, args ...interface{}) {
	if preview.Truncated {
		return
	}
	preview.Truncated = true
	preview.Diagnostics = append(preview.Diagnostics, Diagnostic{
		Code:    "limit-truncated",
		Source:  preview.Source,
		Message: fmt.Sprintf(format, args...),
	})
}
