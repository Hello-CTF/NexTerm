import { useEffect, useRef, useState, type ReactNode } from "react";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import { IconChevronDown, IconClose, IconMaximize, IconMore } from "../../ui/icons";
import { filterCombos, MODIFIER_LABEL, type KeyCombo } from "./keys";
import { RELEASE_CHORD_LABEL } from "../../app/inputCapture";
import { FIXED_SCALES, MODIFIER_KEYS, RESOLUTION_PRESETS, type ModifierKey, type RemoteProtocol, type ScaleMode } from "./remoteTypes";
import type { TouchMode } from "./touchGestures";
import { t } from "../../i18n";

export type ResolutionChoice = "follow" | { width: number; height: number };

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
  // Asks for a typed width and height; absent where the host cannot prompt.
  onCustomResolution?: () => void;
  // The size the server currently shows, for the VNC note.
  desktopSize?: { width: number; height: number };
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
  return scale === "fit" ? t("适应窗口") : percentLabel(scale);
}

function resolutionLabel(choice: ResolutionChoice): string {
  return choice === "follow" ? t("跟随窗口") : `${choice.width} x ${choice.height}`;
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
      aria-label={t("发送组合键")}
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
        placeholder={t("搜索组合键")}
        aria-label={t("搜索组合键")}
        value={query}
        onChange={(event) => setQuery(event.target.value)}
      />
      <div className="max-h-64 overflow-y-auto">
        {combos.length === 0 && <div className="nx-hint px-1 py-2">{t("没有匹配的组合键")}</div>}
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
    { kind: "item", label: t("适应窗口"), accel: scale === "fit" ? t("当前") : undefined, onSelect: () => props.onScale("fit") },
    ...FIXED_SCALES.map<MenuItem>((value) => ({
      kind: "item",
      label: percentLabel(value),
      accel: scale === value ? t("当前") : undefined,
      // noVNC can only show the picture at its own size or fitted.
      disabled: protocol === "vnc" && value !== 1,
      onSelect: () => props.onScale(value),
    })),
  ];

  const clipboardItems = (): MenuItem[] => [
    { kind: "item", label: t("发送本机剪贴板到远程"), disabled: !props.clipboardEnabled || props.viewOnly, onSelect: props.onSendClipboard },
    { kind: "item", label: t("取回远程剪贴板到本机"), disabled: !props.clipboardEnabled, onSelect: props.onFetchClipboard },
    ...(props.clipboardEnabled ? [] : [{ kind: "item" as const, label: t("此资产已关闭剪贴板"), disabled: true }]),
  ];

  const resolutionItems = (): MenuItem[] => {
    if (protocol !== "rdp") {
      const size = props.desktopSize && props.desktopSize.width > 0 ? t("当前 {width} x {height}。", { width: props.desktopSize.width, height: props.desktopSize.height }) : "";
      return [
        { kind: "group", label: t("分辨率") },
        { kind: "item", label: t("VNC 的分辨率由服务器决定"), hint: t("{size}客户端无法修改，请在服务器端调整。", { size }), disabled: true },
      ];
    }
    const current = props.resolution;
    const isPreset = current !== "follow" && RESOLUTION_PRESETS.some((p) => p.width === current.width && p.height === current.height);
    return [
      { kind: "group", label: t("分辨率（仅本次连接）") },
      { kind: "item", label: t("跟随窗口大小"), accel: current === "follow" ? t("当前") : undefined, onSelect: () => props.onResolution("follow") },
      ...RESOLUTION_PRESETS.map<MenuItem>((preset) => ({
        kind: "item",
        label: `${preset.width} x ${preset.height}`,
        accel: current !== "follow" && current.width === preset.width && current.height === preset.height ? t("当前") : undefined,
        onSelect: () => props.onResolution({ ...preset }),
      })),
      ...(props.onCustomResolution
        ? [{ kind: "item" as const, label: t("自定义…"), accel: current !== "follow" && !isPreset ? t("当前") : undefined, onSelect: props.onCustomResolution }]
        : []),
      { kind: "separator" },
      { kind: "item", label: t("要长期使用某个分辨率，请在资产设置里修改"), disabled: true },
    ];
  };

  const moreItems = (): MenuItem[] => {
    const items: MenuItem[] = [];
    if (props.touch) {
      items.push(
        { kind: "group", label: t("触控方式") },
        { kind: "item", label: t("直接点按"), accel: props.touchMode === "direct" ? t("当前") : undefined, onSelect: () => props.onTouchMode("direct") },
        { kind: "item", label: t("虚拟触控板"), accel: props.touchMode === "trackpad" ? t("当前") : undefined, onSelect: () => props.onTouchMode("trackpad") },
        { kind: "separator" },
      );
    }
    items.push(
      { kind: "item", label: props.viewOnly ? t("退出只读模式") : t("只读模式"), hint: props.viewOnly ? t("当前不发送键盘和鼠标输入") : t("开启后不发送键盘和鼠标输入"), onSelect: () => props.onViewOnly(!props.viewOnly) },
      { kind: "item", label: t("释放键盘"), accel: RELEASE_CHORD_LABEL, disabled: !props.captured, onSelect: props.onReleaseKeyboard },
      { kind: "separator" },
      { kind: "item", label: t("断开连接"), hint: t("断开后会话结束"), danger: true, onSelect: props.onDisconnect },
    );
    return items;
  };

  return (
    <>
      <div
        className={`absolute top-1.5 left-1/2 z-20 -translate-x-1/2 transition-opacity duration-150 ${props.visible || pinned ? "opacity-100" : "pointer-events-none opacity-0"}`}
        role="toolbar"
        aria-label={t("远程桌面工具栏")}
        data-testid="remote-toolbar"
        data-visible={props.visible || pinned ? "true" : "false"}
      >
        <div className="relative flex max-w-[calc(100vw-1rem)] items-center gap-0.5 overflow-x-auto rounded-full border border-[var(--nx-border-strong)] bg-[color-mix(in_srgb,var(--nx-bg-raised)_92%,transparent)] px-1.5 py-1 shadow-lg backdrop-blur">
          <ToolButton onClick={(event) => openMenu(event, scaleItems(), t("缩放"))} title={t("缩放：{value}", { value: scaleLabel(scale) })}>
            {t("缩放")}
            <IconChevronDown size={11} />
          </ToolButton>
          <ToolButton onClick={() => setComboOpen((open) => !open)} pressed={comboOpen} title={t("发送组合键，如 Ctrl+Alt+Del")}>
            {t("按键")}
            <IconChevronDown size={11} />
          </ToolButton>
          {MODIFIER_KEYS.map((key) => (
            <ToolButton key={key} pressed={held.has(key)} onClick={() => props.onToggleHeld(key)} title={t("按住 {value}，再点一次释放", { value: MODIFIER_LABEL[key] })} disabled={props.viewOnly}>
              {MODIFIER_LABEL[key]}
            </ToolButton>
          ))}
          <ToolButton
            onClick={(event) => openMenu(event, resolutionItems(), t("分辨率"))}
            title={protocol === "rdp" ? t("分辨率：{value}（仅本次连接）", { value: resolutionLabel(props.resolution) }) : t("VNC 的分辨率由服务器决定")}
          >
            {t("分辨率")}
            <IconChevronDown size={11} />
          </ToolButton>
          <ToolButton onClick={(event) => openMenu(event, clipboardItems(), t("剪贴板"))} title={t("剪贴板")}>
            {t("剪贴板")}
            <IconChevronDown size={11} />
          </ToolButton>
          {props.canFullscreen && (
            <ToolButton onClick={props.onFullscreen} pressed={props.fullscreen} title={props.fullscreen ? t("退出全屏") : t("全屏")} label={t("全屏")}>
              <IconMaximize size={13} />
            </ToolButton>
          )}
          <ToolButton onClick={(event) => openMenu(event, moreItems())} title={t("更多")} label={t("更多")}>
            <IconMore size={13} />
          </ToolButton>
          <ToolButton onClick={props.onDisconnect} title={t("断开后会话结束")} label={t("断开连接")} danger>
            <IconClose size={13} />
          </ToolButton>
          {comboOpen && <ComboPopover onPick={props.onCombo} onClose={() => setComboOpen(false)} />}
        </div>
        <div className="sr-only" aria-live="polite">
          {props.viewOnly ? t("只读模式") : ""}
          {protocol === "rdp" ? t("分辨率 {value}", { value: resolutionLabel(props.resolution) }) : ""}
        </div>
      </div>
      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </>
  );
}
