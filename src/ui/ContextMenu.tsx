/**
 * 全局自绘右键菜单（替代 WebView 自带的浏览器菜单）。
 *
 * 为什么要自绘：Tauri 的 WebView 默认弹的是浏览器那个「返回 / 前进 / 重新加载 /
 * 检查元素」菜单 —— 在一个运维终端里既没用又出戏；而且文件树、终端、资产树各自
 * 需要的动作完全不同，只有自绘才能把动作摆到手指边上。
 *
 * 形态：可分组（带灰色小标）、可带快捷键提示（右侧等宽灰字）、
 * 可挂子菜单（悬停展开，右边放不下自动翻到左边）。
 *
 * 定位策略：先按鼠标点挂载，`useLayoutEffect` 里量到真实尺寸后再做视口避让。
 * 光靠 CSS 的 `max-height` 躲不开右侧溢出 —— 那是横向的。
 */
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";

export type MenuItem =
  | { kind: "separator" }
  /** 分组小标题。不参与交互，只是视觉分段。 */
  | { kind: "group"; label: string }
  | {
      kind: "item";
      label: string;
      icon?: ReactNode;
      /** 右侧快捷键提示，如 "Ctrl+C" / "Ctrl+Shift+D"。 */
      accel?: string;
      /** 右侧补充说明（和 accel 并存时 accel 更靠右）。 */
      hint?: string;
      danger?: boolean;
      disabled?: boolean;
      /** 有子菜单时点击不执行 onSelect，而是悬停展开。 */
      submenu?: MenuItem[];
      onSelect?: () => void;
    };

export interface ContextMenuState {
  x: number;
  y: number;
  items: MenuItem[];
  /** 菜单顶部的一行说明，通常是选中的路径或名称。 */
  title?: string;
}

/** 菜单离视口边缘至少留这么宽。 */
const MARGIN = 8;

export function ContextMenu({
  state,
  onClose,
}: {
  state: ContextMenuState | null;
  onClose: () => void;
}) {
  const boxRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);

  // 每次打开都重新量：不同节点的菜单项数量不同，尺寸也不同。
  // 量完之前先 visibility:hidden，避免在原始位置闪一下再跳走。
  useLayoutEffect(() => {
    if (!state) {
      setPos(null);
      return;
    }
    const el = boxRef.current;
    if (!el) return;
    const { offsetWidth: w, offsetHeight: h } = el;
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    let left = state.x;
    let top = state.y;
    if (left + w + MARGIN > vw) left = Math.max(MARGIN, vw - w - MARGIN);
    if (top + h + MARGIN > vh) top = Math.max(MARGIN, vh - h - MARGIN);
    setPos({ left, top });
  }, [state]);

  useEffect(() => {
    if (!state) return;
    // 捕获阶段监听：菜单项自己的 click 先跑，这里只负责"点别处就收"
    const onDown = (e: PointerEvent) => {
      if (boxRef.current?.contains(e.target as Node)) return;
      onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
    };
    const dismiss = () => onClose();
    window.addEventListener("pointerdown", onDown, true);
    window.addEventListener("keydown", onKey, true);
    window.addEventListener("blur", dismiss);
    window.addEventListener("resize", dismiss);
    // 滚动时不收掉的话，菜单会"漂"在旧位置指向别的东西，收掉更稳
    window.addEventListener("wheel", dismiss, { passive: true });
    return () => {
      window.removeEventListener("pointerdown", onDown, true);
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("blur", dismiss);
      window.removeEventListener("resize", dismiss);
      window.removeEventListener("wheel", dismiss);
    };
  }, [state, onClose]);

  if (!state) return null;

  return (
    <div
      ref={boxRef}
      className="nx-menu"
      role="menu"
      style={{
        left: pos?.left ?? state.x,
        top: pos?.top ?? state.y,
        visibility: pos ? "visible" : "hidden",
      }}
      onContextMenu={(e) => e.preventDefault()}
    >
      {state.title && (
        <div className="nx-menu-title" title={state.title}>
          {state.title}
        </div>
      )}
      <MenuList items={state.items} onClose={onClose} />
    </div>
  );
}

/**
 * 递归渲染一层菜单。
 *
 * 每层自己管「哪个子菜单开着」—— 这样悬停切换时不会互相踩，
 * 也不需要把状态提到顶层去。
 */
function MenuList({ items, onClose }: { items: MenuItem[]; onClose: () => void }) {
  const [openSub, setOpenSub] = useState<number | null>(null);

  return (
    <>
      {items.map((item, i) => {
        if (item.kind === "separator") return <div key={`sep-${i}`} className="nx-menu-sep" />;
        if (item.kind === "group") {
          return (
            <div key={`grp-${i}`} className="nx-menu-group">
              {item.label}
            </div>
          );
        }

        const sub = item.submenu;
        const hasSub = !!sub && sub.length > 0;

        return (
          <div
            key={`${item.label}-${i}`}
            className="nx-menu-row"
            // 子菜单在 DOM 上是这一行的后代，所以鼠标移进子菜单不会触发
            // 这一行的 mouseleave —— 不用额外写"延迟关闭"那套。
            onMouseEnter={() => setOpenSub(hasSub ? i : null)}
            onMouseLeave={() => setOpenSub((cur) => (cur === i ? null : cur))}
          >
            <button
              type="button"
              role="menuitem"
              className={`nx-menu-item ${item.danger ? "is-danger" : ""}`}
              disabled={item.disabled}
              onClick={() => {
                // 有子菜单的项只负责展开，点了不执行
                if (hasSub) return;
                // 先收菜单再执行：动作里可能弹模态框，菜单压在上面会很怪
                onClose();
                item.onSelect?.();
              }}
            >
              <span className="nx-menu-icon">{item.icon}</span>
              <span className="nx-menu-label">{item.label}</span>
              {item.hint && <span className="nx-menu-hint">{item.hint}</span>}
              {item.accel && <span className="nx-menu-accel">{item.accel}</span>}
              {hasSub && <span className="nx-menu-arrow">›</span>}
            </button>
            {hasSub && openSub === i && sub && <SubMenu items={sub} onClose={onClose} />}
          </div>
        );
      })}
    </>
  );
}

/** 子菜单：默认贴父菜单右侧；右边实在放不下就翻到左侧。 */
function SubMenu({ items, onClose }: { items: MenuItem[]; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const [flip, setFlip] = useState(false);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setFlip(el.getBoundingClientRect().right + MARGIN > window.innerWidth);
  }, [items]);

  return (
    <div ref={ref} className={`nx-submenu ${flip ? "is-flip" : ""}`} role="menu">
      <MenuList items={items} onClose={onClose} />
    </div>
  );
}
