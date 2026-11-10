// Package metrics renders the Prometheus text exposition format (version
// 0.0.4) by hand and serves it on a dedicated listener, so nexterm-server
// gains a scrape endpoint without the client_golang dependency.
package metrics

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ContentType is the media type of the text exposition format.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

const (
	typeCounter   = "counter"
	typeGauge     = "gauge"
	typeHistogram = "histogram"
)

// Label is one name/value pair. Values must come from a bounded set and never
// carry user identifiers, hostnames or paths.
type Label struct {
	Name  string
	Value string
}

// L builds a label.
func L(name, value string) Label { return Label{Name: name, Value: value} }

type family struct {
	help    string
	kind    string
	samples bytes.Buffer
}

// Writer collects metric families. Samples of one family may be added in any
// order relative to other families: the output groups them under a single
// HELP/TYPE header, as the format requires, in order of first appearance.
type Writer struct {
	order    []string
	families map[string]*family
}

func NewWriter() *Writer {
	return &Writer{families: make(map[string]*family)}
}

// Counter adds one sample of a counter family.
func (w *Writer) Counter(name, help string, value float64, labels ...Label) {
	w.sample(w.family(name, help, typeCounter), name, "", value, labels)
}

// Gauge adds one sample of a gauge family.
func (w *Writer) Gauge(name, help string, value float64, labels ...Label) {
	w.sample(w.family(name, help, typeGauge), name, "", value, labels)
}

// Histogram adds one labeled series of a histogram family.
func (w *Writer) Histogram(name, help string, snapshot HistogramSnapshot, labels ...Label) {
	f := w.family(name, help, typeHistogram)
	for index, bound := range snapshot.Bounds {
		w.sample(f, name, "_bucket", float64(snapshot.Cumulative[index]), append(append([]Label(nil), labels...), L("le", formatFloat(bound))))
	}
	w.sample(f, name, "_bucket", float64(snapshot.Count), append(append([]Label(nil), labels...), L("le", "+Inf")))
	w.sample(f, name, "_sum", snapshot.Sum, labels)
	w.sample(f, name, "_count", float64(snapshot.Count), labels)
}

// Bytes renders every collected family.
func (w *Writer) Bytes() []byte {
	var out bytes.Buffer
	for _, name := range w.order {
		f := w.families[name]
		out.WriteString("# HELP ")
		out.WriteString(name)
		out.WriteByte(' ')
		out.WriteString(escapeHelp(f.help))
		out.WriteString("\n# TYPE ")
		out.WriteString(name)
		out.WriteByte(' ')
		out.WriteString(f.kind)
		out.WriteByte('\n')
		out.Write(f.samples.Bytes())
	}
	return out.Bytes()
}

func (w *Writer) family(name, help, kind string) *family {
	if !validMetricName(name) {
		panic(fmt.Sprintf("metrics: invalid metric name %q", name))
	}
	if f, ok := w.families[name]; ok {
		if f.kind != kind {
			panic(fmt.Sprintf("metrics: %s registered as %s and %s", name, f.kind, kind))
		}
		return f
	}
	f := &family{help: help, kind: kind}
	w.families[name] = f
	w.order = append(w.order, name)
	return f
}

func (w *Writer) sample(f *family, name, suffix string, value float64, labels []Label) {
	buf := &f.samples
	buf.WriteString(name)
	buf.WriteString(suffix)
	if len(labels) > 0 {
		buf.WriteByte('{')
		for index, label := range labels {
			if !validLabelName(label.Name) {
				panic(fmt.Sprintf("metrics: invalid label name %q on %s", label.Name, name))
			}
			if index > 0 {
				buf.WriteByte(',')
			}
			buf.WriteString(label.Name)
			buf.WriteString(`="`)
			buf.WriteString(escapeLabelValue(label.Value))
			buf.WriteByte('"')
		}
		buf.WriteByte('}')
	}
	buf.WriteByte(' ')
	buf.WriteString(formatFloat(value))
	buf.WriteByte('\n')
}

func formatFloat(value float64) string {
	switch {
	case math.IsInf(value, 1):
		return "+Inf"
	case math.IsInf(value, -1):
		return "-Inf"
	case math.IsNaN(value):
		return "NaN"
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

var labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)

func escapeHelp(value string) string { return helpEscaper.Replace(value) }

func escapeLabelValue(value string) string { return labelEscaper.Replace(value) }

func validMetricName(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		letter := char == '_' || char == ':' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		if !letter && (index == 0 || char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func validLabelName(name string) bool {
	if name == "" || strings.HasPrefix(name, "__") {
		return false
	}
	for index, char := range name {
		letter := char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		if !letter && (index == 0 || char < '0' || char > '9') {
			return false
		}
	}
	return true
}
