import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
} from "react";
import { useUi } from "../app/store";
import { IconAlert, IconInfo } from "./icons";
import "./a11y.css";

const DEFAULT_TITLE = {
  ask: "确认",
  confirm: "确认",
  message: "提示",
} as const;

const FOCUSABLE = [
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "a[href]",
  "[contenteditable]:not([contenteditable='false'])",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

type OverlayEntry = {
  token: object;
  modal: boolean;
  element: () => HTMLElement | null;
  layer: HTMLElement;
  initialFocus?: () => HTMLElement | null;
  selectInitial: boolean;
  restoreFocus: HTMLElement | null;
  originalZIndex: string;
};

export interface OverlayFocusHandle {
  isTopmost: () => boolean;
  getReturnFocus: () => HTMLElement | null;
  setReturnFocus: (target: HTMLElement | null) => void;
}

type OverlayFocusOptions = {
  modal?: boolean;
  initialFocus?: () => HTMLElement | null;
  selectInitial?: boolean;
};

const overlays: OverlayEntry[] = [];
const inertRecords = new Map<HTMLElement, { inert: boolean; ariaHidden: string | null }>();
let scrollLock: { overflow: string } | null = null;

export function isImeKeyEvent(event: KeyboardEvent | ReactKeyboardEvent): boolean {
  const native = "nativeEvent" in event ? event.nativeEvent : event;
  return native.isComposing || event.keyCode === 229;
}

export function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) return true;
  if (target instanceof HTMLInputElement) return target.type !== "button" && target.type !== "checkbox" && target.type !== "radio";
  return target.isContentEditable || !!target.closest("[contenteditable]:not([contenteditable='false'])");
}

export function hasActiveOverlay(): boolean {
  return overlays.length > 0;
}

function focusableElements(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
    (element) =>
      element.tabIndex >= 0 &&
      !element.hidden &&
      element.getAttribute("aria-hidden") !== "true" &&
      !element.closest("[inert]"),
  );
}

function focusOverlay(entry: OverlayEntry): void {
  const root = entry.element();
  if (!root) return;
  const target = entry.initialFocus?.() ?? focusableElements(root)[0] ?? root;
  if (!target.isConnected || target.closest("[inert]")) return;
  target.focus({ preventScroll: true });
  if (
    entry.selectInitial &&
    (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement)
  ) {
    target.select();
  }
}

function restoreInertness(): void {
  for (const [element, record] of inertRecords) {
    element.inert = record.inert;
    if (!record.inert) element.removeAttribute("inert");
    if (record.ariaHidden === null) element.removeAttribute("aria-hidden");
    else element.setAttribute("aria-hidden", record.ariaHidden);
  }
  inertRecords.clear();
}

function syncOverlays(): void {
  overlays.forEach((entry, index) => {
    entry.layer.style.zIndex = String(90 + index);
  });

  const shouldLockScroll = overlays.some((entry) => entry.modal);
  if (shouldLockScroll && !scrollLock) {
    scrollLock = { overflow: document.body.style.overflow };
    document.body.style.overflow = "hidden";
  } else if (!shouldLockScroll && scrollLock) {
    document.body.style.overflow = scrollLock.overflow;
    scrollLock = null;
  }

  restoreInertness();
  const modalIndex = overlays.reduce((latest, entry, index) => (entry.modal ? index : latest), -1);
  if (modalIndex < 0) return;
  const root = overlays[modalIndex].element();
  if (!root) return;
  const upperLayers = overlays.slice(modalIndex + 1).map((entry) => entry.layer);

  let current = root;
  while (current.parentElement) {
    const parent = current.parentElement;
    for (const sibling of parent.children) {
      if (!(sibling instanceof HTMLElement) || sibling === current) continue;
      if (sibling.hasAttribute("data-a11y-announcer")) continue;
      if (upperLayers.some((layer) => sibling === layer || sibling.contains(layer))) continue;
      if (!inertRecords.has(sibling)) {
        inertRecords.set(sibling, {
          inert: sibling.inert || sibling.hasAttribute("inert"),
          ariaHidden: sibling.getAttribute("aria-hidden"),
        });
      }
      sibling.inert = true;
      sibling.setAttribute("inert", "");
      sibling.setAttribute("aria-hidden", "true");
    }
    if (parent === document.body) break;
    current = parent;
  }
}

