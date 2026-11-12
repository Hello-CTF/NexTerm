// Declarative search index for the settings view. Section metadata is keyed by
// SettingsSectionId and SETTINGS_SECTIONS is checked against that union, so a
// section without an entry here, or with an unknown id, is a type error.
// Individual settings are listed in SETTINGS_ITEMS; an item is scrolled to and
// highlighted through the element carrying data-setting="<item id>", and falls
// back to its section when that element is not rendered.

import { isMac, isWindows } from "../../app/platform";
import { isMobileShell } from "../../app/shell/shellMode";
import { terminalDefaultUsesAccount } from "../../app/preferences";
import { DESKTOP } from "../../ipc/env";
import {
  IconActivity,
  IconAlert,
  IconArchive,
  IconBell,
  IconBot,
  IconCommand,
  IconDownload,
  IconFlag,
  IconGlobe,
  IconHistory,
  IconLayers,
  IconLock,
  IconMonitor,
  IconServer,
  IconShield,
  IconShieldCheck,
  IconSparkles,
  IconStar,
  IconTerminal,
} from "../../ui/icons";
import { t } from "../../i18n";

export type SettingsSectionId =
  | "settings-appearance"
  | "settings-terminal"
  | "settings-state-rules"
  | "settings-keyword-highlight"
  | "settings-layout-templates"
  | "settings-notifications"
  | "settings-shortcuts"
  | "settings-quick-commands"
  | "settings-update"
  | "settings-diagnostics"
  | "settings-ai-model"
  | "settings-ai-privacy"
  | "settings-ai-rules"
  | "settings-ai-skills"
  | "settings-memory"
  | "settings-vault"
  | "settings-app-lock"
  | "settings-known-hosts"
  | "settings-production"
  | "settings-account"
  | "settings-history"
  | "settings-bundle"
  | "settings-share";

export type SettingsGroupId = "appearance" | "terminal" | "notifications" | "shortcuts" | "ai" | "security" | "account" | "app";

export type SettingsSectionDef = { id: SettingsSectionId; label: string; icon: typeof IconMonitor; group: SettingsGroupId };

// The settings sections in navigation order. Kept here rather than in the settings view so the jump palette can
// search and link to them without loading the view.
const ALL_SETTINGS_SECTIONS = [
  { id: "settings-appearance", label: t("外观"), icon: IconMonitor, group: "appearance" },
  { id: "settings-terminal", label: t("终端"), icon: IconTerminal, group: "terminal" },
  { id: "settings-state-rules", label: t("状态规则"), icon: IconFlag, group: "terminal" },
  { id: "settings-keyword-highlight", label: t("关键词高亮"), icon: IconStar, group: "terminal" },
  { id: "settings-layout-templates", label: t("布局模板"), icon: IconLayers, group: "terminal" },
  { id: "settings-notifications", label: t("通知"), icon: IconBell, group: "notifications" },
  { id: "settings-shortcuts", label: t("快捷键"), icon: IconCommand, group: "shortcuts" },
  { id: "settings-quick-commands", label: t("快捷命令"), icon: IconCommand, group: "shortcuts" },
  { id: "settings-update", label: t("软件更新"), icon: IconDownload, group: "app" },
  { id: "settings-diagnostics", label: t("诊断与调试"), icon: IconActivity, group: "app" },
  { id: "settings-ai-model", label: t("模型"), icon: IconSparkles, group: "ai" },
  { id: "settings-ai-privacy", label: t("AI 信息脱敏"), icon: IconShield, group: "ai" },
  { id: "settings-ai-rules", label: t("AI 拦截规则"), icon: IconShield, group: "ai" },
  { id: "settings-ai-skills", label: t("技能"), icon: IconLayers, group: "ai" },
  { id: "settings-memory", label: t("记忆"), icon: IconBot, group: "ai" },
  { id: "settings-vault", label: t("凭据保护"), icon: IconLock, group: "security" },
  { id: "settings-app-lock", label: t("锁定 NexTerm"), icon: IconLock, group: "security" },
  { id: "settings-known-hosts", label: t("已知主机"), icon: IconShieldCheck, group: "security" },
  { id: "settings-production", label: t("生产安全"), icon: IconAlert, group: "security" },
  { id: "settings-account", label: t("账号与同步"), icon: IconServer, group: "account" },
  { id: "settings-history", label: t("历史会话"), icon: IconHistory, group: "account" },
  { id: "settings-bundle", label: t("资产包"), icon: IconArchive, group: "account" },
  { id: "settings-share", label: t("分享"), icon: IconGlobe, group: "account" },
] as const satisfies readonly SettingsSectionDef[];

