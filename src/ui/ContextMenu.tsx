import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type TouchEvent as ReactTouchEvent,
} from "react";
import { documentTabTarget, isImeKeyEvent, useOverlayFocus } from "./DialogHost";
import "./contextMenuSheet.css";

export type MenuItem =
  | { kind: "separator" }
  | { kind: "group"; label: string }
  | {
      kind: "item";
      label: string;
      icon?: ReactNode;
      accel?: string;
      hint?: string;
      danger?: boolean;
      disabled?: boolean;
      submenu?: MenuItem[];
      onSelect?: () => void;
    };

export interface ContextMenuState {
  x: number;
  y: number;
  items: MenuItem[];
  title?: string;
}

type ActionMenuItem = Extract<MenuItem, { kind: "item" }>;

const MARGIN = 8;
const COARSE_POINTER_QUERY = "(pointer: coarse)";

// 不引用 app/platform 的 useCoarsePointer: 多个终端测试对 platform 的局部 mock 未导出它。
function subscribeCoarsePointer(callback: () => void): () => void {
  const query = window.matchMedia(COARSE_POINTER_QUERY);
  query.addEventListener("change", callback);
  return () => query.removeEventListener("change", callback);
}

function coarsePointerMatches(): boolean {
  return window.matchMedia(COARSE_POINTER_QUERY).matches;
}

function useCoarsePointer(): boolean {
  return useSyncExternalStore(subscribeCoarsePointer, coarsePointerMatches);
}

function menuButtons(menu: ParentNode | null): HTMLButtonElement[] {
  if (!menu) return [];
  return [...menu.querySelectorAll<HTMLButtonElement>(":scope > .nx-menu-row > .nx-menu-item")].filter(
    (button) => !button.disabled,
  );
}

export function ContextMenu({
  state,
  onClose,
}: {
  state: ContextMenuState | null;
  onClose: () => void;
}) {
  const coarse = useCoarsePointer();
  const Menu = coarse ? ContextMenuSheet : FloatingContextMenu;
  return <Menu state={state} onClose={onClose} />;
}

function FloatingContextMenu({
  state,
  onClose,
}: {
  state: ContextMenuState | null;
  onClose: () => void;
}) {
  const boxRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null);
  const onCloseRef = useRef(onClose);
  const closedRef = useRef(false);
  onCloseRef.current = onClose;
  const layer = useOverlayFocus(pos ? state : null, boxRef, {
    modal: false,
    initialFocus: () => menuButtons(boxRef.current)[0] ?? boxRef.current,
  });

  const close = (): boolean => {
    if (closedRef.current) return false;
    closedRef.current = true;
    onCloseRef.current();
    return true;
  };
  const closeRef = useRef(close);
  closeRef.current = close;

  useLayoutEffect(() => {
    closedRef.current = false;
    if (!state) {
      setPos(null);
      return;
    }
    const element = boxRef.current;
    if (!element) return;
    const { offsetWidth: width, offsetHeight: height } = element;
    let left = state.x;
    let top = state.y;
    if (left + width + MARGIN > window.innerWidth) left = Math.max(MARGIN, window.innerWidth - width - MARGIN);
    if (top + height + MARGIN > window.innerHeight) top = Math.max(MARGIN, window.innerHeight - height - MARGIN);
    setPos({ left, top });
  }, [state]);

  useEffect(() => {
    if (!state) return;
    const onDown = (event: PointerEvent) => {
      if (!layer.isTopmost() || boxRef.current?.contains(event.target as Node)) return;
      closeRef.current();
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      closeRef.current();
    };
    const dismiss = () => closeRef.current();
    const dismissOnScroll = () => {
      if (layer.isTopmost()) closeRef.current();
    };
    window.addEventListener("pointerdown", onDown, true);
    window.addEventListener("keydown", onKey, true);
    window.addEventListener("blur", dismiss);
    window.addEventListener("resize", dismiss);
    window.addEventListener("wheel", dismissOnScroll, { passive: true });
    return () => {
      window.removeEventListener("pointerdown", onDown, true);
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("blur", dismiss);
      window.removeEventListener("resize", dismiss);
      window.removeEventListener("wheel", dismissOnScroll);
    };
  }, [state, layer]);

  if (!state) return null;

  const onMenuKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (isImeKeyEvent(event) || !layer.isTopmost()) return;
    if (event.key === "Tab") {
      event.preventDefault();
      if (boxRef.current) {
        const target = documentTabTarget(
          layer.getReturnFocus(),
          event.shiftKey ? -1 : 1,
          boxRef.current,
        );
        layer.setReturnFocus(target);
      }
      close();
      return;
    }
    const target = event.target instanceof Element ? event.target.closest<HTMLButtonElement>(".nx-menu-item") : null;
    if (!target || !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    const items = menuButtons(target.closest("[role='menu']"));
    if (items.length === 0) return;
    event.preventDefault();
    const current = items.indexOf(target);
    const next =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? items.length - 1
          : event.key === "ArrowDown"
            ? (current + 1 + items.length) % items.length
            : (current - 1 + items.length) % items.length;
    items[next].focus();
  };

  return (
    <div
      ref={boxRef}
      className="nx-menu"
      role="menu"
      aria-label={state.title ?? "上下文菜单"}
      tabIndex={-1}
      style={{
        left: pos?.left ?? state.x,
        top: pos?.top ?? state.y,
        visibility: pos ? "visible" : "hidden",
      }}
      onContextMenu={(event) => event.preventDefault()}
      onKeyDown={onMenuKeyDown}
    >
      {state.title && (
        <div className="nx-menu-title" title={state.title}>
          {state.title}
        </div>
      )}
      <MenuList items={state.items} onClose={close} />
    </div>
  );
}

