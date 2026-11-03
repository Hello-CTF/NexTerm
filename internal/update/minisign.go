package update

import (
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/releasesig"
)

func (m *Manager) minisignTrust() (releasesig.PublicKey, bool, error) {
	encoded := m.MinisignPublicKey
	if encoded == "" {
		encoded = releasesig.EmbeddedPublicKey
	}
	if encoded == "" {
		if m.RequireSignedChecksums {
			return releasesig.PublicKey{}, false, fmt.Errorf("已启用发布签名验证，但未配置受信 minisign 公钥")
		}
		return releasesig.PublicKey{}, false, nil
	}
	key, err := releasesig.ParsePublicKey(encoded)
	if err != nil {
		return releasesig.PublicKey{}, false, fmt.Errorf("受信 minisign 公钥无效: %w", err)
	}
	return key, true, nil
}