export const SETTINGS_SECTIONS: readonly SettingsSectionDef[] = ALL_SETTINGS_SECTIONS.filter((section) => {
  if (section.id === "settings-update") return DESKTOP;
  return true;
});

export interface SettingsSearchMeta {
  description: string;
  keywords: readonly string[];
}

export const SECTION_SEARCH: Record<SettingsSectionId, SettingsSearchMeta> = {
  "settings-appearance": {
    description: t("界面主题与字号、终端主题、字体与对比度"),
    keywords: ["appearance", "theme", "dark", "light", "font", t("主题"), t("深色"), t("浅色"), t("字体"), t("字号"), t("外观")],
  },
  "settings-terminal": {
    description: t("新建终端、关闭行为、剪贴板与粘贴、链接、端口发现、GPU 加速"),
    keywords: ["terminal", "durable", "persistent", "osc52", "clipboard", "paste", "tray", "link", "port", t("持久"), t("剪贴板"), t("粘贴"), t("托盘"), t("后台"), t("链接"), t("端口")],
  },
  "settings-state-rules": {
    description: t("按屏幕内容纠正会话状态（需要处理、运行中、空闲）的规则"),
    keywords: ["state", "rule", "needs attention", "attention", "badge", "regex", "pattern", t("状态"), t("规则"), t("需要处理"), t("徽章"), t("误报"), t("正则"), t("标记"), "test", t("测试")],
  },
  "settings-keyword-highlight": {
    description: t("按关键词或正则表达式为终端输出着色"),
    keywords: ["highlight", "keyword", "color", "regex", "error", "warning", "ip", "uuid", t("高亮"), t("关键词"), t("颜色"), t("正则"), t("错误"), t("警告")],
  },
  "settings-notifications": {
    description: t("会话需要处理时在本机弹出系统通知"),
    keywords: ["notification", "notify", "bell", "inbox", t("通知"), t("系统通知"), t("响铃"), t("铃铛"), t("收件箱")],
  },
  "settings-layout-templates": {
    description: t("保存工作区分屏布局，按模板打开主机终端并运行启动片段"),
    keywords: ["layout", "template", "arrangement", "split", "pane", "workspace", t("布局"), t("模板"), t("分屏"), t("窗格"), t("工作区"), t("启动片段")],
  },
  "settings-quick-commands": {
    description: t("在终端按键条中发送的常用命令"),
    keywords: ["quick command", "snippet", "macro", t("命令"), t("常用命令"), t("片段"), t("按键条"), t("快捷命令")],
  },
  "settings-shortcuts": {
    description: t("自定义快捷键"),
    keywords: ["shortcut", "keybinding", "hotkey", "keyboard", "key", "split", "pane", "close pane", t("快捷键"), t("键盘"), t("按键"), t("热键"), t("分屏"), t("窗格")],
  },
  "settings-update": {
    description: t("检查并安装应用更新"),
    keywords: ["update", "upgrade", "version", "download", "about", t("更新"), t("升级"), t("版本"), t("关于")],
  },
  "settings-diagnostics": {
    description: t("调试日志与抓包文件"),
    keywords: ["diagnostics", "debug", "log", "capture", "trace", t("诊断"), t("调试"), t("日志"), t("抓包")],
  },
  "settings-ai-model": {
    description: t("AI 模型档案、密钥与连通性测试"),
    keywords: ["ai", "model", "api key", "openai", "ollama", "llm", t("模型"), t("密钥"), t("大模型"), t("连通性")],
  },
  "settings-ai-privacy": {
    description: t("本地模型检测敏感信息、稳定占位符与回复还原，下载或删除本地模型"),
    keywords: ["privacy", "guard", "astrlink", "int8", "fp32", "fp16", t("脱敏"), t("隐私"), t("敏感"), t("还原"), t("模型下载"), t("删除模型"), t("磁盘占用")],
  },
  "settings-ai-rules": {
    description: t("AI 操作命中后需确认或拒绝的危险命令规则"),
    keywords: ["rule", "danger", "block", "permission", "rm", t("拦截"), t("规则"), t("危险"), t("权限"), t("确认")],
  },
  "settings-ai-skills": {
    description: t("管理 AI 技能"),
    keywords: ["skill", "skills", "prompt", t("技能")],
  },
  "settings-memory": {
    description: t("AI 长期记忆：引用记忆与记忆工具"),
    keywords: ["memory", "remember", t("记忆"), t("长期记忆")],
  },
  "settings-vault": {
    description: t("用密码保护凭据，闲置自动锁定"),
    keywords: ["credential", "vault", "password", "lock", "encrypt", t("凭据"), t("密码"), t("加密"), t("锁定"), t("保险库"), t("安全")],
  },
  "settings-app-lock": {
    description: t("隐藏整个界面直到输入密码，闲置自动锁定"),
    keywords: ["lock", "lock screen", "idle", "away", "privacy", t("锁定"), t("锁屏"), t("离开"), t("闲置"), t("隐私")],
  },
  "settings-known-hosts": {
    description: t("已信任的 SSH 主机指纹"),
    keywords: ["known hosts", "fingerprint", "ssh", "trust", "host key", t("指纹"), t("信任"), t("主机")],
  },
  "settings-production": {
    description: t("生产环境主机的额外保护"),
    keywords: ["production", "prod", "safety", "protect", t("生产"), t("保护"), t("确认")],
  },
  "settings-account": {
    description: t("账号、用户管理、多端同步与本机终端开放"),
    keywords: ["account", "sync", "server", "user", "mfa", "login", "proxy", "expose", t("账号"), t("同步"), t("用户"), t("登录"), t("两步验证"), t("代理"), t("服务器"), t("服务端"), t("开放")],
  },
  "settings-history": {
    description: t("终端历史的自动清理、手动清理与占用空间"),
    keywords: ["history", "transcript", "cleanup", "retention", "purge", "star", t("历史"), t("会话"), t("清理"), t("保留"), t("星标"), t("缓存"), t("空间")],
  },
  "settings-bundle": {
    description: t("资产包导入与导出"),
    keywords: ["bundle", "import", "export", "backup", "file", t("资产"), t("导入"), t("导出"), t("备份")],
  },
  "settings-share": {
    description: t("向他人分享主机与会话"),
    keywords: ["share", "link", "invite", t("分享"), t("链接"), t("邀请")],
  },
};

