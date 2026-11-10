import { useId, useRef, useState, useSyncExternalStore, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { KEYBINDING_ACTIONS, defaultExtraBindings, formatBinding, useKeybindings, type KeybindingActionId, type KeybindingSnapshot } from "../keybindings";
import { isMac } from "../platform";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconClose, IconCommand } from "../../ui/icons";
import { t } from "../../i18n";

type ShortcutArea = "navigation" | "panes" | "terminal" | "sidebars" | "security";

const AREA_LABELS: Record<ShortcutArea, string> = {
  navigation: t("导航"),
  panes: t("标签页与窗格"),
  terminal: t("终端"),
  sidebars: t("侧栏与 AI"),
  security: t("安全"),
};

const AREA_ORDER: ShortcutArea[] = ["navigation", "panes", "terminal", "sidebars", "security"];

// Every action names its area, so a new action cannot be left out of the cheat sheet.
const ACTION_AREAS: Record<KeybindingActionId, ShortcutArea> = {
  commandPalette: "navigation",
  quickConnect: "navigation",
  nextWorkspace: "navigation",
  previousWorkspace: "navigation",
  nextTab: "navigation",
  previousTab: "navigation",
  switchTab: "navigation",
  restoreClosedTab: "navigation",
  openSettings: "navigation",
  showShortcuts: "navigation",
  newTerminal: "panes",
  closeTab: "panes",
  splitRight: "panes",
  splitDown: "panes",
  closePane: "panes",
  focusPaneLeft: "panes",
  focusPaneRight: "panes",
  focusPaneUp: "panes",
  focusPaneDown: "panes",
  toggleZoomPane: "panes",
  equalizeSplits: "panes",
  terminalSearch: "terminal",
  terminalLinks: "terminal",
  terminalCopyMode: "terminal",
  terminalQuickSelect: "terminal",
  previousPrompt: "terminal",
  nextPrompt: "terminal",
  copyLastOutput: "terminal",
  generateCommand: "terminal",
  terminalFontIncrease: "terminal",
  terminalFontDecrease: "terminal",
  terminalFontReset: "terminal",
  reclaimTakeover: "terminal",
  toggleSidebar: "sidebars",
  toggleAiSidebar: "sidebars",
  sessionSidebar: "sidebars",
  focusAiInput: "sidebars",
  lockVault: "security",
  lockApp: "security",
};

export interface ShortcutRow {
  id: KeybindingActionId;
  label: string;
  /** Display chords in effect; empty when the action is unbound. */
  chords: string[];
}

export interface ShortcutGroup {
  area: ShortcutArea;
  label: string;
  rows: ShortcutRow[];
}

/** The current keymap grouped by area, keeping only rows whose label or chord contains `filter`. */
export function shortcutGroups(bindings: KeybindingSnapshot, filter: string, mac = isMac()): ShortcutGroup[] {
  const needle = filter.trim().toLowerCase();
  const rows = KEYBINDING_ACTIONS.map((action): ShortcutRow => {
    const binding = bindings[action.id];
    const chords = binding === null ? [] : [binding, ...defaultExtraBindings(action, binding, mac)].map(formatBinding);
    return { id: action.id, label: action.label, chords };
  }).filter(
    (row) =>
      !needle ||
      row.label.toLowerCase().includes(needle) ||
      row.chords.some((chord) => chord.toLowerCase().includes(needle)),
  );
  return AREA_ORDER.map((area) => ({
    area,
    label: AREA_LABELS[area],
    rows: rows.filter((row) => ACTION_AREAS[row.id] === area),
  })).filter((group) => group.rows.length > 0);
}

let open = false;
const listeners = new Set<() => void>();

function setOpen(next: boolean): void {
  if (open === next) return;
  open = next;
  for (const listener of listeners) listener();
}

/** Opens the shortcut cheat sheet, or closes it when it is already open. */
export function toggleShortcutsOverlay(): void {
  setOpen(!open);
}

export function closeShortcutsOverlay(): void {
  setOpen(false);
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Hosts the shortcut cheat sheet; it renders nothing until toggleShortcutsOverlay opens it. */
export function ShortcutsOverlayHost() {
  const shown = useSyncExternalStore(subscribe, () => open);
  return shown ? <ShortcutsOverlay onClose={closeShortcutsOverlay} /> : null;
}

export function ShortcutsOverlay({ onClose }: { onClose: () => void }) {
  const bindings = useKeybindings();
  const [filter, setFilter] = useState("");
  const modalRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  useOverlayFocus(true, modalRef, { initialFocus: () => inputRef.current });
  const groups = shortcutGroups(bindings, filter);

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (isImeKeyEvent(event.nativeEvent)) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      onClose();
      return;
    }
    trapOverlayTab(event, modalRef.current);
  };

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="nx-modal nx-shortcuts-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onKeyDown}
      >
        <div className="nx-modal-header">
          <span className="flex shrink-0 text-blue-400" aria-hidden="true">
            <IconCommand size={14} />
          </span>
          <span id={titleId} className="truncate">{t("快捷键速查")}</span>
          <div className="nx-spacer" />
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label={t("关闭")} onClick={onClose}>
            <IconClose size={13} />
          </button>
        </div>
        <div className="nx-modal-body">
          <input
            ref={inputRef}
            type="search"
            className="nx-input w-full"
            placeholder={t("按名称或按键筛选")}
            aria-label={t("筛选快捷键")}
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
          />
          <div className="mt-3 max-h-[60vh] overflow-y-auto">
            {groups.length === 0 && (
              <p role="status" className="py-6 text-center text-[12px] text-neutral-500">
                {t("没有匹配的快捷键")}
              </p>
            )}
            {groups.map((group) => (
              <section key={group.area} aria-label={group.label} className="mb-3">
                <h3 className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-neutral-500">{group.label}</h3>
                <dl className="grid grid-cols-[1fr_auto] gap-x-4 gap-y-1 text-[12.5px]">
                  {group.rows.map((row) => (
                    <div key={row.id} className="contents" data-shortcut={row.id}>
                      <dt className="text-neutral-200">{row.label}</dt>
                      <dd className="flex flex-wrap justify-end gap-1">
                        {row.chords.length === 0 ? (
                          <span className="text-neutral-500">{formatBinding(null)}</span>
                        ) : (
                          row.chords.map((chord) => (
                            <kbd key={chord} className="nx-kbd">
                              {chord}
                            </kbd>
                          ))
                        )}
                      </dd>
                    </div>
                  ))}
                </dl>
              </section>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
