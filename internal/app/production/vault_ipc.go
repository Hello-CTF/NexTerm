package production

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"gorm.io/gorm"
)

type vaultPasswordRequest struct {
	Password    string `json:"password"`
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

type vaultAutoLockRequest struct {
	Minutes *uint64 `json:"minutes"`
}

// vaultStatusDTO adds Managed: the server keeps its vault unlocked from a master key file, so clients hide
// the unlock, lock and password entries.
type vaultStatusDTO struct {
	vault.Status
	Managed bool `json:"managed"`
}

type credentialSetRequest struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	Secret     string  `json:"secret"`
	Source     *string `json:"source"`
	Passphrase *string `json:"passphrase"`
}

type credentialUpdateRequest struct {
	ID         string  `json:"id"`
	Name       *string `json:"name"`
	Secret     *string `json:"secret"`
	Source     *string `json:"source"`
	Passphrase *string `json:"passphrase"`
}

type credentialDTO struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Kind          string           `json:"kind"`
	CreatedAt     int64            `json:"createdAt"`
	UpdatedAt     int64            `json:"updatedAt"`
	UsedBy        []store.AssetRef `json:"usedBy"`
	Source        *string          `json:"source"`
	RefPath       *string          `json:"refPath"`
	HasPassphrase bool             `json:"hasPassphrase"`
}

type revealedCredentialDTO struct {
	Kind       string  `json:"kind"`
	Value      string  `json:"value"`
	Source     *string `json:"source"`
	RefPath    *string `json:"refPath"`
	Passphrase *string `json:"passphrase"`
}