export interface SettingsItem {
  id: string;
  section: SettingsSectionId;
  label: string;
  description?: string;
  keywords?: readonly string[];
  /** The collapsible panel that must be open for the setting to render. */
  panel?: SettingsPanelId;
  /** Hidden from search when it returns false (the setting does not render on this platform). */
  available?: () => boolean;
}

export type SettingsPanelId = "appearance-advanced" | "terminal-advanced";

// Some settings render only in runtime states that are known after an async load (for example, while an account is
// logged in). Their components report whether they render; search lists such an item only after it reported visible.
const runtimeVisible = new Map<string, boolean>();

export function reportSettingVisible(itemId: string, visible: boolean): void {
  runtimeVisible.set(itemId, visible);
}

function reportedVisible(itemId: string): boolean {
  return runtimeVisible.get(itemId) ?? false;
}

export const SETTINGS_ITEMS: readonly SettingsItem[] = [
  { id: "ui-language", section: "settings-appearance", label: t("界面语言"), description: t("简体中文、繁體中文、English、日本語等"), keywords: ["language", "locale", "i18n", t("语言"), t("語言"), "English", t("日本語"), "한국어", "Deutsch", "Français", "Español", "Русский"] },
  { id: "ui-theme", section: "settings-appearance", label: t("界面主题"), description: t("跟随系统、浅色或深色"), keywords: ["theme", "dark", "light", t("主题"), t("深色"), t("浅色")] },
  { id: "ui-accent", section: "settings-appearance", label: t("强调色"), keywords: ["accent", "color", t("颜色"), t("主题色")] },
  { id: "ui-font-size", section: "settings-appearance", label: t("界面字号"), description: t("界面文字大小，可自定义"), keywords: ["font size", "scale", "zoom", t("字体"), t("字号"), t("缩放"), t("倍率"), t("文字大小")] },
  { id: "terminal-theme", section: "settings-appearance", label: t("终端与编辑器主题"), keywords: ["theme", "color scheme", t("配色"), t("主题")] },
  { id: "terminal-scheme", section: "settings-appearance", label: t("终端配色"), description: t("深色和浅色终端各选一套配色，可导入 iTerm2、Windows Terminal、Ghostty 配色"), keywords: ["color scheme", "palette", "itermcolors", "windows terminal", "ghostty", "kitty", "catppuccin", "solarized", "dracula", "nord", "gruvbox", t("配色"), t("导入")] },
  { id: "terminal-font-size", section: "settings-appearance", label: t("终端字号"), keywords: ["font size", t("字体"), t("字号")] },
  { id: "terminal-font-family", section: "settings-appearance", label: t("终端字体"), keywords: ["font", "family", "cascadia", t("字体")] },
  { id: "terminal-contrast", section: "settings-appearance", label: t("终端对比度"), keywords: ["contrast", t("对比度")], panel: "appearance-advanced" },
  { id: "default-terminal-mode", section: "settings-terminal", label: t("默认终端模式"), description: t("持久终端或临时终端"), keywords: ["durable", "persistent", "detach", t("持久"), "local", t("本机"), t("本地终端"), t("本机终端模式"), t("持久组件"), t("持久终端组件")] },
  { id: "close-action", section: "settings-terminal", label: t("关闭运行中的终端标签页时"), keywords: ["close", "kill", "detach", t("关闭"), t("后台")] },
  { id: "close-to-tray", section: "settings-terminal", label: t("关闭时保留后台运行"), description: t("关闭窗口后最小化到系统托盘（仅 Windows 桌面版）"), keywords: ["tray", "minimize", "background", t("托盘"), t("最小化"), t("后台运行"), t("退出")], available: () => DESKTOP && isWindows() },
  { id: "osc52", section: "settings-terminal", label: t("允许远程主机写入剪贴板（OSC 52）"), keywords: ["clipboard", "osc52", t("剪贴板")] },
  { id: "ctrl-v-paste", section: "settings-terminal", label: t("Ctrl+V 粘贴"), description: t("Windows：关闭后 Ctrl+V 发送 ^V"), keywords: ["paste", "ctrl+v", "clipboard", "vim", "^V", "quoted-insert", t("块选择"), t("粘贴"), t("剪贴板")], available: isWindows },
  { id: "terminal-confirm-multiline-paste", section: "settings-terminal", label: t("多行粘贴确认"), description: t("粘贴多行文本前的确认方式"), keywords: ["paste", "bracketed paste", t("括号粘贴"), "multiline", "multi-line", "confirm", "clipboard", t("粘贴"), t("多行"), t("确认"), t("提示"), t("剪贴板")] },
  { id: "copy-on-select", section: "settings-terminal", label: t("选中自动复制"), keywords: ["copy", "select", "clipboard", t("复制"), t("剪贴板")] },
  { id: "terminal-link-activation", section: "settings-terminal", label: t("链接打开方式"), description: t("Ctrl/Cmd+单击或单击打开终端中的链接"), keywords: ["link", "url", "hyperlink", "osc8", "click", "ctrl", "cmd", t("链接"), t("超链接"), t("单击")] },
  { id: "terminal-link-open-mode", section: "settings-terminal", label: t("链接打开位置"), description: t("http/https 链接在系统浏览器、NexTerm 内打开，或每次询问"), keywords: ["link", "url", "browser", "preview", "iframe", "open", t("链接"), t("浏览器"), t("预览"), t("内置"), t("打开位置")], available: () => !isMobileShell() },
  { id: "terminal-trzsz", section: "settings-terminal", label: t("trzsz 文件传输"), description: t("远端运行 trz / tsz 时在终端内上传或下载文件"), keywords: ["trzsz", "trz", "tsz", "rz", "sz", "zmodem", "upload", "download", "transfer", t("上传"), t("下载"), t("文件传输")] },
  { id: "terminal-option-meta", section: "settings-terminal", label: t("Option 键作为 Meta 键"), description: t("macOS 上让 Option 键在终端里发送 ESC 前缀"), keywords: ["option", "meta", "alt", "esc", "tmux", "emacs", t("键盘")], available: isMac },
  { id: "terminal-port-discovery", section: "settings-terminal", label: t("端口发现"), description: t("检测远端新监听的端口，并提示一键转发"), keywords: ["port", "discovery", "forward", "listen", "ss", "netstat", t("端口"), t("发现"), t("转发"), t("监听")] },
  { id: "terminal-gpu-acceleration", section: "settings-terminal", label: t("终端 GPU 加速"), description: t("自动、开启或关闭 WebGL 渲染"), keywords: ["gpu", "webgl", "render", "renderer", "acceleration", t("硬件加速"), t("渲染")], panel: "terminal-advanced" },
  { id: "terminal-scrollback", section: "settings-terminal", label: t("终端回滚行数"), description: t("每个终端在本机保留的滚动历史行数"), keywords: ["scrollback", "history", "buffer", "lines", t("滚动"), t("回滚"), t("历史"), t("行数")], panel: "terminal-advanced" },
  { id: "terminal-durable-history", section: "settings-terminal", label: t("持久终端历史上限"), description: t("每个新建的持久终端在主机上保留的输出量"), keywords: ["history", "durable", "limit", "quota", "scrollback", t("历史"), t("持久"), t("上限"), t("配额")], panel: "terminal-advanced", available: terminalDefaultUsesAccount },
  { id: "system-notifications", section: "settings-notifications", label: t("系统通知"), description: t("会话需要处理或长时间命令结束时，在本机显示系统通知"), keywords: ["notification", "notify", "bell", "inbox", "osc9", t("通知"), t("系统通知"), t("响铃"), t("铃铛"), t("收件箱")] },
  { id: "state-rules-import", section: "settings-state-rules", label: t("导入或导出状态规则"), keywords: ["import", "export", "json", t("导入"), t("导出")] },
  { id: "state-rules-status", section: "settings-state-rules", label: t("各主机的规则状态"), description: t("各主机的持久终端是否已应用当前规则"), keywords: ["push", "helper", "host", t("下发"), t("主机"), t("组件"), t("生效")] },
  { id: "keyword-highlight-presets", section: "settings-keyword-highlight", label: t("内置预设"), description: t("错误、警告、成功、IPv4 地址和 UUID 的高亮"), keywords: ["preset", "error", "warning", "ip", "uuid", t("预设")] },
  { id: "keyword-highlight-preview", section: "settings-keyword-highlight", label: t("高亮预览"), keywords: ["preview", "sample", t("预览"), t("示例")] },
  { id: "layout-templates", section: "settings-layout-templates", label: t("布局模板"), description: t("保存工作区分屏布局，按模板打开主机终端并运行启动片段"), keywords: ["layout", "template", "arrangement", "split", t("布局"), t("模板"), t("分屏")] },
  { id: "quick-commands", section: "settings-quick-commands", label: t("快捷命令"), description: t("在终端按键条中发送的常用命令"), keywords: ["quick command", "snippet", "macro", t("命令"), t("常用命令"), t("片段"), t("按键条")] },
  { id: "memory-mode", section: "settings-memory", label: t("AI 使用记忆"), description: t("引用记忆、允许记忆工具或关闭"), keywords: ["memory", "inject", "tool", t("记忆"), t("记忆工具"), t("引用")] },
  { id: "vault-password", section: "settings-vault", label: t("用密码保护凭据"), keywords: ["password", "credential", "vault", t("密码"), t("凭据")] },
  { id: "vault-auto-lock", section: "settings-vault", label: t("闲置自动锁定"), keywords: ["lock", "idle", "timeout", t("锁定"), t("凭据")] },
  { id: "app-lock-idle", section: "settings-app-lock", label: t("闲置自动锁定 NexTerm"), keywords: ["lock screen", "idle", "timeout", t("锁屏"), t("锁定")] },
  { id: "local-helper-exposure", section: "settings-account", label: t("向服务端账号开放本机终端"), description: t("让同一账号的浏览器和手机接管本机终端"), keywords: ["expose", "remote", "local terminal", t("开放"), t("本机终端"), t("远程")], available: () => DESKTOP && reportedVisible("local-helper-exposure") },
];

