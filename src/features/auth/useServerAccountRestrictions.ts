import { WEB } from "../../ipc/env";
import { useAuth } from "./store";

const ALTERNATIVES = "请改用跳板机或 SOCKS/HTTP 代理，私钥请上传私钥内容或使用凭据库中的私钥";

// Texts mirror the multi-user server rejections in internal/store/server_ssh_restrict.go and the import-source rejections in
// internal/app/production (ssh_import_ipc.go, inventory_ipc.go).
export const SERVER_RESTRICTION_TEXT = {
  proxyCommand: `多用户服务端不支持 SSH 选项「代理命令 proxyCommand」：它会在服务器上执行命令。${ALTERNATIVES}`,
  agentSocket: `多用户服务端不支持 SSH 选项「Agent socket agentSocket」：它会让服务器进程读取服务器上的 SSH Agent。${ALTERNATIVES}`,
  forwardAgent: `多用户服务端不支持 SSH 选项「转发 Agent forwardAgent」：它会使用服务器上的 SSH Agent。${ALTERNATIVES}`,
  agentAuth: `多用户服务端不支持 SSH 选项「认证方式 agent」：它会使用服务器进程的 SSH Agent。${ALTERNATIVES}`,
  keyPath: `多用户服务端不支持 SSH 选项「私钥路径 keyPath」：它会读取服务器上的文件。${ALTERNATIVES}`,
  certPath: `多用户服务端不支持 SSH 选项「证书路径 certPath」：它会读取服务器上的文件。${ALTERNATIVES}`,
  fileCredential: `多用户服务端不支持引用服务器文件的私钥凭据（会读取服务器上的文件）。${ALTERNATIVES}`,
  importSshServerPath: "多用户服务端不支持从服务器路径导入：它会读取服务器上的 SSH 配置和私钥",
  importTermius: "多用户服务端不支持从服务器路径导入：它会读取服务器上的 Termius 数据",
  importDockerContext: "多用户服务端不支持从服务器路径导入：它会读取服务器上的 Docker 上下文",
  importKubeconfig: "多用户服务端不支持从 kubeconfig 导入：它会读取服务器上的 kubeconfig，属于跨账号共享凭据",
  local: "多用户服务端不支持本地终端资产（会在服务器上启动 shell），请改用 SSH 资产或设备",
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