function MenuList({
  items,
  onClose,
  onExit,
  onOpenSubmenu,
}: {
  items: MenuItem[];
  onClose: () => boolean;
  onExit?: () => void;
  onOpenSubmenu?: (item: ActionMenuItem) => void;
}) {
  const [openSub, setOpenSub] = useState<number | null>(null);
  const [focusSub, setFocusSub] = useState(false);
  const itemRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const listId = useId();

  const openForKeyboard = (index: number, submenuId: string) => {
    if (openSub === index) {
      menuButtons(document.getElementById(submenuId))[0]?.focus();
      return;
    }
    setFocusSub(true);
    setOpenSub(index);
  };

  return (
    <>
      {items.map((item, index) => {
        if (item.kind === "separator") {
          return <div key={`sep-${index}`} className="nx-menu-sep" role="separator" />;
        }
        if (item.kind === "group") {
          return (
            <div key={`grp-${index}`} className="nx-menu-group" role="presentation">
              {item.label}
            </div>
          );
        }

        const submenu = item.submenu;
        const hasSubmenu = !!submenu && submenu.length > 0;
        const submenuId = `${listId}-submenu-${index}`;
        const expanded = hasSubmenu && openSub === index;
        const closeSubmenu = () => {
          setOpenSub(null);
          setFocusSub(false);
          itemRefs.current[index]?.focus();
        };

        return (
          <div
            key={`${item.label}-${index}`}
            className="nx-menu-row"
            onMouseEnter={() => {
              if (onOpenSubmenu) return;
              setFocusSub(false);
              setOpenSub(hasSubmenu ? index : null);
            }}
            onMouseLeave={() => setOpenSub((current) => (current === index ? null : current))}
          >
            <button
              ref={(element) => {
                itemRefs.current[index] = element;
              }}
              type="button"
              role="menuitem"
              tabIndex={-1}
              className={`nx-menu-item ${item.danger ? "is-danger" : ""}`}
              disabled={item.disabled}
              aria-haspopup={hasSubmenu && !onOpenSubmenu ? "menu" : undefined}
              aria-expanded={hasSubmenu && !onOpenSubmenu ? expanded : undefined}
              aria-controls={hasSubmenu && !onOpenSubmenu ? submenuId : undefined}
              onClick={() => {
                if (hasSubmenu) {
                  if (onOpenSubmenu) {
                    onOpenSubmenu(item);
                    return;
                  }
                  setFocusSub(true);
                  setOpenSub((current) => (current === index ? null : index));
                  return;
                }
                if (onClose()) item.onSelect?.();
              }}
              onKeyDown={(event) => {
                if (isImeKeyEvent(event)) return;
                if (event.key === "ArrowLeft" && onExit) {
                  event.preventDefault();
                  event.stopPropagation();
                  onExit();
                } else if (hasSubmenu && event.key === "ArrowRight") {
                  event.preventDefault();
                  event.stopPropagation();
                  if (onOpenSubmenu) onOpenSubmenu(item);
                  else openForKeyboard(index, submenuId);
                }
              }}
            >
              <span className="nx-menu-icon" aria-hidden="true">{item.icon}</span>
              <span className="nx-menu-label">{item.label}</span>
              {item.hint && <span className="nx-menu-hint">{item.hint}</span>}
              {item.accel && <span className="nx-menu-accel">{item.accel}</span>}
              {hasSubmenu && <span className="nx-menu-arrow" aria-hidden="true">›</span>}
            </button>
            {expanded && submenu && (
              <SubMenu
                id={submenuId}
                items={submenu}
                onClose={onClose}
                onExit={closeSubmenu}
                autoFocus={focusSub}
                label={item.label}
              />
            )}
          </div>
        );
      })}
    </>
  );
}