export type SettingsSearchResult =
  | { kind: "section"; key: string; sectionId: string; label: string; detail: string }
  | { kind: "item"; key: string; sectionId: string; itemId: string; label: string; detail: string; panel?: SettingsPanelId };

const normalize = (text: string) => text.toLowerCase().normalize("NFKC");

function matches(haystack: string, tokens: readonly string[]): boolean {
  return tokens.every((token) => haystack.includes(token));
}

// A query is split on whitespace and every token must appear somewhere in the
// entry's label, description or keywords. Section matches come first.
export function searchSettings(
  query: string,
  sections: readonly { id: string; label: string }[],
): SettingsSearchResult[] {
  const tokens = normalize(query).split(/\s+/).filter(Boolean);
  if (tokens.length === 0) return [];
  const labelOf = new Map(sections.map((section) => [section.id, section.label]));
  const sectionResults: SettingsSearchResult[] = [];
  for (const section of sections) {
    const meta = SECTION_SEARCH[section.id as SettingsSectionId];
    const text = normalize([section.label, meta?.description ?? "", ...(meta?.keywords ?? [])].join(" "));
    if (matches(text, tokens)) {
      sectionResults.push({
        kind: "section",
        key: section.id,
        sectionId: section.id,
        label: section.label,
        detail: meta?.description ?? "",
      });
    }
  }
  const itemResults: SettingsSearchResult[] = [];
  for (const item of SETTINGS_ITEMS) {
    const sectionLabel = labelOf.get(item.section);
    if (item.available && !item.available()) continue;
    if (!sectionLabel) continue; // The section is not available on this platform.
    const text = normalize([item.label, item.description ?? "", ...(item.keywords ?? [])].join(" "));
    if (matches(text, tokens)) {
      itemResults.push({
        kind: "item",
        key: `${item.section}:${item.id}`,
        sectionId: item.section,
        itemId: item.id,
        panel: item.panel,
        label: item.label,
        detail: sectionLabel,
      });
    }
  }
  return [...sectionResults, ...itemResults];
}
