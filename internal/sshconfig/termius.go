package sshconfig

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/sshconfig/termiusdb"
)

var ErrConfirmationRequired = errors.New("sshconfig: reading local Termius data requires explicit user confirmation")

type TermiusOptions struct {
	DBPath    string
	Confirmed bool
	KeySource termiusdb.KeySource
}

// PreviewTermius synchronously reads the local Termius database and builds an
// import preview. It must only be called after the user explicitly confirmed
// the read; never invoke it from background scans. With a nil KeySource the
// platform default applies, which currently supports macOS only and fails
// before any data is read elsewhere. Private key material and passwords from
// the Termius data are never retained in the preview.
func PreviewTermius(opts TermiusOptions, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) (*ImportPreview, error) {
	if !opts.Confirmed {
		return nil, ErrConfirmationRequired
	}
	keySource := opts.KeySource
	if keySource == nil {
		var err error
		if keySource, err = termiusdb.PlatformKeySource(); err != nil {
			return nil, err
		}
	}
	hostRecords, keyRecords, err := termiusdb.Export(opts.DBPath, keySource)
	if err != nil {
		return nil, err
	}
	return buildTermiusPreview(hostRecords, keyRecords, existing, existingKeys, withDefaultLimits(limits)), nil
}

func buildTermiusPreview(hostRecords []termiusdb.HostRecord, keyRecords []termiusdb.KeyRecord, existing []ExistingAsset, existingKeys []ExistingKey, limits Limits) *ImportPreview {
	preview := &ImportPreview{Source: "termius"}

	existingKeyNames, existingFPNames := indexExistingKeys(existingKeys)
	seenFingerprints := map[string]bool{}
	batchKeyNames := map[string]bool{}
	canonicalByFP := map[string]*KeyPreview{}
	canonicalByAlias := map[string]*KeyPreview{}
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
		decideKeyAction(&item, seenFingerprints, existingKeyNames, existingFPNames, batchKeyNames)
		if canonical, dup := canonicalByFP[fingerprint]; dup {
			for _, alias := range aliases {
				canonicalByAlias[strings.ToLower(alias)] = canonical
			}
		} else {
			canonical := item
			canonicalByFP[fingerprint] = &canonical
			for _, alias := range aliases {
				canonicalByAlias[strings.ToLower(alias)] = &canonical
			}
		}
		seenFingerprints[fingerprint] = true
		batchKeyNames[strings.ToLower(aliases[0])] = true
		preview.Keys = append(preview.Keys, item)
	}

	existingByName, existingByEndpoint := indexExistingAssets(existing)
	batchNames := map[string]endpoint{}
	batchEndpoints := map[endpoint]string{}
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
		planHostAction(&item, existingByName, existingByEndpoint, batchNames, batchEndpoints)
		ep := endpointOf(item.Hostname, item.Port, item.Username)
		if _, ok := batchNames[strings.ToLower(item.Alias)]; !ok {
			batchNames[strings.ToLower(item.Alias)] = ep
		}
		if _, ok := batchEndpoints[ep]; !ok {
			batchEndpoints[ep] = item.Alias
		}
		reconcileTermiusKey(&item, rec, canonicalByAlias, existingKeyNames, existingFPNames)
		preview.Hosts = append(preview.Hosts, item)
	}
	return preview
}

func reconcileTermiusKey(item *HostPreview, rec termiusdb.HostRecord, canonicalByAlias map[string]*KeyPreview, existingKeyNames map[string]ExistingKey, existingFPNames map[string]string) {
	if item.KeyName != "" {
		lower := strings.ToLower(item.KeyName)
		if canonical, ok := canonicalByAlias[lower]; ok {
			item.KeyName = canonical.Aliases[0]
			if name, ok := existingFPNames[canonical.Fingerprint]; ok && canonical.Action == PlanSkipDuplicate {
				item.Warnings = append(item.Warnings, fmt.Sprintf("key %q already exists as %q", canonical.Aliases[0], name))
				item.KeyName = name
			}
			return
		}
		if _, ok := existingKeyNames[lower]; !ok {
			item.Warnings = append(item.Warnings, fmt.Sprintf("references key %q which is not part of the import", item.KeyName))
		}
		return
	}
	if rec.KeyID != 0 {
		item.AuthMethod = "key"
		item.Warnings = append(item.Warnings, fmt.Sprintf("references key id %d which could not be resolved", rec.KeyID))
	}
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

func indexExistingKeys(existing []ExistingKey) (map[string]ExistingKey, map[string]string) {
	byName := map[string]ExistingKey{}
	fpToName := map[string]string{}
	for _, key := range existing {
		byName[strings.ToLower(key.Name)] = key
		if key.Fingerprint != "" {
			fpToName[key.Fingerprint] = key.Name
		}
	}
	return byName, fpToName
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
