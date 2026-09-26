// 全局对话框：Tauri WebView 禁用 window.confirm/alert/prompt，
// 统一走 tauri-plugin-dialog（ask/confirm/message）+ 注册制文本输入弹窗。
// 注意：本模块不得反向 import store（避免循环依赖），文本弹窗用注册制回调。
import { ask as dAsk, confirm as dConfirm, message as dMessage } from "@tauri-apps/plugin-dialog";

export const ask = dAsk;
export const confirmDialog = dConfirm;
export const messageBox = dMessage;

type PromptFn = (message: string, value: string) => Promise<string | null>;

let promptHandler: PromptFn | null = null;

/** App 挂载时注册文本输入弹窗实现。 */
export function registerPromptHandler(fn: PromptFn) {
  promptHandler = fn;
}

/** 文本输入弹窗（替代 window.prompt）。取消返回 null。 */
export function promptText(message: string, value = ""): Promise<string | null> {
  if (!promptHandler) {
    return Promise.resolve(null);
  }
  return promptHandler(message, value);
}
