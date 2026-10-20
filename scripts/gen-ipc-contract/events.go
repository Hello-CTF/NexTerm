package main

import (
	"fmt"
	"go/constant"
	"go/types"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

var eventTopicPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*://[a-z0-9-]+$`)

type eventTopic struct {
	Key   string
	Topic string
}

func extractEventTopics(loaded []*packages.Package) ([]eventTopic, error) {
	byKey := make(map[string]string)
	for _, pkg := range loaded {
		for _, name := range pkg.Types.Scope().Names() {
			key := eventTopicKey(name)
			if key == "" {
				continue
			}
			object, ok := pkg.Types.Scope().Lookup(name).(*types.Const)
			if !ok || object.Val().Kind() != constant.String {
				continue
			}
			topic := constant.StringVal(object.Val())
			if !eventTopicPattern.MatchString(topic) {
				continue
			}
			if existing, exists := byKey[key]; exists && existing != topic {
				return nil, fmt.Errorf("event topic key %q maps to both %q and %q", key, existing, topic)
			}
			byKey[key] = topic
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	topics := make([]eventTopic, 0, len(keys))
	for _, key := range keys {
		topics = append(topics, eventTopic{Key: key, Topic: byKey[key]})
	}
	return topics, nil
}

func eventTopicKey(name string) string {
	for _, affix := range []struct {
		prefix bool
		value  string
	}{{prefix: true, value: "Topic"}, {value: "Topic"}, {value: "Event"}} {
		var key string
		if affix.prefix {
			key = strings.TrimPrefix(name, affix.value)
			if key == name {
				continue
			}
		} else {
			key = strings.TrimSuffix(name, affix.value)
			if key == name {
				continue
			}
		}
		if key != "" {
			return lowerCamel(key)
		}
	}
	return ""
}

func lowerCamel(value string) string {
	if value == "" {
		return ""
	}
	end := 1
	for end < len(value) && value[end] >= 'A' && value[end] <= 'Z' {
		end++
	}
	if end > 1 && end < len(value) && value[end] >= 'a' && value[end] <= 'z' {
		end--
	}
	return strings.ToLower(value[:end]) + value[end:]
}
