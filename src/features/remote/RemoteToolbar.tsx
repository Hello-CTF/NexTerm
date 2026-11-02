import { useEffect, useRef, useState, type ReactNode } from "react";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import { IconChevronDown, IconClose, IconMaximize, IconMore } from "../../ui/icons";
import { filterCombos, MODIFIER_LABEL, type KeyCombo } from "./keys";
import { RELEASE_CHORD_LABEL } from "../../app/inputCapture";
import { FIXED_SCALES, MODIFIER_KEYS, type ModifierKey, type RemoteProtocol, type ScaleMode } from "./remoteTypes";
import type { TouchMode } from "./touchGestures";

export type ResolutionChoice = "follow" | { width: number; height: number };

export const RESOLUTION_PRESETS = [
  { width: 1280, height: 720 },
  { width: 1600, height: 900 },
  { width: 1920, height: 1080 },
  { width: 2560, height: 1440 },
] as const;

export interface RemoteToolbarProps {
  protocol: RemoteProtocol;
  visible: boolean;
  scale: ScaleMode;
  onScale: (mode: ScaleMode) => void;
  held: ReadonlySet<ModifierKey>;
  onToggleHeld: (key: ModifierKey) => void;
  onCombo: (combo: KeyCombo) => void;
  clipboardEnabled: boolean;
  onSendClipboard: () => void;
  onFetchClipboard: () => void;
  resolution: ResolutionChoice;
  onResolution: (choice: ResolutionChoice) => void;
  viewOnly: boolean;
  onViewOnly: (viewOnly: boolean) => void;
  captured: boolean;
  onReleaseKeyboard: () => void;
  fullscreen: boolean;
  canFullscreen: boolean;
  onFullscreen: () => void;
  touch: boolean;
  touchMode: TouchMode;
  onTouchMode: (mode: TouchMode) => void;
  onDisconnect: () => void;
  // The toolbar stays on screen while true: a menu is open or a modifier is held.
  onPinnedChange: (pinned: boolean) => void;
  onReveal: () => void;
}

function percentLabel(scale: number): string {
  return `${Math.round(scale * 100)}%`;
}

function scaleLabel(scale: ScaleMode): string {
  return scale === "fit" ? "适应窗口" : percentLabel(scale);
}

function resolutionLabel(choice: ResolutionChoice): string {
  return choice === "follow" ? "跟随窗口" : `${choice.width} x ${choice.height}`;
}

function ToolButton({
  children,
  onClick,
  pressed,
  title,
  disabled,
  danger,
  label,
}: {
  children: ReactNode;
  onClick: (event: React.MouseEvent<HTMLButtonElement>) => void;
  pressed?: boolean;
  title?: string;
  disabled?: boolean;
  danger?: boolean;
  label?: string;
}) {
  return (
    <button
      type="button"
      className={`nx-btn nx-btn-ghost nx-btn-sm ${pressed ? "is-active" : ""} ${danger ? "text-red-400" : ""}`}
      aria-pressed={pressed}
      aria-label={label}
      title={title}
      disabled={disabled}
      // Keep the remote screen's focus: a toolbar click must not drop the keyboard capture.
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

// The key-combination picker: grouped, searchable.
function ComboPopover({ onPick, onClose }: { onPick: (combo: KeyCombo) => void; onClose: () => void }) {
  const [query, setQuery] = useState("");
  const rootRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    inputRef.current?.focus();
    const onDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) onClose();
    };
    window.addEventListener("pointerdown", onDown, true);
    return () => window.removeEventListener("pointerdown", onDown, true);
  }, [onClose]);
  const combos = filterCombos(query);
  const groups = [...new Set(combos.map((combo) => combo.group))];
  return (
    <div
      ref={rootRef}
      role="dialog"
      aria-label="发送组合键"
      className="absolute top-full left-1/2 z-30 mt-1 w-[min(18rem,calc(100vw-1.5rem))] -translate-x-1/2 rounded-md border border-[var(--nx-border-strong)] bg-[var(--nx-bg-raised)] p-2 shadow-lg"
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          onClose();
        }
      }}
    >
      <input
        ref={inputRef}
        className="nx-input mb-2"
        placeholder="搜索组合键"
        aria-label="搜索组合键"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
      />
      <div className="max-h-64 overflow-y-auto">
        {combos.length === 0 && <div className="nx-hint px-1 py-2">没有匹配的组合键</div>}
        {groups.map((group) => (
          <div key={group} role="group" aria-label={group}>
            <div className="px-1 pt-1 pb-0.5 text-[11px] text-[var(--nx-fg-soft)]">{group}</div>
            {combos
              .filter((combo) => combo.group === group)
              .map((combo) => (
                <button
                  key={combo.id}
                  type="button"
                  className="flex w-full items-baseline justify-between gap-3 rounded px-2 py-1.5 text-left text-[12.5px] hover:bg-neutral-800"
                  onClick={() => {
                    onPick(combo);
                    onClose();
                  }}
                >
                  <span className="font-mono">{combo.label}</span>
                  {combo.hint && <span className="truncate text-[11.5px] text-[var(--nx-fg-soft)]">{combo.hint}</span>}
                </button>
              ))}
          </div>
        ))}
      </div>
    </div>
  );
}

