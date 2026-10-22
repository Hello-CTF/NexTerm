package production

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestKnownHostIPCAccountMissingOwnerFailsClosed(t *testing.T) {
	for _, withStore := range []bool{false, true} {
		name := "nil fallback"
		if withStore {
			name = "host key store"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			database := newKnownHostOwnerTestDB(t)
			ownerA := ipc.WithUserID(ctx, "owner-a")
			ownerB := ipc.WithUserID(ctx, "owner-b")
			for _, acceptance := range []struct {
				ctx         context.Context
				fingerprint string
			}{
				{ctx: ownerA, fingerprint: "SHA256:owner-a"},
				{ctx: ownerB, fingerprint: "SHA256:owner-b"},
			} {
				if err := database.KnownHostAccept(acceptance.ctx, "shared.test", 22, "ssh-ed25519", acceptance.fingerprint); err != nil {
					t.Fatal(err)
				}
			}
			rowA, found, err := database.KnownHostGet(ownerA, "shared.test", 22, "ssh-ed25519")
			if err != nil || !found {
				t.Fatalf("owner A ledger = %+v, %v, %v", rowA, found, err)
			}
			var hostKeys *productionHostKeyStore
			if withStore {
				hostKeys, err = newProductionHostKeyStore(ctx, filepath.Join(t.TempDir(), "known_hosts.json"), database, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			dispatcher := ipc.NewDispatcher()
			if err := registerStoreCommandsWithHostKeyMode(dispatcher, database, hostKeys, t.TempDir(), false, false); err != nil {
				t.Fatal(err)
			}
			dispatch := func(ctx context.Context, command, args string) ipc.Response {
				return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
			}
			for _, call := range []struct {
				command string
				args    string
			}{
				{command: "known_host_list", args: `null`},
				{command: "known_host_accept", args: `{"host":"new.test","port":22,"keyType":"ssh-ed25519","fingerprint":"SHA256:new"}`},
				{command: "known_host_remove", args: `{"id":` + strconvQuote(rowA.ID) + `}`},
			} {
				response := dispatch(ctx, call.command, call.args)
				if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
					t.Fatalf("%s without owner = %+v, want forbidden", call.command, response)
				}
				raw, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				for _, private := range [][]byte{[]byte("owner-a"), []byte("owner-b"), []byte("SHA256:owner-a"), []byte("SHA256:owner-b")} {
					if bytes.Contains(raw, private) {
						t.Fatalf("%s leaked ledger metadata in %s", call.command, raw)
					}
				}
			}
			rows, err := database.KnownHostListForMigration(ctx)
			if err != nil || len(rows) != 2 {
				t.Fatalf("identity-less IPC changed the ledger: %+v, %v", rows, err)
			}
			if _, found, err := database.KnownHostGet(ownerA, "shared.test", 22, "ssh-ed25519"); err != nil || !found {
				t.Fatalf("identity-less remove deleted owner A: found=%v err=%v", found, err)
			}
			response := dispatch(ownerA, "known_host_list", `null`)
			var owned []store.KnownHostRow
			requireStoreTestResponse(t, response, &owned)
			if len(owned) != 1 || owned[0].OwnerID != "owner-a" || owned[0].Fingerprint != "SHA256:owner-a" {
				t.Fatalf("owner A list = %+v", owned)
			}
		})
	}
}

func TestKnownHostIPCSharedNilAndStoreCompatibility(t *testing.T) {
	for _, withStore := range []bool{false, true} {
		name := "nil fallback"
		if withStore {
			name = "host key store"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			database := newKnownHostOwnerTestDB(t)
			if err := database.KnownHostAccept(ctx, "legacy.test", 22, "ssh-ed25519", "SHA256:legacy"); err != nil {
				t.Fatal(err)
			}
			legacy, found, err := database.KnownHostGet(ctx, "legacy.test", 22, "ssh-ed25519")
			if err != nil || !found {
				t.Fatalf("legacy ledger = %+v, %v, %v", legacy, found, err)
			}
			var hostKeys *productionHostKeyStore
			if withStore {
				hostKeys, err = newProductionHostKeyStore(ctx, filepath.Join(t.TempDir(), "known_hosts.json"), database, true)
				if err != nil {
					t.Fatal(err)
				}
			}
			dispatcher := ipc.NewDispatcher()
			if err := registerStoreCommandsWithHostKeyMode(dispatcher, database, hostKeys, t.TempDir(), false, true); err != nil {
				t.Fatal(err)
			}
			dispatch := func(command, args string) ipc.Response {
				return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
			}
			response := dispatch("known_host_list", `null`)
			var rows []store.KnownHostRow
			requireStoreTestResponse(t, response, &rows)
			if len(rows) != 1 || rows[0].ID != legacy.ID {
				t.Fatalf("shared list = %+v", rows)
			}
			requireProductionNullHostKey(t, dispatch("known_host_accept", `{"host":"new.test","port":22,"keyType":"ssh-ed25519","fingerprint":"SHA256:new"}`))
			requireProductionNullHostKey(t, dispatch("known_host_remove", `{"id":`+strconvQuote(legacy.ID)+`}`))
			response = dispatch("known_host_list", `null`)
			rows = nil
			requireStoreTestResponse(t, response, &rows)
			if len(rows) != 1 || rows[0].Host != "new.test" || rows[0].OwnerID != "" {
				t.Fatalf("shared accept/remove = %+v", rows)
			}
		})
	}
}
