// 全局对话框：UI 必须覆盖全面 —— ask / confirm / message 一律走应用内
// 自绘弹框（DialogHost），原生 plugin-dialog / window.confirm 只作为
// App 挂载前的兜底，正常路径下用户永远看到的是本应用样式的弹框。
//
// 注意：本模块不得反向 import store（避免循环依赖），弹框实现走注册制回调
// （App 挂载时 registerDialogHandlers），文本输入弹窗同理（registerPromptHandler）。
import { ask as dAsk, confirm as dConfirm, message as dMessage } from "@tauri-apps/plugin-dialog";
import { DEMO } from "../demo";

type AskOptions = { title?: string; kind?: "info" | "warning" | "error" };

type AskFn = (message: string, options?: AskOptions) => Promise<boolean>;
type ConfirmFn = (message: string) => Promise<boolean>;
type MessageFn = (message: string) => Promise<void>;

interface DialogHandlers {
  ask: AskFn;
  confirm: ConfirmFn;
  message: MessageFn;
}

let handlers: DialogHandlers | null = null;

/** App 挂载时注册应用内弹框实现（DialogHost）。 */
export function registerDialogHandlers(h: DialogHandlers) {
  handlers = h;
}

const nativeAsk: AskFn = DEMO
  ? async (message) => window.confirm(message)
  : dAsk;
const nativeConfirm: ConfirmFn = DEMO
  ? async (message) => window.confirm(message)
  : dConfirm;
const nativeMessage: MessageFn = DEMO
  ? async (message) => {
      window.alert(message);
    }
  : async (message) => {
      await dMessage(message);
    };

/** 确认框（取消 / 确定）。 */
export const ask: AskFn = (message, options) =>
  handlers ? handlers.ask(message, options) : nativeAsk(message, options);

/** 确认框（同 ask，无选项参数的别名场景）。 */
export const confirmDialog: ConfirmFn = (message) =>
  handlers ? handlers.confirm(message) : nativeConfirm(message);

/** 消息提示框（单按钮）。 */
export const messageBox: MessageFn = (message) =>
  handlers ? handlers.message(message) : nativeMessage(message);

/** 选一个本地文件（上传用）。取消返回 null。 */
export async function pickLocalFile(): Promise<string | null> {
  if (DEMO) {
    return "C:\\Users\\you\\Downloads\\nginx-access.log";
  }
  const { open } = await import("@tauri-apps/plugin-dialog");
  const picked = await open({ multiple: false });
  return typeof picked === "string" ? picked : null;
}

/** 选一个私钥文件（资产表单用）。取消返回 null。 */
export async function pickKeyFile(): Promise<string | null> {
  if (DEMO) {
    return "C:\\Users\\you\\.ssh\\id_ed25519";
  }
  const { open } = await import("@tauri-apps/plugin-dialog");
  const picked = await open({
    multiple: false,
    filters: [
      { name: "私钥文件", extensions: ["pem", "key", "ppk", "id_rsa", "id_ed25519", "openssh"] },
      { name: "所有文件", extensions: ["*"] },
    ],
  });
  return typeof picked === "string" ? picked : null;
}

/** 选一个本地保存路径（下载 / 录制用）。取消返回 null。 */
export async function pickSavePath(defaultName: string): Promise<string | null> {
  if (DEMO) {
    return `C:\\Users\\you\\Desktop\\${defaultName}`;
  }
  const { save } = await import("@tauri-apps/plugin-dialog");
  return save({ defaultPath: defaultName });
}

export type PromptOptions = {
  /** 强制多行输入框。不传则由弹窗按内容自动判断。 */
  multiLine?: boolean;
  /** 密码输入：单行 + 打码（解锁凭据库这类敏感输入用）。 */
  secret?: boolean;
};

type PromptFn = (
  message: string,
  value: string,
  options: PromptOptions,
) => Promise<string | null>;

let promptHandler: PromptFn | null = null;

/** App 挂载时注册文本输入弹窗实现。 */
export function registerPromptHandler(fn: PromptFn) {
  promptHandler = fn;
}

/** 文本输入弹窗（替代 window.prompt）。取消返回 null。 */
export function promptText(
  message: string,
  value = "",
  options: PromptOptions = {},
): Promise<string | null> {
  if (!promptHandler) {
    return Promise.resolve(DEMO ? window.prompt(message, value) : null);
  }
  return promptHandler(message, value, options);
}
