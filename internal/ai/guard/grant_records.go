package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// A legacy record is a persisted device grant or grant rule in the format
// written before 1.3.0: a device grant without the explicit permanence marker,
// or any record without a generation. Such records are not migrated and never
// authorize anything on this device; they stay listed with Legacy set so the
// user can see them and re-grant or revoke them. The same grant may still be
// in effect on another device that received it through sync.

// errMissingGeneration guards the write side: every current-format record
// must carry a generation, so a record without one is never persisted as if it
// were current.
var errMissingGeneration = errors.New("AI 授权记录缺少代际标识，拒绝写入")

// DeviceGrantSet is the decoded persisted device grant map. Current records
// participate in authorization; Legacy records are preserved verbatim.
type DeviceGrantSet struct {
	Current map[string]DeviceGrant
	Legacy  map[string]LegacyDeviceGrant
}

// LegacyDeviceGrant is a rejected pre-1.3.0 device grant: View is what the
// user sees (Legacy set), and the raw record is written back unchanged.
type LegacyDeviceGrant struct {
	View DeviceGrant
	raw  json.RawMessage
}

// GrantRuleSet is the decoded persisted grant rule list.
type GrantRuleSet struct {
	Current []GrantRule
	Legacy  []LegacyGrantRule
}

// LegacyGrantRule is a rejected pre-1.3.0 grant rule kept verbatim.
type LegacyGrantRule struct {
	View GrantRule
	raw  json.RawMessage
}

// Renewed returns the grant with a fresh generation; every mutation of a
// record, local or synced, must renew it so prior admissions cannot be revived.
func (g DeviceGrant) Renewed() DeviceGrant {
	g.Legacy = false
	g.generation = ids.New()
	return g
}

// Renewed returns the rule with a fresh generation.
func (r GrantRule) Renewed() GrantRule {
	r.Legacy = false
	r.generation = ids.New()
	return r
}

// DecodeDeviceGrants decodes the persisted device grant map. It only accepts
// the current format as effective; legacy records are separated, not migrated.
func DecodeDeviceGrants(raw string) (DeviceGrantSet, error) {
	set := DeviceGrantSet{Current: map[string]DeviceGrant{}, Legacy: map[string]LegacyDeviceGrant{}}
	if raw == "" {
		return set, nil
	}
	var records map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		return DeviceGrantSet{}, err
	}
	for id, item := range records {
		var record struct {
			DeviceGrant
			Generation string `json:"generation"`
			Permanent  *bool  `json:"permanent"`
		}
		if err := json.Unmarshal(item, &record); err != nil {
			return DeviceGrantSet{}, err
		}
		grant := record.DeviceGrant
		grant.Permanent = record.Permanent != nil && *record.Permanent
		if record.Permanent == nil || record.Generation == "" {
			grant.Legacy = true
			set.Legacy[id] = LegacyDeviceGrant{View: grant, raw: append(json.RawMessage(nil), item...)}
			continue
		}
		grant.Legacy = false
		grant.generation = record.Generation
		set.Current[id] = grant
	}
	return set, nil
}

// Encode serializes current records with their generations and legacy
// records byte-for-byte.
func (s DeviceGrantSet) Encode() (string, error) {
	records := make(map[string]json.RawMessage, len(s.Current)+len(s.Legacy))
	for id, legacy := range s.Legacy {
		records[id] = legacy.raw
	}
	for id, grant := range s.Current {
		if grant.generation == "" {
			return "", fmt.Errorf("%w: device %s", errMissingGeneration, id)
		}
		encoded, err := json.Marshal(deviceGrantRecord{DeviceGrant: grant, Generation: grant.generation})
		if err != nil {
			return "", err
		}
		records[id] = encoded
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// Clone returns an independent copy safe to mutate.
func (s DeviceGrantSet) Clone() DeviceGrantSet {
	clone := DeviceGrantSet{Current: cloneGrantMap(s.Current), Legacy: make(map[string]LegacyDeviceGrant, len(s.Legacy))}
	for id, legacy := range s.Legacy {
		clone.Legacy[id] = legacy
	}
	return clone
}

// DecodeGrantRules decodes the persisted grant rule list with the same
// current-or-legacy split as DecodeDeviceGrants.
func DecodeGrantRules(raw string) (GrantRuleSet, error) {
	set := GrantRuleSet{}
	if raw == "" {
		return set, nil
	}
	var records []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		return GrantRuleSet{}, err
	}
	for _, item := range records {
		var record grantRuleRecord
		if err := json.Unmarshal(item, &record); err != nil {
			return GrantRuleSet{}, err
		}
		rule := record.GrantRule
		if record.Generation == "" {
			rule.Legacy = true
			set.Legacy = append(set.Legacy, LegacyGrantRule{View: rule, raw: append(json.RawMessage(nil), item...)})
			continue
		}
		rule.Legacy = false
		rule.generation = record.Generation
		set.Current = append(set.Current, rule)
	}
	return set, nil
}

// Encode serializes current rules in order followed by legacy rules verbatim.
func (s GrantRuleSet) Encode() (string, error) {
	records := make([]json.RawMessage, 0, len(s.Current)+len(s.Legacy))
	for _, rule := range s.Current {
		if rule.generation == "" {
			return "", fmt.Errorf("%w: rule %s", errMissingGeneration, rule.ID)
		}
		encoded, err := json.Marshal(grantRuleRecord{GrantRule: rule, Generation: rule.generation})
		if err != nil {
			return "", err
		}
		records = append(records, encoded)
	}
	for _, legacy := range s.Legacy {
		records = append(records, legacy.raw)
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// Clone returns an independent copy safe to mutate.
func (s GrantRuleSet) Clone() GrantRuleSet {
	return GrantRuleSet{Current: append([]GrantRule(nil), s.Current...), Legacy: append([]LegacyGrantRule(nil), s.Legacy...)}
}

// LegacyIDs lists the IDs of rejected legacy rules.
func (s GrantRuleSet) LegacyIDs() []string {
	result := make([]string, 0, len(s.Legacy))
	for _, legacy := range s.Legacy {
		result = append(result, legacy.View.ID)
	}
	return result
}

// LegacyIDs lists the device IDs of rejected legacy grants in stable order.
func (s DeviceGrantSet) LegacyIDs() []string {
	result := make([]string, 0, len(s.Legacy))
	for id := range s.Legacy {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}
