// 会话跟随滚动：只有用户**还停在最新输出**时才自动滚到底。
//
// 旧实现是 items 一变就无条件 scrollTo 底部 —— 用户往上翻排查问题时，
// 下一个 token 就把视图拽回去，长流式输出期间根本没法读历史。
// 这里换成三态口径：
//   · 跟随中：新输出 ⇒ 滚到底；
//   · 用户上翻（离底超过阈值）⇒ 停止跟随，位置一动不动；
//   · 停止跟随期间来了新输出 ⇒ 亮「回到最新」按钮，点了才下去并恢复跟随。
import { useEffect, useRef, useState, type RefObject } from "react";

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
  /** 非跟随期间又有新输出（用于渲染「回到最新」入口）。 */
  newOutput: boolean;
  /** 当前是否跟随（测试与调试可读；渲染只依赖 newOutput）。 */
  following: boolean;
  /** 回到底部并恢复跟随。 */
  jumpToLatest: () => void;
}

/**
 * @param scrollRef 消息流滚动容器
 * @param revision  内容版本（一般是 items；变化 ⇒ 需要做一次跟随判断）
 */
export function useConversationFollow(
  scrollRef: RefObject<HTMLElement | null>,
  revision: unknown,
): ConversationFollow {
  const followRef = useRef(true);
  const [following, setFollowing] = useState(true);
  const [newOutput, setNewOutput] = useState(false);
  const lastHeightRef = useRef(0);
  const jumpRef = useRef<() => void>(() => {});

  const setFollow = (value: boolean) => {
    followRef.current = value;
    setFollowing(value);
    if (value) setNewOutput(false);
  };

  jumpRef.current = () => {
    const el = scrollRef.current;
    setFollow(true);
    if (el) {
      scrollToBottom(el);
      lastHeightRef.current = el.scrollHeight;
    }
  };

  // 滚动监听只挂一次：follow 与否读 ref，不把 listener 换来换去。
  useEffect(() => {
    const el = scrollRef.current;
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
  }, []);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    if (followRef.current) {
      scrollToBottom(el);
    } else if (el.scrollHeight > lastHeightRef.current) {
      // 非跟随 + 内容确实长高了 ⇒ 才亮新输出入口；上翻本身不算新输出。
      setNewOutput(true);
    }
    lastHeightRef.current = el.scrollHeight;
    // revision 只表达"内容变了，重新判断一次"，不参与其它取值。
  }, [revision]);

  return { newOutput, following, jumpToLatest: () => jumpRef.current() };
}
