package production

import (
	"context"
	"fmt"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

type vaultPasswordRequest struct {
	Password    string `json:"password"`
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
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

func registerVaultCommands(dispatcher *ipc.Dispatcher, credentialVault *vault.Vault, database *store.Store) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "vault_status", func(_ context.Context, _ *ipc.Call, _ struct{}) (vault.Status, error) {
				return credentialVault.Status(), nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "vault_init_master", func(ctx context.Context, _ *ipc.Call, input vaultPasswordRequest) (any, error) {
				return nil, credentialVault.InitMaster(ctx, input.Password)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "vault_init_dpapi", func(ctx context.Context, _ *ipc.Call, _ struct{}) (any, error) {
				return nil, credentialVault.InitDPAPI(ctx)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "vault_unlock", func(ctx context.Context, _ *ipc.Call, input vaultPasswordRequest) (any, error) {
				return nil, credentialVault.UnlockMaster(ctx, input.Password)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "vault_lock", func(_ context.Context, _ *ipc.Call, _ struct{}) (any, error) {
				credentialVault.Lock()
				return nil, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "vault_change_password", func(ctx context.Context, _ *ipc.Call, input vaultPasswordRequest) (any, error) {
				return nil, credentialVault.ChangeMasterPassword(ctx, input.OldPassword, input.NewPassword)
			})
		},
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
						plaintext = vault.ReferencedPrivateKey(input.Secret, input.Passphrase).Encode()
					case "inline":
						plaintext = vault.InlinePrivateKey(input.Secret, input.Passphrase).Encode()
					default:
						return nil, ipc.BadParam(fmt.Errorf("unknown credential source %q", source))
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
				for _, row := range rows {
					usedBy, err := database.CredentialUsage(ctx, row.ID)
					if err != nil {
						return nil, err
					}
					item := credentialDTO{
						ID: row.ID, Name: row.Name, Kind: row.Kind, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, UsedBy: usedBy,
					}
					if unlocked && row.Kind == vault.KindPrivateKey {
						if plaintext, err := credentialVault.DecryptCredentialString(ctx, row); err == nil {
							payload := vault.ParsePrivateKeyPayload(plaintext)
							item.Source = productionKeySource(payload)
							item.RefPath = payload.File
							item.HasPassphrase = payload.Passphrase != nil
						}
					}
					result = append(result, item)
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
							payload = vault.ReferencedPrivateKey(*input.Secret, payload.Passphrase)
						case "inline":
							payload = vault.InlinePrivateKey(*input.Secret, payload.Passphrase)
						default:
							return nil, ipc.BadParam(fmt.Errorf("unknown credential source %q", source))
						}
					} else if input.Source != nil {
						current := "inline"
						if payload.IsRef() {
							current = "file"
						}
						if *input.Source != current {
							return nil, ipc.BadParam(fmt.Errorf("credential source cannot change without new secret"))
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