export function RemoteToolbar(props: RemoteToolbarProps) {
  const { protocol, scale, held } = props;
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const [comboOpen, setComboOpen] = useState(false);
  const pinned = menu !== null || comboOpen || held.size > 0;
  const pinnedRef = useRef(props.onPinnedChange);
  pinnedRef.current = props.onPinnedChange;
  useEffect(() => {
    pinnedRef.current(pinned);
  }, [pinned]);

  const openMenu = (event: React.MouseEvent<HTMLElement>, items: MenuItem[], title?: string) => {
    const rect = event.currentTarget.getBoundingClientRect();
    setComboOpen(false);
    setMenu({ x: rect.left, y: rect.bottom + 4, items, title });
  };

  const scaleItems = (): MenuItem[] => [
    { kind: "item", label: "适应窗口", accel: scale === "fit" ? "当前" : undefined, onSelect: () => props.onScale("fit") },
    ...FIXED_SCALES.map<MenuItem>((value) => ({
      kind: "item",
      label: percentLabel(value),
      accel: scale === value ? "当前" : undefined,
      // noVNC can only show the picture at its own size or fitted.
      disabled: protocol === "vnc" && value !== 1,
      onSelect: () => props.onScale(value),
    })),
  ];

  const clipboardItems = (): MenuItem[] => [
    { kind: "item", label: "发送本机剪贴板到远程", disabled: !props.clipboardEnabled || props.viewOnly, onSelect: props.onSendClipboard },
    { kind: "item", label: "取回远程剪贴板到本机", disabled: !props.clipboardEnabled, onSelect: props.onFetchClipboard },
    ...(props.clipboardEnabled ? [] : [{ kind: "item" as const, label: "此资产已关闭剪贴板", disabled: true }]),
  ];

  const moreItems = (): MenuItem[] => {
    const items: MenuItem[] = [];
    if (protocol === "rdp") {
      items.push(
        { kind: "group", label: "分辨率" },
        { kind: "item", label: "跟随窗口大小", accel: props.resolution === "follow" ? "当前" : undefined, onSelect: () => props.onResolution("follow") },
        ...RESOLUTION_PRESETS.map<MenuItem>((preset) => ({
          kind: "item",
          label: `${preset.width} x ${preset.height}`,
          accel:
            props.resolution !== "follow" && props.resolution.width === preset.width && props.resolution.height === preset.height ? "当前" : undefined,
          onSelect: () => props.onResolution({ ...preset }),
        })),
        { kind: "separator" },
      );
    }
    if (props.touch) {
      items.push(
        { kind: "group", label: "触控方式" },
        { kind: "item", label: "直接点按", accel: props.touchMode === "direct" ? "当前" : undefined, onSelect: () => props.onTouchMode("direct") },
        { kind: "item", label: "虚拟触控板", accel: props.touchMode === "trackpad" ? "当前" : undefined, onSelect: () => props.onTouchMode("trackpad") },
        { kind: "separator" },
      );
    }
    items.push(
      { kind: "item", label: props.viewOnly ? "退出只读模式" : "只读模式", hint: props.viewOnly ? "当前不发送键盘和鼠标输入" : "开启后不发送键盘和鼠标输入", onSelect: () => props.onViewOnly(!props.viewOnly) },
      { kind: "item", label: "释放键盘", accel: RELEASE_CHORD_LABEL, disabled: !props.captured, onSelect: props.onReleaseKeyboard },
      { kind: "separator" },
      { kind: "item", label: "断开连接", hint: "断开后会话结束", danger: true, onSelect: props.onDisconnect },
    );
    return items;
  };

  return (
    <>
      <div
        className={`absolute top-1.5 left-1/2 z-20 -translate-x-1/2 transition-opacity duration-150 ${props.visible || pinned ? "opacity-100" : "pointer-events-none opacity-0"}`}
        role="toolbar"
        aria-label="远程桌面工具栏"
        data-testid="remote-toolbar"
        data-visible={props.visible || pinned ? "true" : "false"}
      >
        <div className="relative flex max-w-[calc(100vw-1rem)] items-center gap-0.5 overflow-x-auto rounded-full border border-[var(--nx-border-strong)] bg-[color-mix(in_srgb,var(--nx-bg-raised)_92%,transparent)] px-1.5 py-1 shadow-lg backdrop-blur">
          <ToolButton onClick={(event) => openMenu(event, scaleItems(), "缩放")} title={`缩放：${scaleLabel(scale)}`}>
            缩放
            <IconChevronDown size={11} />
          </ToolButton>
          <ToolButton onClick={() => setComboOpen((open) => !open)} pressed={comboOpen} title="发送组合键，如 Ctrl+Alt+Del">
            按键
            <IconChevronDown size={11} />
          </ToolButton>
          {MODIFIER_KEYS.map((key) => (
            <ToolButton key={key} pressed={held.has(key)} onClick={() => props.onToggleHeld(key)} title={`按住 ${MODIFIER_LABEL[key]}，再点一次释放`} disabled={props.viewOnly}>
              {MODIFIER_LABEL[key]}
            </ToolButton>
          ))}
          <ToolButton onClick={(event) => openMenu(event, clipboardItems(), "剪贴板")} title="剪贴板">
            剪贴板
            <IconChevronDown size={11} />
          </ToolButton>
          {props.canFullscreen && (
            <ToolButton onClick={props.onFullscreen} pressed={props.fullscreen} title={props.fullscreen ? "退出全屏" : "全屏"} label="全屏">
              <IconMaximize size={13} />
            </ToolButton>
          )}
          <ToolButton onClick={(event) => openMenu(event, moreItems())} title="更多" label="更多">
            <IconMore size={13} />
          </ToolButton>
          <ToolButton onClick={props.onDisconnect} title="断开后会话结束" label="断开连接" danger>
            <IconClose size={13} />
          </ToolButton>
          {comboOpen && <ComboPopover onPick={props.onCombo} onClose={() => setComboOpen(false)} />}
        </div>
        <div className="sr-only" aria-live="polite">
          {props.viewOnly ? "只读模式" : ""}
          {protocol === "rdp" ? `分辨率 ${resolutionLabel(props.resolution)}` : ""}
        </div>
      </div>
      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </>
  );
}