export function useOverlayFocus<T extends HTMLElement>(
  active: unknown,
  ref: RefObject<T | null>,
  options: OverlayFocusOptions = {},
): OverlayFocusHandle {
  const tokenRef = useRef<object>({});
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const hasOpenRef = useRef(false);
  const optionsRef = useRef(options);
  optionsRef.current = options;
  const handleRef = useRef<OverlayFocusHandle | null>(null);
  if (!handleRef.current) {
    handleRef.current = {
      isTopmost: () => overlays.at(-1)?.token === tokenRef.current,
      getReturnFocus: () =>
        overlays.find((entry) => entry.token === tokenRef.current)?.restoreFocus ?? restoreFocusRef.current,
      setReturnFocus: (target) => {
        restoreFocusRef.current = target;
        const entry = overlays.find((candidate) => candidate.token === tokenRef.current);
        if (entry) entry.restoreFocus = target;
      },
    };
  }

  useLayoutEffect(() => {
    if (!active) {
      hasOpenRef.current = false;
      restoreFocusRef.current = null;
      return;
    }
    if (!ref.current) return;
    const element = ref.current;
    const currentFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const preserveReturnFocus =
      hasOpenRef.current &&
      (!currentFocus ||
        currentFocus === document.body ||
        !currentFocus.isConnected ||
        element.contains(currentFocus) ||
        overlays.some((entry) => entry.element()?.contains(currentFocus)));
    if (!preserveReturnFocus) restoreFocusRef.current = currentFocus;
    hasOpenRef.current = true;

    const entry: OverlayEntry = {
      token: tokenRef.current,
      modal: optionsRef.current.modal ?? true,
      element: () => ref.current,
      layer: element.closest<HTMLElement>(".nx-overlay") ?? element,
      initialFocus: optionsRef.current.initialFocus,
      selectInitial: optionsRef.current.selectInitial ?? false,
      restoreFocus: restoreFocusRef.current,
      originalZIndex: "",
    };
    entry.originalZIndex = entry.layer.style.zIndex;
    overlays.push(entry);
    syncOverlays();
    focusOverlay(entry);

    const onFocusIn = (event: FocusEvent) => {
      if (overlays.at(-1) !== entry || !entry.modal) return;
      const root = entry.element();
      if (!root || !(event.target instanceof Node) || root.contains(event.target)) return;
      focusOverlay(entry);
    };
    document.addEventListener("focusin", onFocusIn, true);

    return () => {
      document.removeEventListener("focusin", onFocusIn, true);
      const index = overlays.indexOf(entry);
      if (index >= 0) overlays.splice(index, 1);
      entry.layer.style.zIndex = entry.originalZIndex;
      syncOverlays();

      const target = entry.restoreFocus;
      queueMicrotask(() => {
        const top = overlays.at(-1);
        if (top) {
          const root = top.element();
          if (target?.isConnected && root?.contains(target) && !target.closest("[inert]")) {
            target.focus({ preventScroll: true });
          } else if (!root?.contains(document.activeElement)) {
            focusOverlay(top);
          }
        } else if (target?.isConnected && !target.closest("[inert]")) {
          target.focus({ preventScroll: true });
        }
      });
    };
  }, [active, ref]);

  return handleRef.current;
}

function isDocumentTabStop(element: HTMLElement): boolean {
  if (element.tabIndex < 0) return false;
  if (element.matches(":disabled")) return false;
  if (element.hidden) return false;
  if (element.closest('[aria-hidden="true"]')) return false;
  if (element.closest("[inert]")) return false;
  if (element.getClientRects().length === 0) return false;
  for (let current: HTMLElement | null = element; current; current = current.parentElement) {
    if (getComputedStyle(current).visibility === "hidden") return false;
  }
  return true;
}

