// 会话跟随滚动：只有用户**还停在最新输出**时才自动滚到底。
//
// 旧实现是 items 一变就无条件 scrollTo 底部 —— 用户往上翻排查问题时，
// 下一个 token 就把视图拽回去，长流式输出期间根本没法读历史。
// 这里换成三态口径：
//   · 跟随中：新输出 ⇒ 滚到底；
//   · 用户上翻（离底超过阈值）⇒ 停止跟随，位置一动不动；
//   · 停止跟随期间来了新输出 ⇒ 亮「回到最新」按钮，点了才下去并恢复跟随。
//
// 元素生命周期：侧栏收起（rightOpen=false）时消息流整个卸载，重开是一个
// **新的 DOM 节点**。所以监听必须跟着元素走 —— 用 callback ref 拿到节点，
// 节点更换 / 卸载时重新挂监听，而不是挂载时抓一次就不管。
import { useCallback, useEffect, useRef, useState } from "react";

/** 离底多少像素以内算"还在最新输出"。留一点余量，吸收小数像素与弹性滚动。 */
export const FOLLOW_BOTTOM_THRESHOLD_PX = 32;

export interface ScrollMetrics {
  scrollTop: number;
  clientHeight: number;
  scrollHeight: number;
}

export function isNearBottom(
  metrics: ScrollMetrics,
  threshold: number = FOLLOW_BOTTOM_THRESHOLD_PX,
): boolean {
  return metrics.scrollHeight - metrics.scrollTop - metrics.clientHeight <= threshold;
}

/** jsdom 与个别老 WebView 没有 Element.scrollTo：退回直接写 scrollTop。 */
function scrollToBottom(el: HTMLElement) {
  const top = el.scrollHeight;
  if (typeof el.scrollTo === "function") {
    el.scrollTo({ top });
  } else {
    el.scrollTop = top;
  }
}

export interface ConversationFollow {
  /** 挂到消息流滚动容器上的 callback ref（元素更换时自动重挂监听）。 */
  scrollRef: (el: HTMLElement | null) => void;
  /** 非跟随期间又有新输出（用于渲染「回到最新」入口）。 */
  newOutput: boolean;
  /** 当前是否跟随（测试与调试可读；渲染只依赖 newOutput）。 */
  following: boolean;
  /** 回到底部并恢复跟随。 */
  jumpToLatest: () => void;
}

/**
 * @param revision 内容版本（一般是 items；变化 ⇒ 需要做一次跟随判断）
 */
export function useConversationFollow(revision: unknown): ConversationFollow {
  const [el, setEl] = useState<HTMLElement | null>(null);
  const elRef = useRef<HTMLElement | null>(null);
  const followRef = useRef(true);
  const [following, setFollowing] = useState(true);
  const [newOutput, setNewOutput] = useState(false);
  const lastHeightRef = useRef(0);
  const jumpRef = useRef<() => void>(() => {});

  const scrollRef = useCallback((node: HTMLElement | null) => {
    elRef.current = node;
    setEl(node);
  }, []);

  const setFollow = (value: boolean) => {
    followRef.current = value;
    setFollowing(value);
    if (value) setNewOutput(false);
  };

  jumpRef.current = () => {
    const node = elRef.current;
    setFollow(true);
    if (node) {
      scrollToBottom(node);
      lastHeightRef.current = node.scrollHeight;
    }
  };

  // 滚动监听跟着元素走：节点卸载（侧栏收起）⇒ 摘监听；
  // 新节点（侧栏重开）⇒ 重新挂上，跟随状态本身不丢。
  useEffect(() => {
    if (!el) return;
    const onScroll = () => {
      const near = isNearBottom(el);
      // 程序自己滚到底也会触发 scroll：那时 near=true，恰好恢复/保持跟随。
      if (near !== followRef.current) setFollow(near);
      if (near) setNewOutput(false);
      lastHeightRef.current = el.scrollHeight;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    lastHeightRef.current = el.scrollHeight;
    return () => el.removeEventListener("scroll", onScroll);
  }, [el]);

  useEffect(() => {
    if (!el) return;
    if (followRef.current) {
      scrollToBottom(el);
    } else if (el.scrollHeight > lastHeightRef.current) {
      // 非跟随 + 内容确实长高了 ⇒ 才亮新输出入口；上翻本身不算新输出。
      setNewOutput(true);
    }
    lastHeightRef.current = el.scrollHeight;
    // revision 只表达"内容变了，重新判断一次"，不参与其它取值。
  }, [el, revision]);

  return { scrollRef, newOutput, following, jumpToLatest: () => jumpRef.current() };
}
