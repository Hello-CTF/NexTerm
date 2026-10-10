package server

import (
	"context"
	"fmt"
	"os"
	"strings"

	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

const DefaultListen = "0.0.0.0:8080"

const (
	AuthOn       = core.AuthOn
	AuthLoopback = core.AuthLoopback
	AuthPlatform = core.AuthPlatform
	AuthOff      = core.AuthOff
)

type Options struct {
	Listen         string
	DataDir        string
	WebRoot        string
	MasterKey      string
	SyncOnly       bool
	Auth           string
	AllowedOrigins []string
}

func ResolveMasterKey(masterKeyFile string) (string, error) {
	if masterKeyFile == "" {
		return "", nil
	}
	data, err := os.ReadFile(masterKeyFile)
	if err != nil {
		return "", fmt.Errorf("read master key file: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("master key file %s is empty", masterKeyFile)
	}
	return key, nil
}

func ResolveDBPassword(passwordFile string) (string, error) {
	if passwordFile == "" {
		return "", nil
	}
	info, err := os.Stat(passwordFile)
	if err != nil {
		return "", fmt.Errorf("stat database password file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("database password file %s is not a regular file", passwordFile)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("database password file %s must have 0600 permissions (got %04o)", passwordFile, info.Mode().Perm())
	}
	data, err := os.ReadFile(passwordFile)
	if err != nil {
		return "", fmt.Errorf("read database password file: %w", err)
	}
	password := strings.TrimSpace(string(data))
	if password == "" {
		return "", fmt.Errorf("database password file %s is empty", passwordFile)
	}
	return password, nil
}

type Vault interface {
	Status() vault.Status
	InitMaster(context.Context, string) error
	UnlockMaster(context.Context, string) error
}

func BootstrapVault(ctx context.Context, credentialVault Vault, masterKey string) error {
	if masterKey == "" {
		return nil
	}
	if credentialVault == nil {
		return fmt.Errorf("vault is not configured")
	}
	if len(masterKey) < 8 {
		return fmt.Errorf("master key must contain at least 8 bytes")
	}
	if credentialVault.Status().Initialized {
		return credentialVault.UnlockMaster(ctx, masterKey)
	}
	return credentialVault.InitMaster(ctx, masterKey)
}

func BootstrapVaultRequired(ctx context.Context, credentialVault Vault, masterKey string) error {
	if masterKey == "" {
		return fmt.Errorf("vault is required but no master key is configured")
	}
	if err := BootstrapVault(ctx, credentialVault, masterKey); err != nil {
		return fmt.Errorf("vault bootstrap failed: %w", err)
	}
	if !credentialVault.Status().Unlocked {
		return fmt.Errorf("vault is required but remained locked after bootstrap")
	}
	return nil
}
