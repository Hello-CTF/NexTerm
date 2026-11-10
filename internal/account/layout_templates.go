package account

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of the workspace.layoutTemplates preference. They mirror src/app/layoutTemplates.ts; the depth limit and
// ratio range are the split tree's own (src/app/splitTree.ts), so every stored template can be rebuilt as a workspace
// layout. The whole list stays below the 64 KiB account request body.
const (
	maxLayoutTemplates           = 30
	maxLayoutTemplatePanes       = 16
	maxLayoutTemplateDepth       = 8
	maxLayoutTemplateName        = 64
	maxLayoutTemplateDescription = 200
	maxLayoutTemplatesBytes      = 48 << 10
	layoutTemplateRatioMin       = 0.15
	layoutTemplateRatioMax       = 0.85
)

var (
	layoutTemplateIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	layoutTemplateRefPattern  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	layoutTemplateIconPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,47}$`)
)

type layoutTemplate struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Icon        string              `json:"icon,omitempty"`
	AssetID     string              `json:"assetId"`
	Layout      *layoutTemplateNode `json:"layout"`
}

// layoutTemplateNode is one node of a template's split tree: a leaf is a pane that opens a terminal on the
// template's host and optionally runs a snippet; a split places a above (col) or left of (row) b.
type layoutTemplateNode struct {
	Type      string              `json:"type"`
	SnippetID string              `json:"snippetId,omitempty"`
	Dir       string              `json:"dir,omitempty"`
	Ratio     *float64            `json:"ratio,omitempty"`
	A         *layoutTemplateNode `json:"a,omitempty"`
	B         *layoutTemplateNode `json:"b,omitempty"`
}

// normalizeLayoutTemplates validates named workspace layout templates. A template references its host and pane
// snippets by id only; whether they still exist is checked when the template is opened, so a template survives the
// deletion of what it points at and the client reports the gap.
func normalizeLayoutTemplates(raw json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var templates []layoutTemplate
	if err := decoder.Decode(&templates); err != nil || decoder.More() || templates == nil || len(templates) > maxLayoutTemplates {
		return "", false
	}
	seen := make(map[string]bool, len(templates))
	for _, template := range templates {
		if !layoutTemplateIDPattern.MatchString(template.ID) || seen[template.ID] {
			return "", false
		}
		seen[template.ID] = true
		if !validLayoutTemplateText(template.Name, maxLayoutTemplateName, true) ||
			!validLayoutTemplateText(template.Description, maxLayoutTemplateDescription, false) ||
			(template.Icon != "" && !layoutTemplateIconPattern.MatchString(template.Icon)) ||
			!layoutTemplateRefPattern.MatchString(template.AssetID) {
			return "", false
		}
		panes := 0
		if !validLayoutTemplateNode(template.Layout, 0, &panes) {
			return "", false
		}
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(templates); err != nil {
		return "", false
	}
	canonical := strings.TrimSuffix(out.String(), "\n")
	if len(canonical) > maxLayoutTemplatesBytes {
		return "", false
	}
	return canonical, true
}

func validLayoutTemplateNode(node *layoutTemplateNode, depth int, panes *int) bool {
	if node == nil {
		return false
	}
	switch node.Type {
	case "leaf":
		*panes++
		if *panes > maxLayoutTemplatePanes || node.Dir != "" || node.Ratio != nil || node.A != nil || node.B != nil {
			return false
		}
		return node.SnippetID == "" || layoutTemplateRefPattern.MatchString(node.SnippetID)
	case "split":
		if depth >= maxLayoutTemplateDepth || node.SnippetID != "" || (node.Dir != "row" && node.Dir != "col") {
			return false
		}
		if node.Ratio == nil || *node.Ratio < layoutTemplateRatioMin || *node.Ratio > layoutTemplateRatioMax {
			return false
		}
		return validLayoutTemplateNode(node.A, depth+1, panes) && validLayoutTemplateNode(node.B, depth+1, panes)
	default:
		return false
	}
}

func validLayoutTemplateText(text string, maxRunes int, required bool) bool {
	if required && strings.TrimSpace(text) == "" {
		return false
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > maxRunes {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
