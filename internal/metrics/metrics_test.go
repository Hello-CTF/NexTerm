package metrics

import (
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWriterExpositionGolden(t *testing.T) {
	w := NewWriter()
	w.Counter("demo_requests_total", "Requests with a \\ backslash\nand a newline.", 3, L("code", "2xx"))
	w.Gauge("demo_temperature", "Plain gauge.", 21.5)
	// A second sample of the first family is grouped under its header.
	w.Counter("demo_requests_total", "ignored on repeat", 1, L("code", `quote" back\slash`+"\nnewline"))
	w.Gauge("demo_special", "Special values.", math.Inf(1), L("kind", "inf"))
	h := NewHistogram([]float64{1, 0.1})
	h.Observe(0.05)
	h.Observe(0.5)
	h.Observe(5)
	w.Histogram("demo_duration_seconds", "Durations.", h.Snapshot(), L("route", "/x/{id}"))

	want := `# HELP demo_requests_total Requests with a \\ backslash\nand a newline.
# TYPE demo_requests_total counter
demo_requests_total{code="2xx"} 3
demo_requests_total{code="quote\" back\\slash\nnewline"} 1
# HELP demo_temperature Plain gauge.
# TYPE demo_temperature gauge
demo_temperature 21.5
# HELP demo_special Special values.
# TYPE demo_special gauge
demo_special{kind="inf"} +Inf
# HELP demo_duration_seconds Durations.
# TYPE demo_duration_seconds histogram
demo_duration_seconds_bucket{route="/x/{id}",le="0.1"} 1
demo_duration_seconds_bucket{route="/x/{id}",le="1"} 2
demo_duration_seconds_bucket{route="/x/{id}",le="+Inf"} 3
demo_duration_seconds_sum{route="/x/{id}"} 5.55
demo_duration_seconds_count{route="/x/{id}"} 3
`
	if got := string(w.Bytes()); got != want {
		t.Fatalf("exposition mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestWriterRejectsInvalidNames(t *testing.T) {
	for name, add := range map[string]func(*Writer){
		"metric": func(w *Writer) { w.Gauge("1bad", "", 1) },
		"label":  func(w *Writer) { w.Gauge("ok", "", 1, L("bad-label", "x")) },
		"type":   func(w *Writer) { w.Gauge("ok", "", 1); w.Counter("ok", "", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid metric was accepted")
				}
			}()
			add(NewWriter())
		})
	}
}

func TestNormalizeAddrRequiresLoopback(t *testing.T) {
	allowed := map[string]string{
		"127.0.0.1:9464": "127.0.0.1:9464",
		":9464":          "127.0.0.1:9464",
		"localhost:9464": "localhost:9464",
		"[::1]:9464":     "[::1]:9464",
	}
	for input, want := range allowed {
		if got, err := NormalizeAddr(input, false); err != nil || got != want {
			t.Fatalf("NormalizeAddr(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if got, err := NormalizeAddr("", false); err != nil || got != "" {
		t.Fatalf("empty = %q, %v", got, err)
	}
	for _, input := range []string{"0.0.0.0:9464", "[::]:9464", "192.168.1.10:9464", "example.com:9464"} {
		if got, err := NormalizeAddr(input, false); err == nil {
			t.Fatalf("NormalizeAddr(%q) = %q without allowRemote, want error", input, got)
		}
		if got, err := NormalizeAddr(input, true); err != nil || got != input {
			t.Fatalf("NormalizeAddr(%q, allowRemote) = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"garbage", "9464", "127.0.0.1:99999"} {
		if _, err := NormalizeAddr(input, true); err == nil {
			t.Fatalf("NormalizeAddr(%q) accepted a malformed address", input)
		}
	}
}

func TestParseAllowRemote(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "0": false, "1": true, "true": true} {
		if got, err := ParseAllowRemote(raw); err != nil || got != want {
			t.Fatalf("ParseAllowRemote(%q) = %v, %v", raw, got, err)
		}
	}
	if _, err := ParseAllowRemote("yes please"); err == nil {
		t.Fatal("invalid value accepted")
	}
}

func TestStartDisabledByDefault(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := Start(logger, "", false, func(*Writer) { t.Error("collector ran while disabled") })
	if err != nil || server != nil {
		t.Fatalf("Start(\"\") = %v, %v", server, err)
	}
	if server.Addr() != "" || server.Close(context.Background()) != nil {
		t.Fatal("nil server must report no address and close cleanly")
	}
	if _, err := Start(logger, "0.0.0.0:0", false, nil); err == nil {
		t.Fatal("wildcard address accepted without allowRemote")
	}
}

func TestStartServesMetricsOnly(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := Start(logger, "127.0.0.1:0", false, func(w *Writer) { w.Gauge("demo_up", "Up.", 1) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get("http://" + server.Addr() + Path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != ContentType || !strings.Contains(string(body), "demo_up 1\n") {
		t.Fatalf("GET /metrics = %d %q %q", response.StatusCode, response.Header.Get("Content-Type"), body)
	}
	response, err = client.Get("http://" + server.Addr() + "/debug/pprof/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unrelated path status = %d, want 404", response.StatusCode)
	}
}