function SubMenu({
  id,
  items,
  onClose,
  onExit,
  autoFocus,
  label,
}: {
  id: string;
  items: MenuItem[];
  onClose: () => boolean;
  onExit: () => void;
  autoFocus: boolean;
  label: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [flip, setFlip] = useState(false);

  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    setFlip(element.getBoundingClientRect().right + MARGIN > window.innerWidth);
    if (autoFocus) menuButtons(element)[0]?.focus();
  }, [items, autoFocus]);

  return (
    <div
      ref={ref}
      id={id}
      className={`nx-submenu ${flip ? "is-flip" : ""}`}
      role="menu"
      aria-label={label}
    >
      <MenuList items={items} onClose={onClose} onExit={onExit} />
    </div>
  );
}

function ContextMenuSheet({
  state,
  onClose,
}: {
  state: ContextMenuState | null;
  onClose: () => void;
}) {
  const boxRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const closedRef = useRef(false);
  const closeTimerRef = useRef(0);
  const dragRef = useRef<{ startY: number; base: number } | null>(null);
  const [levels, setLevels] = useState<Array<{ label: string; items: MenuItem[] }>>([]);
  const [dragOffset, setDragOffset] = useState(0);
  const [dragging, setDragging] = useState(false);
  const [closing, setClosing] = useState(false);
  const [prevState, setPrevState] = useState(state);
  if (prevState !== state) {
    // 换菜单时回到第一级并复位拖动状态
    setPrevState(state);
    setLevels([]);
    setDragOffset(0);
    setDragging(false);
    setClosing(false);
  }
  const layer = useOverlayFocus(state, boxRef, {
    modal: true,
    initialFocus: () => menuButtons(listRef.current)[0] ?? boxRef.current,
  });

  const close = (): boolean => {
    if (closedRef.current) return false;
    closedRef.current = true;
    onCloseRef.current();
    return true;
  };
  const closeRef = useRef(close);
  closeRef.current = close;

  useLayoutEffect(() => {
    closedRef.current = false;
    dragRef.current = null;
  }, [state]);

  const levelDepth = levels.length;
  useEffect(() => {
    if (levelDepth > 0) menuButtons(listRef.current)[0]?.focus();
  }, [levelDepth]);

  useEffect(() => () => window.clearTimeout(closeTimerRef.current), []);

  useEffect(() => {
    if (!state) return;
    const onDown = (event: PointerEvent) => {
      if (!layer.isTopmost() || boxRef.current?.contains(event.target as Node)) return;
      closeRef.current();
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      closeRef.current();
    };
    const dismiss = () => closeRef.current();
    const dismissOnScroll = (event: WheelEvent) => {
      if (!layer.isTopmost() || boxRef.current?.contains(event.target as Node)) return;
      closeRef.current();
    };
    window.addEventListener("pointerdown", onDown, true);
    window.addEventListener("keydown", onKey, true);
    window.addEventListener("blur", dismiss);
    window.addEventListener("resize", dismiss);
    window.addEventListener("wheel", dismissOnScroll, { passive: true });
    return () => {
      window.removeEventListener("pointerdown", onDown, true);
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("blur", dismiss);
      window.removeEventListener("resize", dismiss);
      window.removeEventListener("wheel", dismissOnScroll);
    };
  }, [state, layer]);

  if (!state) return null;

  const current = levels.at(-1);
  const items = current?.items ?? state.items;

  const openLevel = (item: ActionMenuItem) => {
    const submenu = item.submenu;
    if (!submenu || submenu.length === 0) return;
    setLevels((stack) => [...stack, { label: item.label, items: submenu }]);
  };
  const backLevel = () => setLevels((stack) => stack.slice(0, -1));

  const onGrabberTouchStart = (event: ReactTouchEvent<HTMLDivElement>) => {
    if (event.touches.length !== 1) return;
    dragRef.current = { startY: event.touches[0].clientY, base: dragOffset };
    setDragging(true);
  };

  const onGrabberTouchMove = (event: ReactTouchEvent<HTMLDivElement>) => {
    const drag = dragRef.current;
    if (!drag || event.touches.length !== 1) return;
    setDragOffset(Math.max(0, drag.base + event.touches[0].clientY - drag.startY));
  };

  const onGrabberTouchEnd = () => {
    if (!dragRef.current) return;
    dragRef.current = null;
    setDragging(false);
    const height = boxRef.current?.offsetHeight ?? 0;
    const threshold = height > 0 ? Math.min(140, height / 3) : 140;
    if (dragOffset > threshold) {
      setClosing(true);
      setDragOffset(window.innerHeight);
      closeTimerRef.current = window.setTimeout(() => closeRef.current(), 180);
    } else {
      setDragOffset(0);
    }
  };

  const onSheetKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (isImeKeyEvent(event) || !layer.isTopmost()) return;
    if (event.key === "Tab") {
      event.preventDefault();
      if (boxRef.current) {
        const target = documentTabTarget(
          layer.getReturnFocus(),
          event.shiftKey ? -1 : 1,
          boxRef.current,
        );
        layer.setReturnFocus(target);
      }
      close();
      return;
    }
    if (event.key === "ArrowLeft" && levels.length === 0) {
      event.preventDefault();
      close();
      return;
    }
    const target = event.target instanceof Element ? event.target.closest<HTMLButtonElement>(".nx-menu-item") : null;
    if (!target || !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    const buttons = menuButtons(listRef.current);
    if (buttons.length === 0) return;
    event.preventDefault();
    const index = buttons.indexOf(target);
    const next =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? buttons.length - 1
          : event.key === "ArrowDown"
            ? (index + 1 + buttons.length) % buttons.length
            : (index - 1 + buttons.length) % buttons.length;
    buttons[next].focus();
  };

  return (
    <div className="nx-overlay nx-menu-sheet-overlay">
      <div
        ref={boxRef}
        className="nx-menu-sheet"
        role="menu"
        aria-label={state.title ?? "上下文菜单"}
        tabIndex={-1}
        style={{
          transform: dragOffset > 0 ? `translateY(${dragOffset}px)` : undefined,
          transition: dragging ? "none" : undefined,
          pointerEvents: closing ? "none" : undefined,
        }}
        onContextMenu={(event) => event.preventDefault()}
        onKeyDown={onSheetKeyDown}
      >
        <div
          className="nx-menu-sheet-grabber"
          onTouchStart={onGrabberTouchStart}
          onTouchMove={onGrabberTouchMove}
          onTouchEnd={onGrabberTouchEnd}
          onTouchCancel={onGrabberTouchEnd}
        >
          <div className="nx-menu-sheet-bar" />
        </div>
        {(current || state.title) && (
          <div className="nx-menu-sheet-header">
            {current ? (
              <button type="button" className="nx-menu-sheet-back" onClick={backLevel}>
                ‹ {current.label}
              </button>
            ) : (
              <div className="nx-menu-sheet-title" title={state.title}>
                {state.title}
              </div>
            )}
          </div>
        )}
        <div className="nx-menu-sheet-list" ref={listRef}>
          <MenuList
            items={items}
            onClose={close}
            onExit={current ? backLevel : undefined}
            onOpenSubmenu={openLevel}
          />
        </div>
      </div>
    </div>
  );
}
