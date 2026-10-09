import { useQueryClient } from "@tanstack/react-query";
import { vaultApi } from "../../ipc/commands";
import { promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";

export function useVaultUnlock(): (reason?: string) => Promise<boolean> {
  const qc = useQueryClient();
  const { pushToast } = useUi();

  return async (reason?: string) => {
    let passwordless = false;
    try {
      const st = await qc.fetchQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status() });
      passwordless = st.passwordless;
    } catch {
      passwordless = false;
    }
    if (passwordless) {
      try {
        await vaultApi.unlock("");
        void qc.invalidateQueries({ queryKey: ["vault-status"] });
        void qc.invalidateQueries({ queryKey: ["credentials"] });
        pushToast("success", "已解锁");
        return true;
      } catch (e) {
        pushToast("error", `解锁失败：${describeError(e)}`);
        return false;
      }
    }
    const pwd = await promptText(reason ?? "输入保护密码解锁凭据库", "", { secret: true });
    if (pwd === null) return false;
    try {
      await vaultApi.unlock(pwd);
      void qc.invalidateQueries({ queryKey: ["vault-status"] });
      void qc.invalidateQueries({ queryKey: ["credentials"] });
      pushToast("success", "已解锁");
      return true;
    } catch (e) {
      pushToast("error", `解锁失败：${describeError(e)}`);
      return false;
    }
  };
}

export function useRefreshCredentials(): () => void {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["credentials"] });
    void qc.invalidateQueries({ queryKey: ["vault-status"] });
    void qc.invalidateQueries({ queryKey: ["assets"] });
  };
}
