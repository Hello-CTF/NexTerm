import { useEffect, useId, useMemo, useRef, useState } from "react";
import { aiApi, desktopTrayApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import {
  setTerminalCloseAction,
  setTerminalDefaultMode,
  setTerminalGpuAcceleration,
  setTerminalLinkActivation,
  setTerminalLinkOpenMode,
  setTerminalConfirmMultilinePaste,
  setTerminalDurableHistoryMiB,
  setTerminalScrollback,
  setTerminalTrzsz,
  normalizeMultilinePasteConfirm,
  normalizeTerminalScrollback,
  terminalDefaultUsesAccount,
  DEFAULT_DURABLE_HISTORY_MIB,
  TERMINAL_SCROLLBACK_OPTIONS,
  useTerminalPrefs,
  useCtrlVPaste,
  setCtrlVPaste,
  setSelectionAutoCopy,
  useInputPrefs,
  type TerminalCloseAction,
  type TerminalDefaultMode,
  type TerminalGpuAcceleration,
  type TerminalLinkActivation,
  type TerminalLinkOpenMode,
  type TerminalMultilinePasteConfirm,
} from "../../app/preferences";
import { ask } from "../../ui/dialogs";
import { Select } from "../../ui/Select";
import { onSettingsSection } from "./settingsNavigation";
import { SettingsSearch } from "./SettingsSearch";
import {
  SETTINGS_ITEMS,
  SETTINGS_SECTIONS,
  searchSettings,
  type SettingsGroupId,
  type SettingsPanelId,
  type SettingsSearchResult,
} from "./settingsIndex";
import "./settingsSearch.css";
import { isEditableTarget } from "../../ui/DialogHost";
import { isWindows } from "../../app/platform";
import { isTouchShell } from "../../app/shell";
import { isMobileShell } from "../../app/shell/shellMode";
import { ModelManager } from "../ai/ModelPanel";
import { PrivacyCard } from "./PrivacyCard";
import { AiRulesCard } from "./AiRulesCard";
import { VaultCard } from "./VaultCard";
import { AppLockCard } from "./AppLockCard";
import { AppearanceCard } from "./AppearanceCard";
import { LocalHelperSettings } from "./LocalHelperSettings";
import { NotificationSettings } from "../inbox/NotificationSettings";
import { LocalHelperExposureSettings } from "./LocalHelperExposureSettings";
import { KnownHostsCard } from "./KnownHostsCard";
import { MemoryCard } from "./MemoryCard";
import { ProductionSettingsPanel } from "../production/ProductionSettingsPanel";
import { ShortcutsCard } from "./ShortcutsCard";
import { SessionRulesCard } from "./SessionRulesCard";
import { KeywordHighlightCard } from "./KeywordHighlightCard";
import { SkillsCard } from "./SkillsCard";
import { productionSkillsApi } from "./skillsApi";
import { SyncCard } from "./SyncCard";
import { SyncBundleCard } from "./SyncBundleCard";
import { TranscriptCleanupCard } from "./TranscriptCleanupCard";
import { ShareCard } from "./ShareCard";
import { UpdateCard } from "./UpdateCard";
import { DiagnosticsCard } from "./DiagnosticsCard";
import { QuickCommandsEditor } from "../terminal/QuickCommandsEditor";
import { PortDiscoverySetting } from "../forward/ForwardSettingsPanel";
import { SyncScopeBadge } from "./SyncScopeBadge";
import { describeError } from "../../ui/errorText";
import { DESKTOP } from "../../ipc/env";
import {
  IconBell,
  IconCheckCircle,
  IconChevronDown,
  IconChevronLeft,
  IconChevronRight,
  IconCommand,
  IconInfo,
  IconLock,
  IconMonitor,
  IconRefresh,
  IconSearch,
  IconServer,
  IconSettings,
  IconSparkles,
  IconTerminal,
  IconXCircle,
  IconZap,
} from "../../ui/icons";
import { formatNumber, getLocale, t } from "../../i18n";

// Settings are split into a few groups shown one at a time; each group lists
// its sections in the navigation. Hidden groups stay mounted, so switching
// groups never drops an open form.
const SETTINGS_GROUPS: readonly { id: SettingsGroupId; label: string; icon: typeof IconMonitor }[] = [
  { id: "appearance", label: t("外观"), icon: IconMonitor },
  { id: "terminal", label: t("终端"), icon: IconTerminal },
  { id: "notifications", label: t("通知"), icon: IconBell },
  { id: "shortcuts", label: t("快捷键与快捷命令"), icon: IconCommand },
  { id: "ai", label: "AI", icon: IconSparkles },
  { id: "security", label: t("安全"), icon: IconLock },
  { id: "account", label: t("账号与数据"), icon: IconServer },
  { id: "app", label: t("关于"), icon: IconInfo },
];

const DEFAULT_GROUP: SettingsGroupId = "appearance";

function groupOfSection(id: string): SettingsGroupId {
  return SETTINGS_SECTIONS.find((section) => section.id === id)?.group ?? DEFAULT_GROUP;
}

const GROUP_KEY = "nexterm.settingsGroup.v1";

function loadGroup(): SettingsGroupId {
  try {
    const saved = localStorage.getItem(GROUP_KEY);
    return SETTINGS_GROUPS.some((group) => group.id === saved) ? (saved as SettingsGroupId) : DEFAULT_GROUP;
  } catch {
    return DEFAULT_GROUP;
  }
}

function saveGroup(group: SettingsGroupId): void {
  try {
    localStorage.setItem(GROUP_KEY, group);
  } catch {
    // The group choice is a convenience; failing to remember it is harmless.
  }
}

function jumpToSection(id: string): void {
  document.getElementById(id)?.scrollIntoView?.({ behavior: "smooth", block: "start" });
}

const NAV_OPEN_KEY = "nexterm.settingsNav.v1";

const CLOSE_ACTION_OPTIONS: { value: TerminalCloseAction; label: string }[] = [
  { value: "ask", label: t("每次询问") },
  { value: "kill", label: t("直接关闭") },
  { value: "detach", label: t("转到后台") },
];

const GPU_ACCELERATION_OPTIONS: { value: TerminalGpuAcceleration; label: string }[] = [
  { value: "auto", label: t("自动") },
  { value: "on", label: t("开启") },
  { value: "off", label: t("关闭") },
];

const DURABLE_HISTORY_OPTIONS_MIB = [4, 8, 16, 32, 64, 128, 256];

// Chinese counts large numbers in 万; other languages group the digits their own way. count chooses the plural form.
function formatScrollbackLines(lines: number): string {
  if (lines >= 10_000 && getLocale().startsWith("zh")) return t("{value} 万行", { value: lines / 10_000 });
  return t("{lines} 行", { lines: formatNumber(lines), count: lines });
}

const LINK_ACTIVATION_OPTIONS: { value: TerminalLinkActivation; label: string }[] = [
  { value: "modifier", label: t("Ctrl/Cmd+单击") },
  { value: "click", label: t("单击") },
];

const LINK_OPEN_MODE_OPTIONS: { value: TerminalLinkOpenMode; label: string }[] = [
  { value: "system", label: t("系统浏览器（默认）") },
  { value: "app", label: t("NexTerm 内") },
  { value: "ask", label: t("每次询问") },
];

const MULTILINE_PASTE_OPTIONS: { value: TerminalMultilinePasteConfirm; label: string }[] = [
  { value: "unbracketed", label: t("程序未开启括号粘贴时确认") },
  { value: "always", label: t("每次都确认") },
  { value: "never", label: t("从不确认") },
];

const CLOSE_ACTION_HINTS: Record<TerminalCloseAction, string> = {
  ask: t("关闭前询问转到后台还是结束进程。"),
  kill: t("直接结束进程。"),
  detach: t("进程转入后台，可在「后台会话」接管。"),
};

const GPU_ACCELERATION_HINTS: Record<TerminalGpuAcceleration, string> = {
  auto: t("检测到软件渲染（如虚拟机、远程桌面）时改用兼容渲染。"),
  on: t("软件渲染环境也尝试 WebGL，多次失效时回退。"),
  off: t("始终使用兼容渲染，不占用 GPU。"),
};

const OSC52_CONFIRM =
  t("确定要允许远程主机直接写入剪贴板吗？\n开启后将不再逐次询问，恶意主机可能改写剪贴板内容。请仅在可信的主机与网络中开启。");

function Subsection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="mt-4 border-t border-neutral-800/60 pt-3.5">
      <h3 className="mb-2.5 text-[11.5px] font-medium text-neutral-500">{title}</h3>
      <div className="flex flex-col gap-3.5">{children}</div>
    </div>
  );
}

