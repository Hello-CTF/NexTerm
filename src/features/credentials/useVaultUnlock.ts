// 凭据库的解锁与刷新：左栏横幅、详情页、资产连接前拦截三处共用。
//
// 解锁流程本身只有三行（弹密码框 → unlock → 刷缓存），但**提示文案与失败处理
// 必须一致** —— 分散写的话，用户会在 A 处看到"已解锁"、在 B 处看到一句原始报错。
import { useQueryClient } from "@tanstack/react-query";
import { vaultApi } from "../../ipc/commands";
import { WEB } from "../../demo";
import { promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";

/**
 * 返回一个「弹密码框并解锁」的函数。
 * 用户取消返回 false；解锁失败已弹 toast 并返回 false。
 */
export function useVaultUnlock(): (reason?: string) => Promise<boolean> {
  const qc = useQueryClient();
  const { pushToast } = useUi();

  return async (reason?: string) => {
    // ⛔ 浏览器版（服务端）**不能弹密码框**：那里的密码是部署方注入的随机值
    // （懒猫 `stable_secret` / 自建时 `--master-key`），用户答不出来 —— 弹了就是死路。
    // 直接给一条可执行的恢复说明。
    if (WEB) {
      pushToast(
        "error",
        "凭据库已锁定：浏览器版的密码由服务端部署密钥托管，无法在此输入。重启服务端后会自动恢复。",
      );
      return false;
    }
    const pwd = await promptText(reason ?? "输入保护密码解锁凭据库：", "", { secret: true });
    if (pwd === null) return false;
    try {
      await vaultApi.unlock(pwd);
      void qc.invalidateQueries({ queryKey: ["vault-status"] });
      void qc.invalidateQueries({ queryKey: ["credentials"] });
      pushToast("success", "已解锁");
      return true;
    } catch (e) {
      pushToast("error", describeError(e));
      return false;
    }
  };
}

/** 凭据增删改之后统一刷新（凭据 / 状态 / 资产引用关系三者一起失效）。 */
export function useRefreshCredentials(): () => void {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["credentials"] });
    void qc.invalidateQueries({ queryKey: ["vault-status"] });
    void qc.invalidateQueries({ queryKey: ["assets"] });
  };
}
