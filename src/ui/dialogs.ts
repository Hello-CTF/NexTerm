// 全局对话框：UI 必须覆盖全面 —— ask / confirm / message 一律走应用内
// 自绘弹框（DialogHost），Wails 原生弹框 / window.confirm 只作为
// App 挂载前的兜底，正常路径下用户永远看到的是本应用样式的弹框。
//
// 注意：本模块不得反向 import store（避免循环依赖），弹框实现走注册制回调
// （App 挂载时 registerDialogHandlers），文本输入弹窗同理（registerPromptHandler）。
import {
  askWails,
  confirmWails,
  messageWails,
  openWailsFile,
  saveWailsFile,
} from "../ipc/wails";
import { DEMO, TRANSPORT } from "../demo";
import { isMac } from "../app/platform";
import {
  deliverStaged,
  dropStaged,
  isStagedPath,
  pickBrowserFile,
  requestSaveTarget,
  stageFile,
} from "../ipc/webFiles";

export { dropStaged as discardStaged };

type AskOptions = { title?: string; kind?: "info" | "warning" | "error" };

type AskFn = (message: string, options?: AskOptions) => Promise<boolean>;
type ConfirmFn = (message: string) => Promise<boolean>;
type MessageFn = (message: string) => Promise<void>;

/** 多选一弹框的一个选项（key 由调用方定义，原样回传）。 */
export interface ChoiceOption {
  key: string;
  label: string;
  hint?: string;
  danger?: boolean;
  primary?: boolean;
}

export type ChoiceOptions = {
  title?: string;
  level?: "info" | "warning";
  choices: ChoiceOption[];
};

/** 多选一：返回选中的 key；取消返回 null。 */
type ChooseFn = (message: string, options: ChoiceOptions) => Promise<string | null>;

interface DialogHandlers {
  ask: AskFn;
  confirm: ConfirmFn;
  message: MessageFn;
  choose: ChooseFn;
}

let handlers: DialogHandlers | null = null;

type DeferredTask = () => void;

function createDeferredQueue() {
  let running = false;
  const pending: DeferredTask[] = [];
  return <T>(start: () => Promise<T>): Promise<T> =>
    new Promise<T>((resolve, reject) => {
      const run = () => {
        running = true;
        let result: Promise<T>;
        try {
          result = start();
        } catch (error) {
          result = Promise.reject(error);
        }
        result.then(resolve, reject).finally(() => {
          running = false;
          pending.shift()?.();
        });
      };
      if (running) pending.push(run);
      else run();
    });
}

const deferDialog = createDeferredQueue();
const deferPrompt = createDeferredQueue();

/** App 挂载时注册应用内弹框实现（DialogHost）。 */
export function registerDialogHandlers(h: DialogHandlers) {
  handlers = h;
}

/** App 挂载前：Web/demo 用浏览器弹框，desktop 用 Wails 原生弹框。 */
const NATIVE_BROWSER_DIALOG = DEMO || TRANSPORT === "web";

const nativeAsk: AskFn = NATIVE_BROWSER_DIALOG
  ? async (message) => window.confirm(message)
  : askWails;
const nativeConfirm: ConfirmFn = NATIVE_BROWSER_DIALOG
  ? async (message) => window.confirm(message)
  : confirmWails;
const nativeMessage: MessageFn = NATIVE_BROWSER_DIALOG
  ? async (message) => {
      window.alert(message);
    }
  : messageWails;

/**
 * 多选一的兜底（App 还没挂载时）。
 *
 * 用 `window.confirm` 逐个问，比"静默返回 null"强：返回 null 等于把用户的
 * 关闭动作悄悄吞掉（标签没关、也没提示）。这个分支正常路径下不会走到 ——
 * 弹框只在用户点击之后才可能触发，那时 App 早已挂载。
 */
const nativeChoose: ChooseFn = async (message, options) => {
  for (const c of options.choices) {
    const text = `${message}\n\n${c.label}${c.hint ? `\n（${c.hint}）` : ""}`;
    if (window.confirm(text)) return c.key;
  }
  return null;
};
/** 确认框（取消 / 确定）。 */
export const ask: AskFn = (message, options) => {
  if (!handlers) return nativeAsk(message, options);
  const current = handlers;
  return deferDialog(() => current.ask(message, options));
};

/** 确认框（同 ask，无选项参数的别名场景）。 */
export const confirmDialog: ConfirmFn = (message) => {
  if (!handlers) return nativeConfirm(message);
  const current = handlers;
  return deferDialog(() => current.confirm(message));
};

/** 消息提示框（单按钮）。 */
export const messageBox: MessageFn = (message) => {
  if (!handlers) return nativeMessage(message);
  const current = handlers;
  return deferDialog(() => current.message(message));
};

/**
 * 多选一弹框（关闭终端标签的「后台继续运行 / 结束进程」用）。
 *
 * 和 ask / confirm 一样走注册制：App 挂载时注册应用内实现，调用方不必知道
 * 自己跑在哪种环境里。
 */
