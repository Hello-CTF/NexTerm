import { WEB } from "../../ipc/env";
import { useAuth } from "./store";
import { t } from "../../i18n";

const ALTERNATIVES = t("请改用跳板机或 SOCKS/HTTP 代理，私钥请上传私钥内容或使用凭据库中的私钥");

// Texts mirror the multi-user server rejections in internal/store/server_ssh_restrict.go and the import-source rejections in
// internal/app/production (ssh_import_ipc.go, inventory_ipc.go).
export const SERVER_RESTRICTION_TEXT = {
  proxyCommand: t("多用户服务端不支持 SSH 选项「代理命令 proxyCommand」：它会在服务器上执行命令。{ALTERNATIVES}", { ALTERNATIVES }),
  agentSocket: t("多用户服务端不支持 SSH 选项「Agent socket agentSocket」：它会让服务器进程读取服务器上的 SSH Agent。{ALTERNATIVES}", { ALTERNATIVES }),
  forwardAgent: t("多用户服务端不支持 SSH 选项「转发 Agent forwardAgent」：它会使用服务器上的 SSH Agent。{ALTERNATIVES}", { ALTERNATIVES }),
  agentAuth: t("多用户服务端不支持 SSH 选项「认证方式 agent」：它会使用服务器进程的 SSH Agent。{ALTERNATIVES}", { ALTERNATIVES }),
  keyPath: t("多用户服务端不支持 SSH 选项「私钥路径 keyPath」：它会读取服务器上的文件。{ALTERNATIVES}", { ALTERNATIVES }),
  certPath: t("多用户服务端不支持 SSH 选项「证书路径 certPath」：它会读取服务器上的文件。{ALTERNATIVES}", { ALTERNATIVES }),
  fileCredential: t("多用户服务端不支持引用服务器文件的私钥凭据（会读取服务器上的文件）。{ALTERNATIVES}", { ALTERNATIVES }),
  importSshServerPath: t("多用户服务端不支持从服务器路径导入：它会读取服务器上的 SSH 配置和私钥"),
  importTermius: t("多用户服务端不支持从服务器路径导入：它会读取服务器上的 Termius 数据"),
  importDockerContext: t("多用户服务端不支持从服务器路径导入：它会读取服务器上的 Docker 上下文"),
  importKubeconfig: t("多用户服务端不支持从 kubeconfig 导入：它会读取服务器上的 kubeconfig，属于跨账号共享凭据"),
  local: t("多用户服务端不支持本地终端资产（会在服务器上启动 shell），请改用 SSH 资产或设备"),
} as const;

export interface ServerAccountRestrictions {
  restricted: boolean;
  text: typeof SERVER_RESTRICTION_TEXT;
}

// The server restricts only requests that carry an account owner. The frontend knows that exactly when
// it runs in browser mode with a signed-in account; --auth=off and anonymous loopback sessions have no
// user, so nothing is restricted there, matching the backend.
export function useServerAccountRestrictions(): ServerAccountRestrictions {
  const signedIn = useAuth((s) => s.user !== null);
  return { restricted: WEB && signedIn, text: SERVER_RESTRICTION_TEXT };
}
