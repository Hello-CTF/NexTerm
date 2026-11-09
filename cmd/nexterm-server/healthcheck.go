package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/applog"
)

const healthcheckUsage = `Usage:
  nexterm-server healthcheck [--url URL] [--timeout DURATION]

Probe a running nexterm-server readiness endpoint without curl (Docker, LazyCat, systemd).
Exits 0 when /healthz answers 200 with {"ok":true,"service":"nexterm-server"}, 1 when the
server is unreachable or unhealthy (database ping failed, required vault locked), 2 on usage errors.

Flags:
  --url URL           Health URL (default http://<NEXTERM_LISTEN>/healthz, with a wildcard
                      host replaced by loopback; NEXTERM_LISTEN defaults to 0.0.0.0:8080)
  --timeout DURATION  Request timeout (default 5s)
  -h, --help          Print this help
`

func runHealthcheck(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rawURL := flags.String("url", "", "")
	timeout := flags.Duration("timeout", 5*time.Second, "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, healthcheckUsage)
			return 0
		}
		fmt.Fprintln(stderr, "nexterm-server healthcheck:", err)
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "nexterm-server healthcheck: unexpected argument", flags.Arg(0))
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "nexterm-server healthcheck: --timeout must be positive")
		return 2
	}
	target := strings.TrimSpace(*rawURL)
	if target == "" {
		listen := strings.TrimSpace(getenv("NEXTERM_LISTEN"))
		if listen == "" {
			listen = "0.0.0.0:8080"
		}
		derived, err := healthURLForListen(listen)
		if err != nil {
			fmt.Fprintln(stderr, "nexterm-server healthcheck:", err)
			return 2
		}
		target = derived
	} else if parsed, err := url.Parse(target); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		fmt.Fprintln(stderr, "nexterm-server healthcheck: --url must be an absolute http(s) URL")
		return 2
	}
	if err := probeHealth(target, *timeout); err != nil {
		fmt.Fprintln(stderr, "nexterm-server healthcheck:", applog.RedactText(err.Error()))
		return 1
	}
	fmt.Fprintln(stdout, "ok")
	return 0
}

func healthURLForListen(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("invalid NEXTERM_LISTEN %q: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}

func probeHealth(target string, timeout time.Duration) error {
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	var view struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&view)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP %d", target, response.StatusCode)
	}
	if decodeErr != nil || !view.OK || view.Service != "nexterm-server" {
		return fmt.Errorf("%s did not return a healthy nexterm-server payload", target)
	}
	return nil
}
