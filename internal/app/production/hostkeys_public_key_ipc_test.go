package production

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	gossh "golang.org/x/crypto/ssh"
)

type hostKeyMaterialDetailDTO struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	Key         string `json:"key"`
	Changed     bool   `json:"changed"`
}

type hostKeyProbeMaterialDTO struct {
	Host        string                  `json:"host"`
	Port        int                     `json:"port"`
	KeyType     string                  `json:"keyType"`
	Fingerprint string                  `json:"fingerprint"`
	Key         string                  `json:"key"`
	State       string                  `json:"state"`
	Known       []hostKeyKnownDetailDTO `json:"known"`
}

func TestProductionKnownHostAcceptPersistsPresentedPublicKey(t *testing.T) {
	server := newHostKeyTestServer(t)
	firstKey := (*server.signer.Load()).PublicKey()
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	detail := requireHostKeyMaterialPending(t, response, false)
	requireHostKeyMaterialSelfConsistent(t, detail.KeyType, detail.Fingerprint, detail.Key, firstKey)

	requireProductionNullHostKey(t, acceptHostKeyMaterial(t, production, detail, detail.Key))
	rows := knownHostTestRows(t, production)
	if len(rows) != 1 || rows[0].Key != detail.Key || rows[0].Fingerprint != detail.Fingerprint {
		t.Fatalf("accept with material did not persist the presented key: %+v", rows)
	}

	connected := connectHostKeyTestAsset(t, production, assetID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))

	secondKey := server.rotateHostKey(t)
	response = dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	changed := requireHostKeyMaterialPending(t, response, true)
	requireHostKeyMaterialSelfConsistent(t, changed.KeyType, changed.Fingerprint, changed.Key, secondKey)

	requireProductionNullHostKey(t, acceptHostKeyMaterial(t, production, changed, changed.Key))
	rows = knownHostTestRows(t, production)
	if len(rows) != 1 || rows[0].Key != changed.Key || rows[0].Fingerprint != changed.Fingerprint {
		t.Fatalf("changed-key accept did not replace the recorded material: %+v", rows)
	}
	replacement := connectHostKeyTestAsset(t, production, assetID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+replacement.ID+`"}`))
}

func TestProductionKnownHostAcceptRejectsTamperedPublicKey(t *testing.T) {
	server := newHostKeyTestServer(t)
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	detail := requireHostKeyMaterialPending(t, response, false)

	_, otherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := gossh.NewSignerFromKey(otherPrivate)
	if err != nil {
		t.Fatal(err)
	}
	otherKey := base64.StdEncoding.EncodeToString(otherSigner.PublicKey().Marshal())

	ecdsaPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaSigner, err := gossh.NewSignerFromKey(ecdsaPrivate)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaKey := base64.StdEncoding.EncodeToString(ecdsaSigner.PublicKey().Marshal())

	for _, tampered := range []struct {
		name        string
		keyType     string
		fingerprint string
		key         string
	}{
		{name: "invalid base64", keyType: detail.KeyType, fingerprint: detail.Fingerprint, key: "!!!not-base64!!!"},
		{name: "unparseable material", keyType: detail.KeyType, fingerprint: detail.Fingerprint, key: base64.StdEncoding.EncodeToString([]byte("not an ssh public key"))},
		{name: "key type mismatch", keyType: detail.KeyType, fingerprint: gossh.FingerprintSHA256(ecdsaSigner.PublicKey()), key: ecdsaKey},
		{name: "fingerprint mismatch", keyType: detail.KeyType, fingerprint: detail.Fingerprint, key: otherKey},
	} {
		t.Run(tampered.name, func(t *testing.T) {
			claim := hostKeyMaterialDetailDTO{Host: detail.Host, Port: detail.Port, KeyType: tampered.keyType, Fingerprint: tampered.fingerprint}
			response := acceptHostKeyMaterial(t, production, claim, tampered.key)
			if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
				t.Fatalf("tampered accept = %+v, want bad_param", response)
			}
			if rows := knownHostTestRows(t, production); len(rows) != 0 {
				t.Fatalf("tampered accept persisted trust: %+v", rows)
			}
		})
	}

	requireProductionNullHostKey(t, acceptHostKeyMaterial(t, production, detail, detail.Key))
	claim := hostKeyMaterialDetailDTO{Host: detail.Host, Port: detail.Port, KeyType: detail.KeyType, Fingerprint: detail.Fingerprint}
	response = acceptHostKeyMaterial(t, production, claim, otherKey)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("tampered replacement = %+v, want bad_param", response)
	}
	rows := knownHostTestRows(t, production)
	if len(rows) != 1 || rows[0].Fingerprint != detail.Fingerprint || rows[0].Key != detail.Key {
		t.Fatalf("tampered replacement disturbed the trusted row: %+v", rows)
	}
	connected := connectHostKeyTestAsset(t, production, assetID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
}

func TestProductionKnownHostAcceptFingerprintOnlyStaysLegacy(t *testing.T) {
	server := newHostKeyTestServer(t)
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_connect", `{"args":{"assetId":"`+assetID+`"}}`)
	detail := requireHostKeyMaterialPending(t, response, false)

	requireProductionNullHostKey(t, acceptHostKeyMaterial(t, production, detail, ""))
	rows := knownHostTestRows(t, production)
	if len(rows) != 1 || rows[0].Fingerprint != detail.Fingerprint || rows[0].Key != "" {
		t.Fatalf("legacy fingerprint-only accept = %+v, want a material-less row", rows)
	}

	connected := connectHostKeyTestAsset(t, production, assetID)
	requireProductionNullHostKey(t, dispatchHostKeyTest(t, production, "session_disconnect", `{"sessionId":"`+connected.ID+`"}`))
	rows = knownHostTestRows(t, production)
	if len(rows) != 1 || rows[0].Key != detail.Key {
		t.Fatalf("connect after a legacy accept did not fill the presented material: %+v", rows)
	}
}

