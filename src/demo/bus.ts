// 演示模式的本地事件总线。
//
// 真机上 Rust → 前端的事件走 `tauri::event::listen`；演示模式下没有内核，
// 于是把同一套事件名在浏览器内用一个极简的发布订阅替代，
// 这样 main.tsx / FileBrowser 等处的 `listenEvent` 代码一行都不用改。

type Handler = (payload: unknown) => void;

const handlers = new Map<string, Set<Handler>>();

/** 发布一个事件（仅演示模式内部使用）。 */
export function emit(event: string, payload: unknown) {
  const set = handlers.get(event);
  if (!set) return;
  // 复制一份再遍历：handler 里可能会退订
  for (const h of [...set]) {
    try {
      h(payload);
    } catch {
      // 单个订阅者出错不影响其它订阅者
    }
  }
}

/** 订阅事件；返回退订函数。 */
export function subscribe(event: string, handler: Handler): () => void {
  let set = handlers.get(event);
  if (!set) {
    set = new Set();
    handlers.set(event, set);
  }
  set.add(handler);
  return () => {
    set.delete(handler);
    if (set.size === 0) handlers.delete(event);
  };
}

/** Tauri `Channel<T>` 在演示模式下只需要它的 `onmessage` 回调。 */
export interface MockChannel {
  onmessage?: (message: unknown) => void;
}

/** 向"通道"推一段文本（当作 PTY 输出）。 */
export function pushText(channel: unknown, text: string) {
  const c = channel as MockChannel | undefined;
  c?.onmessage?.(new TextEncoder().encode(text));
}

/** 向"通道"推一个结构化事件（AI 事件通道用）。 */
export function pushEvent(channel: unknown, event: Record<string, unknown>) {
  const c = channel as MockChannel | undefined;
  c?.onmessage?.(event);
}

/** 可取消的定时器集合，便于统一清理。 */
export function later(ms: number, fn: () => void): () => void {
  const t = window.setTimeout(fn, ms);
  return () => window.clearTimeout(t);
}