export function askChoice(message: string, options: ChoiceOptions): Promise<string | null> {
  if (!handlers) return nativeChoose(message, options);
  const current = handlers;
  return deferDialog(() => current.choose(message, options));
}

/** 选一个要上传的文件。取消返回 null；返回的是**内核能读到的路径**。
 *
 *  服务端模式下浏览器里没有「本地路径」这个概念，所以这里先把选中的字节
 *  交给服务端暂存，再把暂存后的盒子路径回上去 —— 调用点（`fsApi.upload`）
 *  因此完全不用改。用完调用 `discardStaged` 删掉副本，别让盒子攒垃圾。
 */
export async function pickLocalFile(): Promise<string | null> {
  if (DEMO) {
    return isMac()
      ? "~/Downloads/nginx-access.log"
      : "C:\\Users\\you\\Downloads\\nginx-access.log";
  }
  if (TRANSPORT === "web") {
    const picked = await pickBrowserFile();
    if (!picked) return null;
    const staged = await stageFile(picked);
    return staged.path;
  }
  return openWailsFile();
}

/** 选一个私钥文件（资产表单用）。取消返回 null。
 *
 *  服务端模式下**必须 `persist`**：这个路径会被存进资产、几天后才用于建连，
 *  落进会被巡检清理的暂存目录的话，用户的密钥认证会在两小时后自己失效。
 */
export async function pickKeyFile(): Promise<string | null> {
  if (DEMO) {
    return isMac() ? "~/.ssh/id_ed25519" : "C:\\Users\\you\\.ssh\\id_ed25519";
  }
  if (TRANSPORT === "web") {
    const picked = await pickBrowserFile();
    if (!picked) return null;
    const staged = await stageFile(picked, { persist: true });
    return staged.path;
  }
  return openWailsFile([
    {
      name: "私钥文件",
      extensions: ["pem", "key", "ppk", "id_rsa", "id_ed25519", "openssh"],
    },
    { name: "所有文件", extensions: ["*"] },
  ]);
}

/** 选一个保存落点（下载 / 录制用）。取消返回 null；返回的是**内核能写到的路径**。
 *
 *  服务端模式下先向服务端要一个空的暂存落点，同时**趁用户手势还在**把浏览器的
 *  保存句柄拿到手（`showSaveFilePicker()` 需要 transient activation，而内核落盘
 *  往往要几十秒，那时再问就过期了）。等内核（`fs_download` / 打包 / 日志导出）
 *  写完，再由调用点用 `finishSave` 把内容交给浏览器保存。
 *  「先要落点、后交付」是必然的：那时才有字节。
 *
 *  ⚠️ 本函数必须在用户手势（点击）的同步调用链里被 await，不要先等别的异步操作。
 */
export async function pickSavePath(defaultName: string): Promise<string | null> {
  if (DEMO) {
    return isMac() ? `~/Desktop/${defaultName}` : `C:\\Users\\you\\Desktop\\${defaultName}`;
  }
  if (TRANSPORT === "web") {
    return requestSaveTarget(defaultName);
  }
  return saveWailsFile(defaultName);
}

/**
 * 下载收尾：服务端模式下把暂存的内容交给浏览器保存，返回给提示语用的**落点描述**。
 *
 * 落点已在 `pickSavePath` 的手势期确认过，这里只是往那个句柄里写字节，
 * 不会再弹窗口；句柄没拿到时才退到锚点下载（见 `ipc/webFiles.ts` 文件头）。
 *
 * 描述刻意返回文件名，而不是那个盒子上的暂存路径 —— 后者对用户没有意义，
 * 打进 toast 只会让人以为「下载到盒子里了」。桌面模式原样返回真实路径。
 * 用户取消时返回 `null`（调用方报「已取消保存」）；交付本身出错时**抛出**，
 * 由调用方的 catch 如实报错 —— 不要把「没交出去」说成成功。
 */
export async function finishSave(target: string, name: string): Promise<string | null> {
  if (TRANSPORT !== "web") return target;
  const outcome = await deliverStaged(target, name);
  if (outcome === "cancelled") return null;
  if (outcome === "not-staged") {
    // web 模式下 `pickSavePath` 一定登记过，走到这里说明流程串了。文件已经取回但没交出去，
    // 静默丢掉是最坏的结果，所以明确报出来。
    console.warn("[dialogs] 期望是暂存落点，但没有登记记录", target);
  }
  return name;
}

/**
 * 把一个「内核用的路径」转成给用户看的描述。
 *
 * 服务端模式下 `pickSavePath` 返回的是暂存路径，直接显示（比如「开始录制 → …」）
 * 会让用户以为文件落在盒子上。桌面模式原样返回。
 */
export function describeTarget(path: string, name: string): string {
  return TRANSPORT === "web" && isStagedPath(path) ? name : path;
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
    return Promise.resolve(NATIVE_BROWSER_DIALOG ? window.prompt(message, value) : null);
  }
  const current = promptHandler;
  return deferPrompt(() => current(message, value, options));
}
