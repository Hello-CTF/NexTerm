// 全局自绘右键菜单：指针、触摸与键盘共用同一套动作。
import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { documentTabTarget, isImeKeyEvent, useOverlayFocus } from "./DialogHost";

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

const MARGIN = 8;

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
}: {
  items: MenuItem[];
  onClose: () => boolean;
  onExit?: () => void;
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
              aria-haspopup={hasSubmenu ? "menu" : undefined}
              aria-expanded={hasSubmenu ? expanded : undefined}
              aria-controls={hasSubmenu ? submenuId : undefined}
              onClick={() => {
                if (hasSubmenu) {
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
                  openForKeyboard(index, submenuId);
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
