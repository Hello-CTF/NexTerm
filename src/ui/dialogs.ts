// 全局对话框：Tauri WebView 禁用 window.confirm/alert/prompt，
// 统一走 tauri-plugin-dialog（ask/confirm/message）+ 注册制文本输入弹窗。
//
// 演示模式（纯浏览器）下 plugin-dialog 不可用，这里退化成浏览器原生对话框，
// 并把文件选择换成固定假路径，让上传/下载/录制这些流程也能走完。
//
// 注意：本模块不得反向 import store（避免循环依赖），文本弹窗用注册制回调。
import { ask as dAsk, confirm as dConfirm, message as dMessage } from "@tauri-apps/plugin-dialog";
import { DEMO } from "../demo";

type AskOptions = { title?: string; kind?: "info" | "warning" | "error" };

export const ask: (message: string, options?: AskOptions) => Promise<boolean> = DEMO
  ? async (message) => window.confirm(message)
  : dAsk;

export const confirmDialog = DEMO
  ? async (message: string) => window.confirm(message)
  : dConfirm;

export const messageBox = DEMO
  ? async (message: string) => {
      window.alert(message);
      return undefined;
    }
  : dMessage;

/** 选一个本地文件（上传用）。取消返回 null。 */
export async function pickLocalFile(): Promise<string | null> {
  if (DEMO) {
    return "C:\\Users\\you\\Downloads\\nginx-access.log";
  }
  const { open } = await import("@tauri-apps/plugin-dialog");
  const picked = await open({ multiple: false });
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