export function documentTabTarget(
  origin: HTMLElement | null,
  direction: 1 | -1,
  exclude: HTMLElement,
): HTMLElement | null {
  const candidates = [...document.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
    (element) => !exclude.contains(element) && isDocumentTabStop(element),
  );
  if (!origin) return null;
  const index = candidates.indexOf(origin);
  if (index >= 0) return candidates[index + direction] ?? null;
  if (direction === 1) {
    return (
      candidates.find(
        (candidate) =>
          (origin.compareDocumentPosition(candidate) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0,
      ) ?? null
    );
  }
  return (
    [...candidates].reverse().find(
      (candidate) =>
        (origin.compareDocumentPosition(candidate) & Node.DOCUMENT_POSITION_PRECEDING) !== 0,
    ) ?? null
  );
}

export function trapOverlayTab(event: ReactKeyboardEvent, root: HTMLElement | null): void {
  if (event.key !== "Tab" || !root) return;
  const items = focusableElements(root);
  if (items.length === 0) {
    event.preventDefault();
    root.focus({ preventScroll: true });
    return;
  }
  const first = items[0];
  const last = items[items.length - 1];
  const current = document.activeElement;
  if (event.shiftKey && (current === first || !root.contains(current))) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && (current === last || !root.contains(current))) {
    event.preventDefault();
    first.focus();
  }
}

function ToastAnnouncer() {
  const toasts = useUi((state) => state.toasts);
  const error = [...toasts].reverse().find((toast) => toast.kind === "error");
  const status = [...toasts].reverse().find((toast) => toast.kind !== "error");
  return (
    <div className="nx-sr-only" data-a11y-announcer>
      <div key={error ? `error-${error.id}` : "error-empty"} role="alert" aria-atomic="true">
        {error?.text ?? ""}
      </div>
      <div key={status ? `status-${status.id}` : "status-empty"} role="status" aria-atomic="true">
        {status?.text ?? ""}
      </div>
    </div>
  );
}

