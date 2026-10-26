import {
  useCallback,
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
      // keepOpen runs the action without dismissing the menu, for repeated actions such as closing several rows.
      trailingAction?: { label: string; icon: ReactNode; disabled?: boolean; keepOpen?: boolean; onSelect: () => void };
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

function measureSafeBottom(): number {
  const probe = document.createElement("div");
  probe.style.position = "fixed";
  probe.style.visibility = "hidden";
  probe.style.pointerEvents = "none";
  probe.style.paddingBottom = "var(--nx-safe-bottom)";
  document.body.append(probe);
  const inset = Number.parseFloat(window.getComputedStyle(probe).paddingBottom);
  probe.remove();
  return Number.isFinite(inset) ? inset : 0;
}

function clampMenuStart(start: number, size: number, viewport: number, safeBottom: number): number {
  if (start + size + MARGIN + safeBottom <= viewport) return start;
  return Math.max(MARGIN, viewport - size - MARGIN - safeBottom);
}

function menuAvailableHeight(viewport: number, safeBottom: number): number {
  return Math.max(0, viewport - 2 * MARGIN - safeBottom);
}

// Does not use useCoarsePointer from app/platform: several terminal tests partially mock platform without exporting it.
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
  return [...menu.querySelectorAll<HTMLButtonElement>(":scope > .nx-menu-row > .nx-menu-item, :scope > .nx-menu-row > .nx-menu-trailing")].filter(
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
  const [pos, setPos] = useState<{ left: number; top: number; maxHeight: number } | null>(null);
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
    const { offsetWidth: width, offsetHeight: naturalHeight } = element;
    let left = state.x;
    if (left + width + MARGIN > window.innerWidth) left = Math.max(MARGIN, window.innerWidth - width - MARGIN);
    const safeBottom = measureSafeBottom();
    const maxHeight = menuAvailableHeight(window.innerHeight, safeBottom);
    const top = clampMenuStart(state.y, Math.min(naturalHeight, maxHeight), window.innerHeight, safeBottom);
    setPos({ left, top, maxHeight });
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
    const target = event.target instanceof Element ? event.target.closest<HTMLButtonElement>(".nx-menu-item, .nx-menu-trailing") : null;
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
        maxHeight: pos?.maxHeight,
        overflowY: "auto",
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
            {item.trailingAction && (
              <button
                type="button"
                role="menuitem"
                tabIndex={-1}
                className="nx-menu-trailing"
                aria-label={item.trailingAction.label}
                title={item.trailingAction.label}
                disabled={item.trailingAction.disabled}
                onClick={() => {
                  const action = item.trailingAction;
                  if (action?.keepOpen) action.onSelect();
                  else if (onClose()) action?.onSelect();
                }}
              >
                {item.trailingAction.icon}
              </button>
            )}
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
  const [position, setPosition] = useState<{ left: number; top: number; maxHeight: number } | null>(null);

  const reposition = useCallback(() => {
    const element = ref.current;
    const row = element?.parentElement;
    if (!element || !row) return;
    const rect = element.getBoundingClientRect();
    const anchor = row.getBoundingClientRect();
    const safeBottom = measureSafeBottom();
    const maxHeight = menuAvailableHeight(window.innerHeight, safeBottom);
    let left = anchor.right + 2;
    if (left + rect.width + MARGIN > window.innerWidth) {
      left = Math.max(MARGIN, anchor.left - rect.width - 2);
    }
    const top = clampMenuStart(
      anchor.top - 5,
      Math.min(rect.height, maxHeight),
      window.innerHeight,
      safeBottom,
    );
    // Fixed positioning keeps the flyout outside the parent menu's scroll clipping.
    setPosition({ left, top, maxHeight });
  }, []);

  useLayoutEffect(() => {
    reposition();
    if (autoFocus) menuButtons(ref.current)[0]?.focus();
  }, [items, autoFocus, reposition]);

  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const ancestors: Element[] = [];
    for (let parent = element.parentElement; parent; parent = parent.parentElement) {
      if (parent.classList.contains("nx-menu") || parent.classList.contains("nx-submenu")) {
        ancestors.push(parent);
      }
    }
    for (const ancestor of ancestors) ancestor.addEventListener("scroll", reposition);
    return () => {
      for (const ancestor of ancestors) ancestor.removeEventListener("scroll", reposition);
    };
  }, [reposition]);

  return (
    <div
      ref={ref}
      id={id}
      className="nx-submenu"
      role="menu"
      aria-label={label}
      style={position ? {
        position: "fixed",
        left: position.left,
        top: position.top,
        maxHeight: position.maxHeight,
        overflowY: "auto",
        marginLeft: 0,
        marginRight: 0,
        visibility: "visible",
      } : { visibility: "hidden" }}
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
    // Return to the first level and reset drag state when the menu changes
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
    const target = event.target instanceof Element ? event.target.closest<HTMLButtonElement>(".nx-menu-item, .nx-menu-trailing") : null;
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