func TestProductionSessionProbeHostKeyCarriesPresentedPublicKey(t *testing.T) {
	server := newHostKeyTestServer(t)
	firstKey := (*server.signer.Load()).PublicKey()
	production := newHostKeyTestProduction(t)
	assetID := createHostKeyTestAsset(t, production, server.port())

	response := dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+assetID+`"}}`)
	var pending hostKeyProbeMaterialDTO
	requireStoreTestResponse(t, response, &pending)
	if pending.State != "pending" {
		t.Fatalf("unknown key probe state = %q", pending.State)
	}
	requireHostKeyMaterialSelfConsistent(t, pending.KeyType, pending.Fingerprint, pending.Key, firstKey)
	if listed := knownHostTestFingerprints(t, production); len(listed) != 0 {
		t.Fatalf("probe persisted trust for an unknown key: %+v", listed)
	}

	requireProductionNullHostKey(t, acceptHostKeyMaterial(t, production, hostKeyMaterialDetailDTO{
		Host: pending.Host, Port: pending.Port, KeyType: pending.KeyType, Fingerprint: pending.Fingerprint,
	}, pending.Key))
	secondKey := server.rotateHostKey(t)
	response = dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+assetID+`"}}`)
	var changed hostKeyProbeMaterialDTO
	requireStoreTestResponse(t, response, &changed)
	if changed.State != "changed" {
		t.Fatalf("rotated key probe state = %q", changed.State)
	}
	requireHostKeyMaterialSelfConsistent(t, changed.KeyType, changed.Fingerprint, changed.Key, secondKey)
	if len(changed.Known) != 1 || changed.Known[0].Fingerprint != pending.Fingerprint {
		t.Fatalf("changed probe lost the previous fingerprint: %+v", changed.Known)
	}
}

func TestProductionSessionProbeHostKeyJumpPendingCarriesPublicKey(t *testing.T) {
	jump := newSSHConnectorServer(t, nil)
	target := newSSHConnectorServer(t, nil)
	production := newHostKeyTestProduction(t)
	initConnectorTestVault(t, production)
	credID := createConnectorTestCredential(t, production, "jump-material-password", "secret")

	jumpAssetID := createConnectorTestAsset(t, production, "jump-material", "127.0.0.1", jump.port(), "password", credID, "")
	targetAssetID := createConnectorTestAsset(t, production, "target-material", "127.0.0.1", target.port(), "password", credID,
		`{"jumpAssetId":"`+jumpAssetID+`"}`)

	response := dispatchHostKeyTest(t, production, "session_probe_host_key", `{"args":{"assetId":"`+targetAssetID+`"}}`)
	detail := requireHostKeyMaterialPending(t, response, false)
	if detail.Port != jump.port() {
		t.Fatalf("pending endpoint = %+v, want the jump hop", detail)
	}
	requireHostKeyMaterialSelfConsistent(t, detail.KeyType, detail.Fingerprint, detail.Key, nil)
	if listed := knownHostTestFingerprints(t, production); len(listed) != 0 {
		t.Fatalf("probe persisted trust for the jump key: %+v", listed)
	}
}

func requireHostKeyMaterialPending(t *testing.T, response ipc.Response, changed bool) hostKeyMaterialDetailDTO {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeHostKeyPending {
		t.Fatalf("response = %+v, want host_key_pending", response)
	}
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatalf("host key detail is not serializable: %v", err)
	}
	var detail hostKeyMaterialDetailDTO
	if err := json.Unmarshal(encoded, &detail); err != nil {
		t.Fatalf("host key detail is malformed: %v", err)
	}
	if detail.Changed != changed {
		t.Fatalf("detail changed = %v, want %v: %+v", detail.Changed, changed, detail)
	}
	if detail.Host == "" || detail.Port == 0 || detail.KeyType == "" || detail.Fingerprint == "" {
		t.Fatalf("host key detail is incomplete: %+v", detail)
	}
	return detail
}

// requireHostKeyMaterialSelfConsistent proves the carried material is a parseable public key whose type and
// fingerprint match the claimed metadata; when presented is non-nil the material must be exactly that key.
func requireHostKeyMaterialSelfConsistent(t *testing.T, keyType, fingerprint, key string, presented gossh.PublicKey) {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("presented key material is not base64: %q", key)
	}
	parsed, err := gossh.ParsePublicKey(decoded)
	if err != nil {
		t.Fatalf("presented key material does not parse: %v", err)
	}
	if parsed.Type() != keyType || gossh.FingerprintSHA256(parsed) != fingerprint {
		t.Fatalf("presented key material does not match the detail metadata: %q %q", keyType, fingerprint)
	}
	if presented != nil && (parsed.Type() != presented.Type() || gossh.FingerprintSHA256(parsed) != gossh.FingerprintSHA256(presented)) {
		t.Fatalf("presented key material does not match the server key: %q", key)
	}
}

func acceptHostKeyMaterial(t *testing.T, production *Production, detail hostKeyMaterialDetailDTO, key string) ipc.Response {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"host": detail.Host, "port": detail.Port, "keyType": detail.KeyType, "fingerprint": detail.Fingerprint, "key": key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dispatchHostKeyTest(t, production, "known_host_accept", string(payload))
}

func knownHostTestRows(t *testing.T, production *Production) []store.KnownHostRow {
	t.Helper()
	response := dispatchHostKeyTest(t, production, "known_host_list", `null`)
	var rows []store.KnownHostRow
	requireStoreTestResponse(t, response, &rows)
	return rows
}
