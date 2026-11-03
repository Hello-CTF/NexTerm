package asciicast

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var sizePattern = regexp.MustCompile(`^\d+x\d+$`)

type parsedCast struct {
	header map[string]any
	times  []float64
	codes  []string
	data   []string
}

// parseCast validates the asciicast v2 line grammar and returns the decoded events.
func parseCast(t *testing.T, raw []byte) parsedCast {
	t.Helper()
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("cast must end with a newline: %q", raw)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	var result parsedCast
	if err := json.Unmarshal([]byte(lines[0]), &result.header); err != nil {
		t.Fatalf("header is not a JSON object: %v", err)
	}
	if version, _ := result.header["version"].(float64); version != 2 {
		t.Fatalf("version = %v", result.header["version"])
	}
	for _, key := range []string{"width", "height"} {
		value, ok := result.header[key].(float64)
		if !ok || value < 1 || value != float64(int(value)) {
			t.Fatalf("%s = %v", key, result.header[key])
		}
	}
	if _, ok := result.header["timestamp"].(float64); !ok {
		t.Fatalf("timestamp = %v", result.header["timestamp"])
	}
	last := 0.0
	for _, line := range lines[1:] {
		var event []any
		if err := json.Unmarshal([]byte(line), &event); err != nil || len(event) != 3 {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		at, ok := event[0].(float64)
		code, ok2 := event[1].(string)
		data, ok3 := event[2].(string)
		if !ok || !ok2 || !ok3 {
			t.Fatalf("bad event types %q", line)
		}
		if at < last {
			t.Fatalf("time went backwards: %v after %v", at, last)
		}
		last = at
		if code != "o" && code != "r" {
			t.Fatalf("unexpected event code %q", code)
		}
		if code == "r" && !sizePattern.MatchString(data) {
			t.Fatalf("bad resize data %q", data)
		}
		if !utf8.ValidString(data) {
			t.Fatalf("invalid UTF-8 in %q", line)
		}
		result.times = append(result.times, at)
		result.codes = append(result.codes, code)
		result.data = append(result.data, data)
	}
	return result
}

func (p parsedCast) output() string {
	var out strings.Builder
	for i, code := range p.codes {
		if code == "o" {
			out.WriteString(p.data[i])
		}
	}
	return out.String()
}

func TestWriterFormatAndHeader(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, Header{Width: 100, Height: 30, StartMS: 1_700_000_000_500, Title: "web-01"})
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Output(1_700_000_001_000, []byte("hello \x1b[31mred\x1b[0m\r\n")))
	must(t, w.Resize(1_700_000_002_250, 120, 40))
	must(t, w.Output(1_700_000_003_000, []byte(`quote " <tag> & \`)))
	result, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	cast := parseCast(t, buf.Bytes())
	if cast.header["width"].(float64) != 100 || cast.header["height"].(float64) != 30 ||
		cast.header["timestamp"].(float64) != 1_700_000_000 || cast.header["title"] != "web-01" {
		t.Fatalf("header = %v", cast.header)
	}
	if cast.times[0] != 0.5 || cast.times[1] != 1.75 || cast.data[1] != "120x40" {
		t.Fatalf("events = %v %v", cast.times, cast.data)
	}
	if cast.output() != "hello \x1b[31mred\x1b[0m\r\n"+`quote " <tag> & \` {
		t.Fatalf("output = %q", cast.output())
	}
	if result.Events != 3 || result.Bytes != int64(buf.Len()) || result.TimingEstimated {
		t.Fatalf("result = %+v", result)
	}
}

func TestWriterDefaultsSizeAndOmitsTitle(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, Header{StartMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	cast := parseCast(t, buf.Bytes())
	if cast.header["width"].(float64) != 80 || cast.header["height"].(float64) != 24 {
		t.Fatalf("header = %v", cast.header)
	}
	if _, ok := cast.header["title"]; ok {
		t.Fatal("empty title must be omitted")
	}
}

func TestWriterJoinsMultibyteAcrossChunks(t *testing.T) {
	original := "中文输出 and emoji \U0001F600 end\r\n"
	raw := []byte(original)
	for split := 1; split < len(raw); split++ {
		var buf bytes.Buffer
		w, err := NewWriter(&buf, Header{StartMS: 1000})
		if err != nil {
			t.Fatal(err)
		}
		must(t, w.Output(1000, raw[:split]))
		must(t, w.Output(1001, raw[split:]))
		if _, err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if got := parseCast(t, buf.Bytes()).output(); got != original {
			t.Fatalf("split %d: output = %q", split, got)
		}
	}
}

func TestWriterReplacesInvalidBytes(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, Header{StartMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Output(1000, []byte("a\xffb")))
	must(t, w.Output(1001, []byte("c\xe4\xb8"))) // truncated 3-byte sequence at the end
	must(t, w.Output(1002, []byte("d")))         // cannot complete it
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := parseCast(t, buf.Bytes()).output(); got != "a\uFFFDbc\uFFFD\uFFFDd" {
		t.Fatalf("output = %q", got)
	}
}

func TestWriterFlushesIncompleteTailOnClose(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, Header{StartMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Output(1000, []byte("ok\xe4\xb8")))
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := parseCast(t, buf.Bytes()).output(); got != "ok\uFFFD\uFFFD" {
		t.Fatalf("output = %q", got)
	}
}

func TestWriterEstimatesMissingTimestamps(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, Header{StartMS: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Output(11_000, []byte("a")))
	must(t, w.Output(0, []byte("b")))
	must(t, w.Output(-5, []byte("c")))
	result, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	cast := parseCast(t, buf.Bytes())
	if !result.TimingEstimated || !(cast.times[1] > cast.times[0]) || !(cast.times[2] > cast.times[1]) {
		t.Fatalf("result = %+v times = %v", result, cast.times)
	}
}

func TestWriterClampsClockRegression(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, Header{StartMS: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	must(t, w.Output(12_000, []byte("a")))
	must(t, w.Output(11_000, []byte("b"))) // wall clock went back
	must(t, w.Output(9_000, []byte("c")))  // before the base
	result, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	cast := parseCast(t, buf.Bytes())
	if cast.times[1] != 2 || cast.times[2] != 2 || result.TimingEstimated {
		t.Fatalf("times = %v result = %+v", cast.times, result)
	}
}

func TestWriterRejectsUseAfterClose(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, Header{StartMS: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Output(2, []byte("x")); err == nil {
		t.Fatal("output after close must fail")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