export function DialogHost() {
  const dialog = useUi((state) => state.appDialog);
  const choice = useUi((state) => state.appChoice);
  const close = (value: boolean) => {
    if (!dialog || useUi.getState().appDialog !== dialog) return;
    useUi.setState({ appDialog: null });
    dialog.resolve(value);
  };
  const closeChoice = (value: string | null) => {
    if (!choice || useUi.getState().appChoice !== choice) return;
    useUi.setState({ appChoice: null });
    choice.resolve(value);
  };
  const active = choice ?? dialog;
  const modalRef = useRef<HTMLDivElement>(null);
  const initialButtonRef = useRef<HTMLButtonElement>(null);
  const layer = useOverlayFocus(active, modalRef, {
    initialFocus: () => initialButtonRef.current,
  });
  const titleId = useId();
  const descriptionId = useId();

  useEffect(() => {
    if (!active) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      if (choice) {
        if (useUi.getState().appChoice === choice) closeChoice(null);
      } else if (dialog && useUi.getState().appDialog === dialog) {
        close(dialog.kind === "message");
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [active, choice, dialog, close, closeChoice, layer]);

  const onModalKeyDown = (event: ReactKeyboardEvent) => {
    event.stopPropagation();
    if (layer.isTopmost()) trapOverlayTab(event, modalRef.current);
  };

  const cancelActive = () => {
    if (choice) {
      if (useUi.getState().appChoice === choice) closeChoice(null);
    } else if (dialog && useUi.getState().appDialog === dialog) {
      close(dialog.kind === "message");
    }
  };

  let content = null;
  if (choice) {
    const warning = choice.level === "warning";
    const defaultChoice =
      choice.options.find((option) => option.primary && !option.danger) ??
      choice.options.find((option) => !option.danger) ??
      null;
    content = (
      <div className="nx-overlay" onClick={cancelActive}>
        <div
          ref={modalRef}
          className="nx-modal max-w-[440px]"
          role={warning ? "alertdialog" : "dialog"}
          aria-modal="true"
          aria-labelledby={titleId}
          aria-describedby={descriptionId}
          tabIndex={-1}
          onClick={(event) => event.stopPropagation()}
          onKeyDown={onModalKeyDown}
        >
          <div className="nx-modal-header">
            <span className={`flex shrink-0 ${warning ? "text-amber-400" : "text-blue-400"}`} aria-hidden="true">
              {warning ? <IconAlert size={14} /> : <IconInfo size={14} />}
            </span>
            <span id={titleId} className="truncate">{choice.title}</span>
          </div>
          <div className="nx-modal-body">
            <div id={descriptionId} className="whitespace-pre-wrap text-[12.5px] leading-relaxed text-neutral-200">
              {choice.message}
            </div>
            <div className="mt-3.5 flex flex-col gap-2">
              {choice.options.map((option) => (
                <button
                  key={option.key}
                  ref={option === defaultChoice ? initialButtonRef : undefined}
                  type="button"
                  className={`w-full rounded-lg border px-3 py-2 text-left transition-colors ${
                    option.danger
                      ? "border-red-500/35 bg-red-950/30 hover:bg-red-950/55"
                      : "border-neutral-700/70 bg-neutral-900/60 hover:bg-neutral-800/80"
                  }`}
                  onClick={() => {
                    if (useUi.getState().appChoice === choice) closeChoice(option.key);
                  }}
                >
                  <span className={`block text-[12.5px] font-semibold ${option.danger ? "text-red-200" : "text-neutral-100"}`}>
                    {option.label}
                  </span>
                  {option.hint && (
                    <span className="mt-0.5 block text-[11px] leading-relaxed text-neutral-400">
                      {option.hint}
                    </span>
                  )}
                </button>
              ))}
            </div>
          </div>
          <div className="nx-modal-footer">
            <button
              ref={defaultChoice ? undefined : initialButtonRef}
              type="button"
              className="nx-btn nx-btn-ghost"
              onClick={cancelActive}
            >
              取消
            </button>
          </div>
        </div>
      </div>
    );
  } else if (dialog) {
    const warning = dialog.level === "warning";
    const Icon = warning ? IconAlert : IconInfo;
    const focusCancel = warning && dialog.kind !== "message";
    content = (
      <div className="nx-overlay" onClick={cancelActive}>
        <div
          ref={modalRef}
          className="nx-modal max-w-[420px]"
          role={warning ? "alertdialog" : "dialog"}
          aria-modal="true"
          aria-labelledby={titleId}
          aria-describedby={descriptionId}
          tabIndex={-1}
          onClick={(event) => event.stopPropagation()}
          onKeyDown={onModalKeyDown}
        >
          <div className="nx-modal-header">
            <span className={`flex shrink-0 ${warning ? "text-amber-300" : "text-blue-300"}`} aria-hidden="true">
              <Icon size={14} />
            </span>
            <span id={titleId} className="truncate">{dialog.title ?? DEFAULT_TITLE[dialog.kind]}</span>
          </div>
          <div className="nx-modal-body">
            <div id={descriptionId} className="whitespace-pre-wrap text-[12.5px] leading-relaxed text-neutral-200">
              {dialog.message}
            </div>
          </div>
          <div className="nx-modal-footer">
            {dialog.kind !== "message" && (
              <button
                ref={focusCancel ? initialButtonRef : undefined}
                type="button"
                className="nx-btn nx-btn-ghost"
                onClick={() => {
                  if (useUi.getState().appDialog === dialog) close(false);
                }}
              >
                取消
              </button>
            )}
            <button
              ref={focusCancel ? undefined : initialButtonRef}
              type="button"
              className={`nx-btn ${warning ? "nx-btn-danger-solid" : "nx-btn-primary"}`}
              onClick={() => {
                if (useUi.getState().appDialog === dialog) close(true);
              }}
            >
              确定
            </button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <>
      <ToastAnnouncer />
      {content}
    </>
  );
}
