// 在线更新的编排：启动时静默检查 → 用户确认 → 下载安装 → 重启生效。
//
// # 为什么集中在这里，而不是散在横幅 / 设置卡片里
//
// 两条理由，都是「分开写迟早出错」那一类：
//
//   · **查的结果要两处共用**。启动时那次检查的结果同时被顶部横幅和设置卡片读，
//     两处各查一次就是两次 GitHub 请求 —— 而未认证的 GitHub API 限流是
//     **按出口 IP** 算 60 次/小时的，一个办公室共用一条出口，很容易一起被限。
//   · **订阅与收尾必须成对**。进度订阅、复位按钮、弹重启确认，三件事只要有一件
//     漏了，症状分别是「事件监听泄漏」「按钮永远卡在正在更新」「装完了没提示」。
//     放在同一个 try/finally 里，它们不可能只做一半。
import { useEffect } from "react";
import { updateApi } from "../ipc/commands";
import { EVENTS, listenEvent, type UpdateProgressEvent } from "../ipc/events";
import { ask } from "../ui/dialogs";
import { describeError } from "../ui/errorText";
import { useUi } from "./store";

/**
 * 启动后延迟多久做那次静默检查。
 *
 * 刻意**不是 0**：应用刚起来时首屏要连会话、拉资产、建终端，这时候再抢一路网络
 * 只会让用户觉得「打开就卡了一下」。3 秒足够让首屏安顿完，用户也还没开始干活。
 */
const STARTUP_DELAY_MS = 3000;

/**
 * 启动时的静默检查。挂在 App 顶层，只跑一次。
 *
 * 失败**完全静默**：启动时查不到更新不该弹任何东西 —— 它既不是用户发起的操作，
 * 也几乎没有可操作性。何况绝大多数失败在 Rust 侧已经降级成正常返回了
 * （见 `src-tauri/src/update.rs` 的模块文档）。这里再兜一层是因为
 * 真出错时（IPC 断了）也没必要打扰用户。
 */
export function useStartupUpdateCheck(): void {
  useEffect(() => {
    let cancelled = false;
    // 卸载后不写 store：开发时的 StrictMode 双挂载会让这里跑两遍，
    // 而第二遍的定时器归属已经不存在的那个实例。
    const timer = window.setTimeout(() => {
      void updateApi
        .check()
        .then((info) => {
          if (!cancelled) useUi.getState().setUpdateInfo(info);
        })
        .catch(() => undefined);
    }, STARTUP_DELAY_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, []);
}

/** 手动查一次（设置页的「检查更新」按钮）。结果写进全局 `updateInfo`。 */
export async function checkForUpdate(): Promise<void> {
  const ui = useUi.getState();
  try {
    ui.setUpdateInfo(await updateApi.check());
  } catch (e) {
    // 手动触发时用户在**等结果**，静默会让人以为按钮坏了。
    ui.pushToast("error", `检查更新失败：${describeError(e)}`);
  }
}

/**
 * 下载并安装；装完问要不要立刻重启。
 *
 * 进度走 `update://progress` 事件而不是返回值：下载几十兆期间界面必须有反馈，
 * 而这条调用的返回值只该回答「成没成」。`total` 缺失时进度保持 `null`，
 * 界面据此画**不确定**进度条 —— 按 0% 画会被当成卡死。
 */
export async function installUpdate(): Promise<void> {
  const ui = useUi.getState();
  // 连点保护：两个下载并发写同一个安装包，结果是包损坏 + 一次验签失败。
  if (ui.updateInstalling) return;

  ui.setUpdateInstalling(true);
  ui.setUpdateProgress(null);

  let off: (() => void) | null = null;
  try {
    // ⚠️ `listenEvent` 返回的是 **Promise**（三态各自实现里，服务端那条要等
    // WS 连上）。必须先 await 到取消函数再发安装请求，否则进度事件已经开跑、
    // 取消函数还没拿到，`finally` 里就无从解绑。
    off = await listenEvent<UpdateProgressEvent>(EVENTS.updateProgress, (p) => {
      const ratio = p.total && p.total > 0 ? Math.min(1, p.downloaded / p.total) : null;
      useUi.getState().setUpdateProgress(ratio);
    });
    await updateApi.install();
  } catch (e) {
    useUi.getState().pushToast("error", `更新失败：${describeError(e)}`);
    return;
  } finally {
    off?.();
    useUi.getState().setUpdateInstalling(false);
    useUi.getState().setUpdateProgress(null);
  }

  // 走到这里说明装完了。
  //
  // ⚠️ Windows 上多半**走不到这里**：NSIS 安装器是独立进程，装完会自己拉起新版
  // 并把当前进程干掉 —— 这个 await 永远回不来是**预期行为**，不是卡住。
  // 下面这段实际只在 macOS / Linux 上执行（那两个平台替换的是磁盘上的包，
  // 跑着的仍是旧代码，必须重启）。
  //
  // 清掉检查结果：包已经装上了，横幅继续挂着「有新版本」就是在骗人。
  useUi.getState().setUpdateInfo(null);
  if (await ask("更新已安装。现在重启以使用新版本？")) {
    await restartApp();
  }
}

/** 重启应用。macOS / Linux 上装完新版本不重启仍是旧代码。 */
export async function restartApp(): Promise<void> {
  try {
    await updateApi.restart();
  } catch (e) {
    useUi.getState().pushToast("error", `重启失败：${describeError(e)}`);
  }
}