const DEFAULT_MODE_OPTIONS: { value: TerminalDefaultMode; label: string }[] = [
  { value: "durable", label: t("持久终端（推荐）") },
  { value: "regular", label: t("临时终端") },
];

function loadNavOpen(): boolean {
  try {
    return localStorage.getItem(NAV_OPEN_KEY) !== "collapsed";
  } catch {
    return true;
  }
}

function saveNavOpen(open: boolean): void {
  try {
    localStorage.setItem(NAV_OPEN_KEY, open ? "open" : "collapsed");
  } catch {
  }
}

export function SettingsView({ active = true }: { active?: boolean }) {
  const inputPrefs = useInputPrefs();
  const showLinkOpenMode = !isMobileShell();
  const terminalAdvancedId = useId();
  const [terminalAdvancedOpen, setTerminalAdvancedOpen] = useState(false);
  const pushToast = useUi((s) => s.pushToast);
  const terminalOsc52 = useUi((s) => s.terminalOsc52);
  const ctrlVPaste = useCtrlVPaste();
  const setTerminalOsc52 = useUi((s) => s.setTerminalOsc52);
  const terminalPrefs = useTerminalPrefs();
  const multilinePasteSelectId = useId();
  const [terminalModeSaving, setTerminalModeSaving] = useState(false);
  const saveTerminalMode = async (value: TerminalDefaultMode) => {
    if (terminalModeSaving) return;
    setTerminalModeSaving(true);
    try {
      await setTerminalDefaultMode(value);
    } catch (error) {
      pushToast("error", t("保存默认终端模式失败：{value}", { value: describeError(error) }));
    } finally {
      setTerminalModeSaving(false);
    }
  };
  const scrollbackSelectId = useId();
  const durableHistorySelectId = useId();
  const [durableHistorySaving, setDurableHistorySaving] = useState(false);
  const durableHistoryAccount = terminalDefaultUsesAccount();
  const saveDurableHistory = async (value: number | null) => {
    if (durableHistorySaving) return;
    setDurableHistorySaving(true);
    try {
      await setTerminalDurableHistoryMiB(value);
    } catch (error) {
      pushToast("error", t("保存持久终端历史上限失败：{value}", { value: describeError(error) }));
    } finally {
      setDurableHistorySaving(false);
    }
  };
  const showCloseToTray = DESKTOP && isWindows();
  const [closeToTray, setCloseToTray] = useState(false);
  const [closeToTrayLoading, setCloseToTrayLoading] = useState(showCloseToTray);
  const [closeToTraySaving, setCloseToTraySaving] = useState(false);
  useEffect(() => {
    if (!showCloseToTray) return;
    let cancelled = false;
    desktopTrayApi
      .get()
      .then((dto) => {
        if (!cancelled) setCloseToTray(dto.enabled);
      })
      .catch((error) => {
        if (!cancelled) pushToast("error", describeError(error));
      })
      .finally(() => {
        if (!cancelled) setCloseToTrayLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [showCloseToTray, pushToast]);
  const changeCloseToTray = (enabled: boolean) => {
    if (closeToTraySaving) return;
    const previous = closeToTray;
    setCloseToTray(enabled);
    setCloseToTraySaving(true);
    desktopTrayApi.set(enabled).catch((e) => {
      setCloseToTray(previous);
      pushToast("error", describeError(e));
    }).finally(() => setCloseToTraySaving(false));
  };

  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{
    modelsOk: boolean;
    chatOk: boolean;
    modelsError?: string | null;
    chatError?: string | null;
    fatal?: string;
  } | null>(null);
  const connTestPanelId = useId();
  const [connTestOpen, setConnTestOpen] = useState(false);

  const [navOpen, setNavOpenState] = useState(loadNavOpen);
  const setNavOpen = (open: boolean) => {
    setNavOpenState(open);
    saveNavOpen(open);
  };
  const [group, setGroupState] = useState<SettingsGroupId>(() =>
    !DESKTOP && window.location.hash === "#settings-account" ? "account" : loadGroup(),
  );
  const groupSections = SETTINGS_SECTIONS.filter((section) => section.group === group);
  const [activeSection, setActiveSection] = useState<string>(groupSections[0]?.id ?? SETTINGS_SECTIONS[0].id);
  const openGroup = (next: SettingsGroupId) => {
    setGroupState(next);
    saveGroup(next);
    setActiveSection(SETTINGS_SECTIONS.find((section) => section.group === next)?.id ?? SETTINGS_SECTIONS[0].id);
    contentRef.current?.scrollTo?.({ top: 0 });
  };
  const jump = (id: string) => {
    const target = groupOfSection(id);
    if (target !== group) {
      setGroupState(target);
      saveGroup(target);
      // The section only gets a layout once its group is shown.
      requestAnimationFrame(() => jumpToSection(id));
    } else {
      jumpToSection(id);
    }
    setActiveSection(id);
  };

  // Search: results span all groups; picking one opens its group, scrolls to
  // the setting (or its section when the setting is not rendered, e.g. inside a
  // collapsed panel) and flashes it.
  const [searchQuery, setSearchQuery] = useState("");
  const searchInputRef = useRef<HTMLInputElement>(null);
  const flashTimer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(flashTimer.current), []);
  const searching = searchQuery.trim() !== "";
  const searchResults = useMemo(() => searchSettings(searchQuery, SETTINGS_SECTIONS), [searchQuery]);
  const [reveal, setReveal] = useState<{ panel: SettingsPanelId; n: number } | null>(null);
  const [focusRequest, setFocusRequest] = useState<{ result: SettingsSearchResult; n: number } | null>(null);
  const selectSearchResult = (result: SettingsSearchResult) => {
    setSearchQuery("");
    const target = groupOfSection(result.sectionId);
    if (target !== group) {
      setGroupState(target);
      saveGroup(target);
    }
    setActiveSection(result.sectionId);
    const n = Date.now() + Math.random();
    if (result.kind === "item" && result.panel) setReveal({ panel: result.panel, n });
    if (result.kind === "item" && result.panel === "terminal-advanced") setTerminalAdvancedOpen(true);
    setFocusRequest({ result, n });
  };
  // Runs after the group is shown and any collapsed panel holding the setting
  // has opened, which both need a frame of layout.
  useEffect(() => {
    if (!focusRequest) return;
    const { result } = focusRequest;
    const frame = requestAnimationFrame(() => {
      const root = contentRef.current;
      const el =
        (result.kind === "item"
          ? root?.querySelector<HTMLElement>(`[data-setting="${result.itemId}"]`)
          : null) ?? document.getElementById(result.sectionId);
      if (!el) return;
      el.scrollIntoView?.({ behavior: "smooth", block: "center" });
      if (!el.hasAttribute("tabindex")) el.setAttribute("tabindex", "-1");
      el.focus({ preventScroll: true });
      el.classList.add("nx-setting-flash");
      window.clearTimeout(flashTimer.current);
      flashTimer.current = window.setTimeout(() => el.classList.remove("nx-setting-flash"), 2000);
    });
    return () => cancelAnimationFrame(frame);
  }, [focusRequest]);
  const focusSearch = () => {
    // Expand without persisting: focusing search should not change the saved nav preference.
    setNavOpenState(true);
    requestAnimationFrame(() => searchInputRef.current?.focus());
  };
  // The active settings pane opens with the search box focused so typing
  // searches right away. Inactive (kept-mounted) panes must not steal focus,
  // and touch layouts skip it to avoid raising the on-screen keyboard.
  useEffect(() => {
    if (active && !isTouchShell()) searchInputRef.current?.focus();
  }, [active]);
  const onViewKeyDown = (e: React.KeyboardEvent) => {
    if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
    if (isEditableTarget(e.target)) return;
    e.preventDefault();
    focusSearch();
  };

  // Links from elsewhere in the app open the section's group, then scroll.
  const jumpRef = useRef(jump);
  jumpRef.current = jump;
  const revealRef = useRef(selectSearchResult);
  revealRef.current = selectSearchResult;
  useEffect(
    () =>
      onSettingsSection((id, itemId) => {
        const item = itemId ? SETTINGS_ITEMS.find((entry) => entry.id === itemId && entry.section === id) : undefined;
        if (!item) {
          jumpRef.current(id);
          return;
        }
        revealRef.current({
          kind: "item",
          key: `${item.section}:${item.id}`,
          sectionId: item.section,
          itemId: item.id,
          panel: item.panel,
          label: item.label,
          detail: "",
        });
      }),
    [],
  );

  useEffect(() => {
    if (DESKTOP || window.location.hash !== "#settings-account") return;
    // The initial group is already "account" for this link; only scroll.
    const frame = requestAnimationFrame(() => {
      setActiveSection("settings-account");
      jumpToSection("settings-account");
    });
    return () => cancelAnimationFrame(frame);
  }, []);

  // The TOC highlight follows the right-side content scroll: the current section is the last one whose top crossed the threshold inside the scroll container.
  // The optimistic highlight from click-to-jump and the scroll settle on the same section; jump and collapse behavior is unchanged.
  const contentRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = contentRef.current;
    if (!el) return;
    let frame: number | null = null;
    const update = () => {
      frame = null;
      const top = el.getBoundingClientRect().top;
      const visible = SETTINGS_SECTIONS.filter((section) => section.group === group);
      let current: string = visible[0]?.id ?? SETTINGS_SECTIONS[0].id;
      for (const s of visible) {
        const node = el.querySelector<HTMLElement>(`#${s.id}`);
        if (node && node.getBoundingClientRect().top - top <= 64) current = s.id;
      }
      // Short last sections can never reach the top; at the bottom, the last one is current.
      if (el.scrollHeight > el.clientHeight && el.scrollTop + el.clientHeight >= el.scrollHeight - 2) {
        current = visible[visible.length - 1]?.id ?? current;
      }
      setActiveSection((prev) => (prev === current ? prev : current));
    };
    const onScroll = () => {
      if (frame !== null) return;
      frame = requestAnimationFrame(update);
    };
    update();
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      el.removeEventListener("scroll", onScroll);
      if (frame !== null) cancelAnimationFrame(frame);
    };
  }, [group]);

  const test = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const r = await aiApi.testProvider();
      setTestResult({
        modelsOk: r.modelsOk,
        chatOk: r.chatOk,
        modelsError: r.modelsError,
        chatError: r.chatError,
      });
    } catch (e) {
      setTestResult({ modelsOk: false, chatOk: false, fatal: describeError(e) });
    } finally {
      setTesting(false);
    }
  };

  const testPassed = !!testResult && testResult.modelsOk && testResult.chatOk;
  const testDetail = testResult
    ? testResult.fatal
      ? t("调用失败：{fatal}", { fatal: testResult.fatal })
      : [
          t("模型列表：{value}", { value: testResult.modelsOk ? t("可用") : t("失败（{value}）", { value: testResult.modelsError ?? t("原因未查明") }) }),
          t("实际对话：{value}", { value: testResult.chatOk ? t("可用") : t("失败（{value}）", { value: testResult.chatError ?? t("原因未查明") }) }),
        ].join("\n")
    : "";

  return (
    <div className="nx-pane nx-settings-view h-full" onKeyDown={onViewKeyDown}>
      <div className="nx-toolbar">
        <IconSettings size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">{t("设置")}</span>
        <div className="nx-spacer" />
      </div>

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <nav
          aria-label={t("设置分区")}
          className={`z-10 flex shrink-0 flex-col md:overflow-y-auto ${
            navOpen ? "md:w-52" : "md:w-14"
          }`}
        >
          <div className={navOpen ? "" : "md:hidden"}>
            <SettingsSearch
              query={searchQuery}
              onQueryChange={setSearchQuery}
              results={searchResults}
              onSelect={selectSearchResult}
              inputRef={searchInputRef}
            />
          </div>
          <div
            hidden={searching}
            className="flex gap-1.5 overflow-x-auto px-3 py-2 sm:px-5 md:hidden"
            data-testid="settings-nav-chips"
          >
            {SETTINGS_GROUPS.map((g) => (
              <button
                key={g.id}
                type="button"
                className={`nx-chip shrink-0 ${g.id === group ? "is-active" : ""}`}
                aria-pressed={g.id === group}
                onClick={() => openGroup(g.id)}
              >
                {g.label}
              </button>
            ))}
          </div>
          <div
            className={`hidden md:flex md:flex-col md:gap-0.5 md:p-2 ${searching ? "md:!hidden" : ""}`}
            data-testid="settings-nav-toc"
          >
            <button
              type="button"
              className="nx-icon-btn nx-icon-btn-sm self-end"
              aria-label={navOpen ? t("收起设置目录") : t("展开设置目录")}
              aria-expanded={navOpen}
              title={navOpen ? t("收起目录") : t("展开目录")}
              onClick={() => setNavOpen(!navOpen)}
            >
              {navOpen ? <IconChevronLeft size={13} /> : <IconChevronRight size={13} />}
            </button>
            {navOpen
              ? SETTINGS_GROUPS.map((g) => (
                  <div key={g.id} className="flex flex-col">
                    <button
                      type="button"
                      className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] font-medium whitespace-nowrap transition-colors ${
                        g.id === group ? "text-neutral-100" : "text-neutral-400 hover:bg-neutral-800/45 hover:text-neutral-200"
                      }`}
                      aria-expanded={g.id === group}
                      onClick={() => openGroup(g.id)}
                    >
                      <g.icon size={13} />
                      {g.label}
                    </button>
                    {g.id === group &&
                      groupSections.length > 1 &&
                      groupSections.map((section) => (
                        <button
                          key={section.id}
                          type="button"
                          className={`w-full rounded-md py-1 pl-7 pr-2 text-left text-[12.5px] whitespace-nowrap transition-colors ${
                            section.id === activeSection
                              ? "bg-neutral-800/70 text-neutral-100"
                              : "text-neutral-400 hover:bg-neutral-800/45 hover:text-neutral-200"
                          }`}
                          aria-current={section.id === activeSection ? "true" : undefined}
                          onClick={() => jump(section.id)}
                        >
                          {section.label}
                        </button>
                      ))}
                  </div>
                ))
              : [
                  <button
                    key="search"
                    type="button"
                    className="nx-icon-btn self-center"
                    title={t("搜索设置")}
                    aria-label={t("搜索设置")}
                    onClick={focusSearch}
                  >
                    <IconSearch size={14} />
                  </button>,
                  ...SETTINGS_GROUPS.map((g) => (
                  <button
                    key={g.id}
                    type="button"
                    className={`nx-icon-btn self-center${g.id === group ? " is-active" : ""}`}
                    title={g.label}
                    aria-label={g.label}
                    aria-current={g.id === group ? "true" : undefined}
                    onClick={() => openGroup(g.id)}
                  >
                    <g.icon size={14} />
                  </button>
                  )),
                ]}
          </div>
        </nav>

        <div className="min-w-0 flex-1 overflow-y-auto" ref={contentRef}>
          <div className="mx-auto flex w-full max-w-[720px] flex-col gap-3 p-3 sm:gap-4 sm:p-5">
        <div className="flex flex-col gap-4" hidden={group !== "appearance"} data-settings-group="appearance">
        <div id="settings-appearance" className="scroll-mt-12">
          <AppearanceCard reveal={reveal} />
        </div>
        </div>

        <div className="flex flex-col gap-4" hidden={group !== "terminal"} data-settings-group="terminal">
        <section id="settings-terminal" className="nx-card scroll-mt-12">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconTerminal size={15} className="text-neutral-400" />
            <span className="nx-card-title">{t("终端")}</span>
          </div>

          <Subsection title={t("新建终端")}>
            <div>
              <div className="nx-setting-label mb-1.5 flex items-center gap-2" data-setting="default-terminal-mode">
                {t("默认终端模式")}
                <SyncScopeBadge scope={durableHistoryAccount ? "sync" : "local"} />
              </div>
              <div className="nx-segment" role="group" aria-label={t("默认终端模式")} aria-busy={terminalModeSaving}>
                {DEFAULT_MODE_OPTIONS.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    className={`nx-segment-item ${terminalPrefs.defaultMode === option.value ? "is-active" : ""}`}
                    aria-pressed={terminalPrefs.defaultMode === option.value}
                    disabled={terminalModeSaving}
                    onClick={() => void saveTerminalMode(option.value)}
                  >
                    {option.label}
                  </button>
                ))}
              </div>
              <p className="nx-hint mt-1.5">
                {t("断线后继续运行，可在「后台会话」接管；持久终端组件就绪前新建的为临时终端。仅影响新建终端，资产设置优先。")}
              </p>
            </div>
            <LocalHelperSettings />
          </Subsection>

          <Subsection title={t("关闭")}>
            <div>
              <div className="nx-setting-label mb-1.5 flex items-center gap-2" data-setting="close-action">
                {t("关闭运行中的终端标签页时")}
                <SyncScopeBadge scope="sync" />
              </div>
              <div className="nx-segment" role="group" aria-label={t("关闭运行中的终端标签页时")}>
                {CLOSE_ACTION_OPTIONS.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    className={`nx-segment-item ${terminalPrefs.closeAction === option.value ? "is-active" : ""}`}
                    aria-pressed={terminalPrefs.closeAction === option.value}
                    onClick={() => setTerminalCloseAction(option.value)}
                  >
                    {option.label}
                  </button>
                ))}
              </div>
              <p className="nx-hint mt-1.5">{CLOSE_ACTION_HINTS[terminalPrefs.closeAction]}</p>
            </div>
            {showCloseToTray && (
              <label className="flex cursor-pointer items-start gap-3">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 text-[12.5px] text-neutral-200" data-setting="close-to-tray">
                    {t("关闭时保留后台运行（最小化到托盘）")}
                    <SyncScopeBadge scope="local" />
                  </div>
                  <p className="nx-hint mt-0.5">{t("关闭窗口后留在托盘，会话保持连接；右键托盘图标可退出。")}</p>
                </div>
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 shrink-0"
                  checked={closeToTray}
                  disabled={closeToTrayLoading || closeToTraySaving}
                  onChange={(e) => changeCloseToTray(e.target.checked)}
                />
              </label>
            )}
          </Subsection>

          <Subsection title={t("剪贴板与粘贴")}>
            <label className="flex cursor-pointer items-start gap-3">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 text-[12.5px] text-neutral-200" data-setting="osc52">
                  {t("允许远程主机写入剪贴板（OSC 52）")}
                  <SyncScopeBadge scope="local" />
                </div>
                <p className="nx-hint mt-0.5">{t("开启后不再逐次询问，请仅对可信主机开启。")}</p>
              </div>
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 shrink-0"
                checked={terminalOsc52}
                onChange={(e) => {
                  const enable = e.target.checked;
                  if (!enable) {
                    setTerminalOsc52(false);
                    return;
                  }
                  void ask(OSC52_CONFIRM, {
                    title: t("开启剪贴板写入"),
                    kind: "warning",
                  }).then((ok) => {
                    if (ok) setTerminalOsc52(true);
                  });
                }}
              />
            </label>
            {isWindows() && (
              <label className="flex cursor-pointer items-start gap-3">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 text-[12.5px] text-neutral-200" data-setting="ctrl-v-paste">
                    {t("Ctrl+V 粘贴")}
                    <SyncScopeBadge scope="local" />
                  </div>
                  <p className="nx-hint mt-0.5">{t("关闭后 Ctrl+V 发送 ^V；Ctrl+Shift+V 始终可粘贴。")}</p>
                </div>
                <input
                  type="checkbox"
                  aria-label={t("Ctrl+V 粘贴")}
                  className="mt-0.5 h-4 w-4 shrink-0"
                  checked={ctrlVPaste}
                  onChange={(e) => setCtrlVPaste(e.target.checked)}
                />
              </label>
            )}
            <div>
              <label className="nx-setting-label mb-1.5 flex items-center gap-2" htmlFor={multilinePasteSelectId} data-setting="terminal-confirm-multiline-paste">
                {t("多行粘贴确认")}
                <SyncScopeBadge scope="sync" />
              </label>
              <Select
                id={multilinePasteSelectId}
                ariaLabel={t("多行粘贴确认")}
                className="w-full sm:w-auto"
                value={terminalPrefs.confirmMultilinePaste}
                options={MULTILINE_PASTE_OPTIONS}
                onChange={(value) => setTerminalConfirmMultilinePaste(normalizeMultilinePasteConfirm(value))}
              />
              <p className="nx-hint mt-1.5">{t("防止多行内容被 shell 逐行执行。")}</p>
            </div>
            <label className="flex cursor-pointer items-start gap-3">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 text-[12.5px] text-neutral-200" data-setting="copy-on-select">
                  {t("选中自动复制")}
                  <SyncScopeBadge scope="sync" />
                </div>
                <p className="nx-hint mt-0.5">{t("在终端选中文字后立即复制到剪贴板。")}</p>
              </div>
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 shrink-0"
                aria-label={t("选中自动复制")}
                checked={inputPrefs.selectionAutoCopy}
                onChange={(event) => setSelectionAutoCopy(event.target.checked)}
              />
            </label>
          </Subsection>

          <Subsection title={t("链接")}>
            <div>
              <div className="nx-setting-label mb-1.5 flex items-center gap-2" data-setting="terminal-link-activation">
                {t("链接打开方式")}
                <SyncScopeBadge scope="sync" />
              </div>
              <div className="nx-segment" role="group" aria-label={t("链接打开方式")}>
                {LINK_ACTIVATION_OPTIONS.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    className={`nx-segment-item ${terminalPrefs.linkActivation === option.value ? "is-active" : ""}`}
                    aria-pressed={terminalPrefs.linkActivation === option.value}
                    onClick={() => setTerminalLinkActivation(option.value)}
                  >
                    {option.label}
                  </button>
                ))}
              </div>
              <p className="nx-hint mt-1.5">{t("终端程序捕获鼠标时按住 Shift，或右键选择「打开链接」。")}</p>
            </div>
            {showLinkOpenMode && (
              <div>
                <div className="nx-setting-label mb-1.5 flex items-center gap-2" data-setting="terminal-link-open-mode">
                  {t("链接打开位置")}
                  <SyncScopeBadge scope="sync" />
                </div>
                <div className="nx-segment" role="group" aria-label={t("链接打开位置")}>
                  {LINK_OPEN_MODE_OPTIONS.map((option) => (
                    <button
                      key={option.value}
                      type="button"
                      className={`nx-segment-item ${terminalPrefs.linkOpenMode === option.value ? "is-active" : ""}`}
                      aria-pressed={terminalPrefs.linkOpenMode === option.value}
                      onClick={() => setTerminalLinkOpenMode(option.value)}
                    >
                      {option.label}
                    </button>
                  ))}
                </div>
                <p className="nx-hint mt-1.5">{t("仅影响 http/https 链接；无法嵌入时改用系统浏览器。")}</p>
              </div>
            )}
          </Subsection>

          <Subsection title={t("文件传输")}>
            <label className="flex cursor-pointer items-start gap-3">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 text-[12.5px] text-neutral-200" data-setting="terminal-trzsz">
                  {t("trzsz 文件传输")}
                  <SyncScopeBadge scope="local" />
                </div>
                <p className="nx-hint mt-0.5">{t("在远端运行 trz 上传、tsz 下载，可穿过 tmux 和跳板机。每次传输都需在本机确认，回放的历史输出不会触发。")}</p>
              </div>
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 shrink-0"
                aria-label={t("trzsz 文件传输")}
                checked={terminalPrefs.trzsz}
                onChange={(event) => setTerminalTrzsz(event.target.checked)}
              />
            </label>
          </Subsection>

          <Subsection title={t("端口转发")}>
            <PortDiscoverySetting />
          </Subsection>

          <div className="mt-4 border-t border-neutral-800/60 pt-3.5">
            <button
              type="button"
              className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
              aria-expanded={terminalAdvancedOpen}
              aria-controls={terminalAdvancedId}
              onClick={() => setTerminalAdvancedOpen((open) => !open)}
            >
              {terminalAdvancedOpen ? <IconChevronDown size={12} className="shrink-0" /> : <IconChevronRight size={12} className="shrink-0" />}
              {t("高级设置")}
            </button>
            {terminalAdvancedOpen && (
              <div id={terminalAdvancedId} className="mt-3 flex flex-col gap-3.5">
                <div>
                  <div className="nx-setting-label mb-1.5 flex items-center gap-2" data-setting="terminal-gpu-acceleration">
                    {t("终端 GPU 加速")}
                    <SyncScopeBadge scope="sync" />
                  </div>
                  <div className="nx-segment" role="group" aria-label={t("终端 GPU 加速")}>
                    {GPU_ACCELERATION_OPTIONS.map((option) => (
                      <button
                        key={option.value}
                        type="button"
                        className={`nx-segment-item ${terminalPrefs.gpuAcceleration === option.value ? "is-active" : ""}`}
                        aria-pressed={terminalPrefs.gpuAcceleration === option.value}
                        onClick={() => setTerminalGpuAcceleration(option.value)}
                      >
                        {option.label}
                      </button>
                    ))}
                  </div>
                  <p className="nx-hint mt-1.5">{GPU_ACCELERATION_HINTS[terminalPrefs.gpuAcceleration]}</p>
                </div>
                <div>
                  <label className="nx-setting-label mb-1.5 flex items-center gap-2" htmlFor={scrollbackSelectId} data-setting="terminal-scrollback">
                    {t("终端回滚行数")}
                    <SyncScopeBadge scope="local" />
                  </label>
                  <Select
                    id={scrollbackSelectId}
                    ariaLabel={t("终端回滚行数")}
                    className="w-full sm:w-auto"
                    value={terminalPrefs.scrollback.toString()}
                    options={TERMINAL_SCROLLBACK_OPTIONS.map((lines) => ({
                      value: lines.toString(),
                      label: `${formatScrollbackLines(lines)}${lines === 10_000 ? t("（默认）") : ""}`,
                    }))}
                    onChange={(value) => setTerminalScrollback(normalizeTerminalScrollback(Number(value)))}
                  />
                  <p className="nx-hint mt-1.5">{t("调小后丢弃最早的行。")}</p>
                </div>
                {durableHistoryAccount && (
                  <div>
                    <label className="nx-setting-label mb-1.5 flex items-center gap-2" htmlFor={durableHistorySelectId} data-setting="terminal-durable-history">
                      {t("持久终端历史上限")}
                      <SyncScopeBadge scope="sync" />
                    </label>
                    <div aria-busy={durableHistorySaving}>
                      <Select
                        id={durableHistorySelectId}
                        ariaLabel={t("持久终端历史上限")}
                        className="w-full sm:w-auto"
                        value={terminalPrefs.durableHistoryMiB?.toString() ?? ""}
                        disabled={durableHistorySaving}
                        options={[
                          { value: "", label: t("默认（{DEFAULT_DURABLE_HISTORY_MIB} MB）", { DEFAULT_DURABLE_HISTORY_MIB }) },
                          ...(terminalPrefs.durableHistoryMiB !== null && !DURABLE_HISTORY_OPTIONS_MIB.includes(terminalPrefs.durableHistoryMiB)
                            ? [{ value: terminalPrefs.durableHistoryMiB.toString(), label: `${terminalPrefs.durableHistoryMiB} MB` }]
                            : []),
                          ...DURABLE_HISTORY_OPTIONS_MIB.map((mib) => ({ value: mib.toString(), label: `${mib} MB` })),
                        ]}
                        onChange={(value) => void saveDurableHistory(value === "" ? null : Number(value))}
                      />
                    </div>
                    <p className="nx-hint mt-1.5">{t("超出后丢弃最早的输出，只影响新开的持久终端。")}</p>
                  </div>
                )}
              </div>
            )}
          </div>
        </section>

        <div id="settings-state-rules" className="scroll-mt-12">
          <SessionRulesCard />
        </div>

        <KeywordHighlightCard />
        </div>

        <div className="flex flex-col gap-4" hidden={group !== "notifications"} data-settings-group="notifications">
        <section id="settings-notifications" className="nx-card scroll-mt-12">
          <div className="mb-3 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconBell size={15} className="text-neutral-400" />
            <span className="nx-card-title">{t("通知")}</span>
            <SyncScopeBadge scope="local" />
          </div>
          <NotificationSettings />
        </section>
        </div>

        <div className="flex flex-col gap-4" hidden={group !== "shortcuts"} data-settings-group="shortcuts">
        <div id="settings-shortcuts" className="scroll-mt-12">
          <ShortcutsCard />
        </div>

        <section id="settings-quick-commands" className="nx-card scroll-mt-12">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconCommand size={15} className="text-neutral-400" />
            <span className="nx-card-title" data-setting="quick-commands">{t("快捷命令")}</span>
            <SyncScopeBadge scope="sync" />
          </div>
          <p className="nx-hint mb-2">{t("在终端按键条的「命令」中发送。")}</p>
          <QuickCommandsEditor />
        </section>
        </div>

        <div className="flex flex-col gap-4" hidden={group !== "app"} data-settings-group="app">
        {DESKTOP && (
          <div id="settings-update" className="scroll-mt-12">
            <UpdateCard />
          </div>
        )}

        <div id="settings-diagnostics" className="scroll-mt-12">
          <DiagnosticsCard />
        </div>
        </div>
        <div className="flex flex-col gap-4" hidden={group !== "ai"} data-settings-group="ai">
        <section id="settings-ai-model" className="nx-card scroll-mt-12">
          <div className="mb-1 flex flex-wrap items-center gap-2">
            <IconSparkles size={15} className="text-blue-300" />
            <span className="nx-card-title">{t("AI 模型")}</span>
            <span className="nx-badge">{t("OpenAI 兼容协议 · 自带密钥")}</span>
          </div>
          <p className="nx-hint mb-3.5">
            {t("配置 OpenAI 兼容端点，支持云端模型及本地 Ollama、LM Studio、vLLM。密钥加密保存在当前实例；启用档案同步并解锁凭据库后，才会加密同步。")}
          </p>

          <ModelManager />

          <div className="mt-4 border-t border-neutral-800/60 pt-3.5">
            <button
              type="button"
              className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
              aria-expanded={connTestOpen}
              aria-controls={connTestPanelId}
              onClick={() => setConnTestOpen((v) => !v)}
            >
              {connTestOpen ? (
                <IconChevronDown size={12} className="shrink-0" />
              ) : (
                <IconChevronRight size={12} className="shrink-0" />
              )}
              {t("连通性测试")}
            </button>
            {connTestOpen && (
              <div id={connTestPanelId}>
                <div className="mt-2 flex flex-wrap items-center gap-2">
                  <button
                    className="nx-btn nx-btn-outline h-auto min-h-7 shrink py-1 text-left whitespace-normal"
                    disabled={testing}
                    onClick={() => void test()}
                  >
                    {testing ? (
                      <IconRefresh size={13} className="shrink-0 animate-spin" />
                    ) : (
                      <IconZap size={13} className="shrink-0" />
                    )}
                    {testing ? t("正在测试…") : t("测试当前模型连通性")}
                  </button>
                  <span className="nx-hint">
                    {t("仅可测试已生效的当前模型；未保存的修改请通过上方的「刷新模型列表」验证。")}
                  </span>
                </div>

                {testResult && (
                  <div
                    className={`mt-3.5 nx-alert ${
                      testPassed
                        ? "nx-alert-success"
                        : "nx-alert-danger"
                    }`}
                  >
                    <div className="mb-1 flex items-center gap-1.5 font-semibold">
                      {testPassed ? <IconCheckCircle size={13} /> : <IconXCircle size={13} />}
                      {t("测试结果")}
                    </div>
                    <pre className="whitespace-pre-wrap font-mono text-[11px]">{testDetail}</pre>
                  </div>
                )}
              </div>
            )}
          </div>
        </section>

        <PrivacyCard />

        <AiRulesCard />

        <div id="settings-ai-skills" className="scroll-mt-12">
          <SkillsCard api={productionSkillsApi} />
        </div>

        <div id="settings-memory" className="scroll-mt-12">
          <MemoryCard />
        </div>
        </div>
        <div className="flex flex-col gap-4" hidden={group !== "security"} data-settings-group="security">
        <VaultCard />

        <AppLockCard />

        <div id="settings-known-hosts" className="scroll-mt-12">
          <KnownHostsCard />
        </div>

        <ProductionSettingsPanel />
        </div>
        <div className="flex flex-col gap-4" hidden={group !== "account"} data-settings-group="account">
        <div id="settings-account" className="flex scroll-mt-12 flex-col gap-4">
          <SyncCard />
          <LocalHelperExposureSettings />
        </div>

        <div id="settings-history" className="scroll-mt-12">
          <TranscriptCleanupCard />
        </div>

        <div id="settings-bundle" className="scroll-mt-12">
          <SyncBundleCard />
        </div>

        <div id="settings-share" className="scroll-mt-12">
          <ShareCard />
        </div>
        </div>
          </div>
        </div>
      </div>
    </div>
  );
}
