package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type BaseURLEntry struct {
	URL      string `json:"url"`
	Insecure bool   `json:"insecure,omitempty"`
}

type Config struct {
	Version           int            `json:"version"`
	DeviceID          string         `json:"device_id"`
	Secret            string         `json:"secret"`
	Name              string         `json:"name"`
	Platform          string         `json:"platform"`
	AppVersion        string         `json:"app_version"`
	BaseURLs          []BaseURLEntry `json:"base_urls"`
	MetricsIntervalMS int64          `json:"metrics_interval_ms"`
	DesiredAutostart  bool           `json:"desired_autostart"`
	TerminalEnabled   bool           `json:"terminal_enabled"`
	CurrentURL        string         `json:"current_url,omitempty"`
	EnrolledAt        int64          `json:"enrolled_at"`
}

func (c *Config) validate() error {
	if c.Version != configVersion {
		return fmt.Errorf("unsupported agent config version %d", c.Version)
	}
	if c.DeviceID == "" || c.Secret == "" {
		return errors.New("agent config requires device-bound credentials")
	}
	if len(c.BaseURLs) == 0 {
		return errors.New("agent config requires at least one base URL")
	}
	for _, entry := range c.BaseURLs {
		if err := validateBaseURLEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

// validateBaseURLEntry mirrors the M124 transport policy: cleartext HTTP is
// only acceptable for local/private hosts, and public HTTP requires an
// explicit insecure opt-in.
func validateBaseURLEntry(entry BaseURLEntry) error {
	parsed, err := url.Parse(entry.URL)
	if err != nil {
		return fmt.Errorf("invalid base URL %q: %w", entry.URL, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("base URL %q must be an explicit http:// or https:// URL with a host", entry.URL)
	}
	if scheme == "http" && !entry.Insecure && !isLocalOrPrivate(parsed.Hostname()) {
		return fmt.Errorf("base URL %q uses cleartext HTTP on a public host; explicit insecure opt-in is required", entry.URL)
	}
	return nil
}

func isLocalOrPrivate(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || strings.HasSuffix(hostname, ".local") {
		return true
	}
	address, err := netip.ParseAddr(hostname)
	if err != nil {
		return false
	}
	address = address.Unmap()
	return address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast()
}

type Store struct {
	path string
}

func StoreAt(dataDir string) *Store {
	return &Store{path: filepath.Join(dataDir, "fleet", "agent.json")}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Load() (*Config, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotEnrolled
	}
	if err != nil {
		return nil, fmt.Errorf("read agent config: %w", err)
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse agent config: %w", err)
	}
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("agent config invalid: %w", err)
	}
	return &config, nil
}

func (s *Store) Save(config *Config) error {
	if config == nil {
		return errors.New("agent config is required")
	}
	if err := config.validate(); err != nil {
		return fmt.Errorf("agent config invalid: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create agent config directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("restrict agent config directory: %w", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent config: %w", err)
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("write agent config: %w", err)
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return fmt.Errorf("restrict agent config: %w", err)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		return fmt.Errorf("replace agent config: %w", err)
	}
	return nil
}

func (s *Store) Remove() error {
	err := os.Remove(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
