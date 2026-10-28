package guard

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// Persisted records without the current marker (explicit permanence and a
// generation for grants, a generation for rules) never authorize anything and
// are dropped when decoded; the caller rewrites the row.

// errMissingGeneration guards the write side: every current-format record
// must carry a generation, so a record without one is never persisted as if it
// were current.
var errMissingGeneration = errors.New("AI 授权记录缺少代际标识，拒绝写入")

// DeviceGrantSet is the decoded persisted device grant map.
type DeviceGrantSet struct {
	Current map[string]DeviceGrant
	// Dropped reports that the decoded row held records that were discarded.
	Dropped bool
}

// GrantRuleSet is the decoded persisted grant rule list.
type GrantRuleSet struct {
	Current []GrantRule
	// Dropped reports that the decoded row held records that were discarded.
	Dropped bool
}

// Renewed returns the grant with a fresh generation; every mutation of a
// record, local or synced, must renew it so prior admissions cannot be revived.
func (g DeviceGrant) Renewed() DeviceGrant {
	g.generation = ids.New()
	return g
}

// Renewed returns the rule with a fresh generation.
func (r GrantRule) Renewed() GrantRule {
	r.generation = ids.New()
	return r
}

// DecodeDeviceGrants decodes the persisted device grant map. Records that are
// not in the current format or are malformed are dropped without an error; only
// a row that is not a JSON object at all is rejected.
func DecodeDeviceGrants(raw string) (DeviceGrantSet, error) {
	set := DeviceGrantSet{Current: map[string]DeviceGrant{}}
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
		if err := json.Unmarshal(item, &record); err != nil || record.Permanent == nil || record.Generation == "" || record.DeviceID == "" {
			set.Dropped = true
			continue
		}
		grant := record.DeviceGrant
		grant.Permanent = *record.Permanent
		grant.generation = record.Generation
		set.Current[id] = grant
	}
	return set, nil
}

// Encode serializes the records with their generations.
func (s DeviceGrantSet) Encode() (string, error) {
	records := make(map[string]json.RawMessage, len(s.Current))
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
	return DeviceGrantSet{Current: cloneGrantMap(s.Current)}
}

// DecodeGrantRules decodes the persisted grant rule list with the same
// drop-quietly behavior as DecodeDeviceGrants.
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
		if err := json.Unmarshal(item, &record); err != nil || record.Generation == "" || record.ID == "" {
			set.Dropped = true
			continue
		}
		rule := record.GrantRule
		rule.generation = record.Generation
		set.Current = append(set.Current, rule)
	}
	return set, nil
}

// Encode serializes the rules in order.
func (s GrantRuleSet) Encode() (string, error) {
	records := make([]json.RawMessage, 0, len(s.Current))
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
	encoded, err := json.Marshal(records)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// Clone returns an independent copy safe to mutate.
func (s GrantRuleSet) Clone() GrantRuleSet {
	return GrantRuleSet{Current: append([]GrantRule(nil), s.Current...)}
}
