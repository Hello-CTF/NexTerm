import { useCallback, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { vaultApi, type VaultStatus } from "../../ipc/commands";
import { VaultInitModal } from "./VaultInitModal";

type GateSession = {
  promise: Promise<boolean>;
  resolve: (ok: boolean) => void;
};

export function useVaultInitGate(): {
  ensureVaultInit: () => Promise<boolean>;
  vaultInitGate: ReactNode;
  vaultStatus: VaultStatus | undefined;
} {
  const qc = useQueryClient();
  const status = useQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status() });
  const [gate, setGate] = useState<GateSession | null>(null);

  const ensureVaultInit = useCallback(async () => {
    let st = status.data;
    if (!st) {
      try {
        st = await qc.fetchQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status() });
      } catch {
        return true;
      }
    }
    if (!st || st.initialized) return true;
    if (gate) return gate.promise;
    let resolve!: (ok: boolean) => void;
    const promise = new Promise<boolean>((r) => {
      resolve = r;
    });
    setGate({ promise, resolve });
    return promise;
  }, [qc, status.data, gate]);

  const settle = (ok: boolean) => {
    gate?.resolve(ok);
    setGate(null);
  };

  const vaultInitGate = gate ? (
    <VaultInitModal onClose={() => settle(false)} onInitialized={() => settle(true)} />
  ) : null;

  return { ensureVaultInit, vaultInitGate, vaultStatus: status.data };
}