func registerVaultCommands(dispatcher *ipc.Dispatcher, credentialVault *vault.Vault, database *store.Store, events ipc.Emitter, desktop bool) error {
	// The server vault is held open by its master key file, so locking or re-keying it from an account would
	// desynchronize the shared vault from that file; only the desktop owns these commands.
	desktopOnly := func(register func() error) func() error {
		return func() error {
			if !desktop {
				return nil
			}
			return register()
		}
	}
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "vault_status", func(_ context.Context, _ *ipc.Call, _ struct{}) (vaultStatusDTO, error) {
				return vaultStatusDTO{Status: credentialVault.Status(), Managed: !desktop}, nil
			})
		},
		desktopOnly(func() error {
			return ipc.Register(dispatcher, "vault_init_master", func(ctx context.Context, _ *ipc.Call, input vaultPasswordRequest) (any, error) {
				return nil, credentialVault.InitMaster(ctx, input.Password)
			})
		}),
		desktopOnly(func() error {
			return ipc.Register(dispatcher, "vault_init_dpapi", func(ctx context.Context, _ *ipc.Call, input vaultPasswordRequest) (any, error) {
				return nil, credentialVault.InitDPAPI(ctx, input.Password)
			})
		}),
		desktopOnly(func() error {
			return ipc.Register(dispatcher, "vault_unlock", func(ctx context.Context, _ *ipc.Call, input vaultPasswordRequest) (any, error) {
				return nil, credentialVault.UnlockMaster(ctx, input.Password)
			})
		}),
		desktopOnly(func() error {
			return ipc.Register(dispatcher, "vault_lock", func(_ context.Context, _ *ipc.Call, _ struct{}) (any, error) {
				credentialVault.Lock()
				return nil, nil
			})
		}),
		desktopOnly(func() error {
			return ipc.Register(dispatcher, "vault_set_autolock", func(ctx context.Context, _ *ipc.Call, input vaultAutoLockRequest) (any, error) {
				if input.Minutes == nil {
					return nil, ipc.BadParam(fmt.Errorf("自动锁定时长需在 0（禁用）至 1440 分钟之间"))
				}
				return nil, credentialVault.SetAutoLock(ctx, *input.Minutes)
			})
		}),
		desktopOnly(func() error {
			return ipc.Register(dispatcher, "vault_change_password", func(ctx context.Context, _ *ipc.Call, input vaultPasswordRequest) (any, error) {
				return nil, credentialVault.ChangeMasterPassword(ctx, input.OldPassword, input.NewPassword)
			})
		}),
		func() error {
			return ipc.RegisterNested(dispatcher, "vault_set_credential", func(ctx context.Context, _ *ipc.Call, input credentialSetRequest) (map[string]string, error) {
				plaintext := input.Secret
				if input.Kind == vault.KindPrivateKey {
					source := "inline"
					if input.Source != nil {
						source = *input.Source
					}
					switch source {
					case "file":
						if ipc.OwnerID(ctx) != "" {
							return nil, store.RejectServerSSHFile()
						}
						plaintext = vault.ReferencedPrivateKey(input.Secret, input.Passphrase).Encode()
					case "inline":
						plaintext = vault.InlinePrivateKey(input.Secret, input.Passphrase).Encode()
					default:
						return nil, ipc.BadParam(fmt.Errorf("未知的凭据来源 %q", source))
					}
				}
				id, err := putProductionCredential(ctx, credentialVault, database, input.ID, input.Name, input.Kind, plaintext)
				return map[string]string{"id": id}, err
			})
		},
		func() error {
			return ipc.Register(dispatcher, "vault_list_credentials", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]credentialDTO, error) {
				rows, err := database.CredentialList(ctx)
				if err != nil {
					return nil, err
				}
				result := make([]credentialDTO, 0, len(rows))
				unlocked := credentialVault.Status().Unlocked
				decryptFailures := 0
				for _, row := range rows {
					usedBy, err := database.CredentialUsage(ctx, row.ID)
					if err != nil {
						return nil, err
					}
					item := credentialDTO{
						ID: row.ID, Name: row.Name, Kind: row.Kind, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, UsedBy: usedBy,
					}
					if unlocked && row.Kind == vault.KindPrivateKey {
						plaintext, err := credentialVault.DecryptCredentialString(ctx, row)
						if err != nil {
							decryptFailures++
							slog.Warn("credential decrypt failed", "credential", row.ID, "error", err)
						} else {
							payload := vault.ParsePrivateKeyPayload(plaintext)
							item.Source = productionKeySource(payload)
							item.RefPath = payload.File
							item.HasPassphrase = payload.Passphrase != nil
						}
					}
					result = append(result, item)
				}
				if decryptFailures > 0 {
					reportAppError(ctx, events, "vault", fmt.Sprintf("%d 条凭据解密失败，来源与口令信息无法显示，请检查凭据库状态或重新保存这些凭据", decryptFailures), nil)
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "vault_delete_credential", func(ctx context.Context, _ *ipc.Call, input idRequest) (any, error) {
				return nil, database.CredentialDelete(ctx, input.ID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "vault_reveal_credential", func(ctx context.Context, _ *ipc.Call, input idRequest) (revealedCredentialDTO, error) {
				row, err := database.CredentialGetRow(ctx, input.ID)
				if err != nil {
					return revealedCredentialDTO{}, err
				}
				plaintext, err := credentialVault.DecryptCredentialString(ctx, row)
				if err != nil {
					return revealedCredentialDTO{}, err
				}
				if row.Kind != vault.KindPrivateKey {
					return revealedCredentialDTO{Kind: row.Kind, Value: plaintext}, nil
				}
				payload := vault.ParsePrivateKeyPayload(plaintext)
				value := ""
				if payload.Key != nil {
					value = *payload.Key
				}
				return revealedCredentialDTO{
					Kind: row.Kind, Value: value, Source: productionKeySource(payload), RefPath: payload.File, Passphrase: payload.Passphrase,
				}, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "credential_update", func(ctx context.Context, _ *ipc.Call, input credentialUpdateRequest) (any, error) {
				row, err := database.CredentialGetRow(ctx, input.ID)
				if err != nil {
					return nil, err
				}
				plaintext, err := credentialVault.DecryptCredentialString(ctx, row)
				if err != nil {
					return nil, err
				}
				name := row.Name
				if input.Name != nil {
					name = *input.Name
				}
				if row.Kind == vault.KindPrivateKey {
					payload := vault.ParsePrivateKeyPayload(plaintext)
					if input.Secret != nil {
						source := "inline"
						if input.Source != nil {
							source = *input.Source
						} else if payload.IsRef() {
							source = "file"
						}
						switch source {
						case "file":
							if ipc.OwnerID(ctx) != "" {
								return nil, store.RejectServerSSHFile()
							}
							payload = vault.ReferencedPrivateKey(*input.Secret, payload.Passphrase)
						case "inline":
							payload = vault.InlinePrivateKey(*input.Secret, payload.Passphrase)
						default:
							return nil, ipc.BadParam(fmt.Errorf("未知的凭据来源 %q", source))
						}
					} else if input.Source != nil {
						current := "inline"
						if payload.IsRef() {
							current = "file"
						}
						if *input.Source != current {
							return nil, ipc.BadParam(fmt.Errorf("更改凭据来源需要提供新的密钥内容"))
						}
					}
					if input.Passphrase != nil {
						payload.Passphrase = normalizedPassphrase(input.Passphrase)
					}
					plaintext = payload.Encode()
				} else if input.Secret != nil {
					plaintext = *input.Secret
				}
				_, err = putProductionCredential(ctx, credentialVault, database, row.ID, name, row.Kind, plaintext)
				return nil, err
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func putProductionCredential(ctx context.Context, credentialVault *vault.Vault, database *store.Store, id, name, kind, plaintext string) (string, error) {
	nonce, blob, err := credentialVault.EncryptCredential(ctx, plaintext)
	if err != nil {
		return "", err
	}
	return database.CredentialPut(ctx, store.CredentialInput{
		ID: id, Name: name, Kind: kind, Nonce: nonce, Blob: blob, KEKHint: credentialVault.KEKHint(),
	})
}

func createProductionCredentialTx(ctx context.Context, credentialVault *vault.Vault, database *store.Store, tx *gorm.DB, name, kind, plaintext string) (string, error) {
	nonce, blob, err := credentialVault.EncryptCredential(ctx, plaintext)
	if err != nil {
		return "", err
	}
	return database.CredentialCreateTx(ctx, tx, store.CredentialInput{
		Name: name, Kind: kind, Nonce: nonce, Blob: blob, KEKHint: credentialVault.KEKHint(),
	})
}

func overwriteProductionCredentialTx(ctx context.Context, credentialVault *vault.Vault, database *store.Store, tx *gorm.DB, id string, expectedUpdatedAt int64, name, kind, plaintext string) error {
	nonce, blob, err := credentialVault.EncryptCredential(ctx, plaintext)
	if err != nil {
		return err
	}
	return database.CredentialUpdateCAS(ctx, tx, id, expectedUpdatedAt, store.CredentialInput{
		Name: name, Kind: kind, Nonce: nonce, Blob: blob, KEKHint: credentialVault.KEKHint(),
	})
}

func productionKeySource(payload vault.PrivateKeyPayload) *string {
	source := "inline"
	if payload.IsRef() {
		source = "file"
	}
	return &source
}

func normalizedPassphrase(passphrase *string) *string {
	if passphrase == nil {
		return nil
	}
	normalized := strings.TrimSpace(*passphrase)
	if normalized == "" {
		return nil
	}
	return &normalized
}
