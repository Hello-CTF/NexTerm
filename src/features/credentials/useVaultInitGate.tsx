import { useCallback, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { vaultApi, type VaultStatus } from "../../ipc/commands";
import { VaultInitModal } from "./VaultInitModal";
import { useVaultUnlock } from "./useVaultUnlock";

type GateSession = {
  promise: Promise<boolean>;
  resolve: (ok: boolean) => void;
};

export function useVaultInitGate(): {
  ensureVaultInit: () => Promise<boolean>;
  ensureVaultReady: (unlockReason?: string) => Promise<boolean>;
  vaultInitGate: ReactNode;
  vaultStatus: VaultStatus | undefined;
} {
  const qc = useQueryClient();
  const unlock = useVaultUnlock();
  const status = useQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status() });
  const [gate, setGate] = useState<GateSession | null>(null);

  const fetchStatus = useCallback(async (): Promise<VaultStatus | undefined> => {
    try {
      return await qc.fetchQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status() });
    } catch {
      return undefined;
    }
  }, [qc]);

  const ensureVaultInit = useCallback(async () => {
    let st = status.data;
    if (!st) {
      st = await fetchStatus();
      if (!st) return true;
    }
    if (st.initialized) return true;
    if (gate) return gate.promise;
    let resolve!: (ok: boolean) => void;
    const promise = new Promise<boolean>((r) => {
      resolve = r;
    });
    setGate({ promise, resolve });
    return promise;
  }, [fetchStatus, status.data, gate]);

  const ensureVaultReady = useCallback(
    async (unlockReason?: string) => {
      const st = await fetchStatus();
      if (!st) return true;
      if (!st.initialized) return ensureVaultInit();
      if (st.unlocked) return true;
      return unlock(unlockReason);
    },
    [fetchStatus, ensureVaultInit, unlock],
  );

  const settle = (ok: boolean) => {
    gate?.resolve(ok);
    setGate(null);
  };

  const vaultInitGate = gate ? (
    <VaultInitModal onClose={() => settle(false)} onInitialized={() => settle(true)} />
  ) : null;

  return { ensureVaultInit, ensureVaultReady, vaultInitGate, vaultStatus: status.data };
}
