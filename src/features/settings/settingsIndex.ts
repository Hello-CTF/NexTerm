// Declarative search index for the settings view. Section metadata is keyed by
// SettingsSectionId and SETTINGS_SECTIONS is checked against that union, so a
// section without an entry here, or with an unknown id, is a type error.
// Individual settings are listed in SETTINGS_ITEMS; an item is scrolled to and
// highlighted through the element carrying data-setting="<item id>", and falls
// back to its section when that element is not rendered.

export type SettingsSectionId =
  | "settings-appearance"
  | "settings-terminal"
  | "settings-state-rules"
  | "settings-shortcuts"
  | "settings-update"
  | "settings-diagnostics"
  | "settings-ai-model"
  | "settings-ai-privacy"
  | "settings-ai-rules"
  | "settings-ai-skills"
  | "settings-memory"
  | "settings-vault"
  | "settings-known-hosts"
  | "settings-production"
  | "settings-account"
  | "settings-bundle"
  | "settings-share";

export interface SettingsSearchMeta {
  description: string;
  keywords: readonly string[];
}

export const SECTION_SEARCH: Record<SettingsSectionId, SettingsSearchMeta> = {
  "settings-appearance": {
    description: "界面主题、字号、终端主题与字体",
    keywords: ["appearance", "theme", "dark", "light", "font", "主题", "深色", "浅色", "字体", "字号", "外观"],
  },
  "settings-terminal": {
    description: "默认终端模式、剪贴板、关闭行为、GPU 加速、快捷命令",
    keywords: ["terminal", "durable", "persistent", "osc52", "clipboard", "tray", "snippet", "持久", "剪贴板", "托盘", "后台", "快捷命令"],
  },
  "settings-state-rules": {
    description: "按屏幕内容纠正会话状态（需要我、运行中、空闲）的规则",
    keywords: ["state", "rule", "needs me", "attention", "badge", "regex", "pattern", "状态", "规则", "需要我", "徽章", "误报", "正则", "标记", "test", "测试"],
  },
  "settings-shortcuts": {
    description: "自定义快捷键与选中复制",
    keywords: ["shortcut", "keybinding", "hotkey", "keyboard", "key", "split", "pane", "close pane", "快捷键", "键盘", "按键", "热键", "分屏", "窗格"],
  },
  "settings-update": {
    description: "检查并安装应用更新",
    keywords: ["update", "upgrade", "version", "download", "更新", "升级", "版本"],
  },
  "settings-diagnostics": {
    description: "调试日志与抓取文件",
    keywords: ["diagnostics", "debug", "log", "capture", "trace", "诊断", "调试", "日志", "抓包"],
  },
  "settings-ai-model": {
    description: "AI 模型档案、密钥与连通性测试",
    keywords: ["ai", "model", "api key", "openai", "ollama", "llm", "模型", "密钥", "大模型", "连通性"],
  },
  "settings-ai-privacy": {
    description: "本地模型检测敏感信息、稳定占位符与回复还原，下载或删除本地模型",
    keywords: ["privacy", "guard", "astrlink", "int8", "fp32", "fp16", "脱敏", "隐私", "敏感", "还原", "模型下载", "删除模型", "磁盘占用"],
  },
  "settings-ai-rules": {
    description: "AI 命中后需要确认或拒绝的危险规则",
    keywords: ["rule", "danger", "block", "permission", "rm", "拦截", "规则", "危险", "权限", "确认"],
  },
  "settings-ai-skills": {
    description: "管理 AI 技能",
    keywords: ["skill", "skills", "prompt", "技能"],
  },
  "settings-memory": {
    description: "AI 长期记忆与记忆工具",
    keywords: ["memory", "remember", "记忆", "长期记忆"],
  },
  "settings-vault": {
    description: "用密码保护凭据，闲置自动锁定",
    keywords: ["credential", "vault", "password", "lock", "encrypt", "凭据", "密码", "加密", "锁定", "保险库", "安全"],
  },
  "settings-known-hosts": {
    description: "已信任的 SSH 主机指纹",
    keywords: ["known hosts", "fingerprint", "ssh", "trust", "host key", "指纹", "信任", "主机"],
  },
  "settings-production": {
    description: "生产环境主机的额外保护",
    keywords: ["production", "prod", "safety", "protect", "生产", "保护", "确认"],
  },
  "settings-account": {
    description: "账号、用户管理与多端同步",
    keywords: ["account", "sync", "server", "user", "mfa", "login", "proxy", "账号", "同步", "用户", "登录", "两步验证", "代理", "服务器"],
  },
  "settings-bundle": {
    description: "资产包导入与导出",
    keywords: ["bundle", "import", "export", "backup", "file", "资产", "导入", "导出", "备份"],
  },
  "settings-share": {
    description: "分享主机与会话给他人",
    keywords: ["share", "link", "invite", "分享", "链接", "邀请"],
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
}

export type SettingsPanelId = "appearance-advanced" | "shortcuts-advanced";

export const SETTINGS_ITEMS: readonly SettingsItem[] = [
  { id: "ui-theme", section: "settings-appearance", label: "界面主题", description: "跟随系统、浅色或深色", keywords: ["theme", "dark", "light", "主题", "深色", "浅色"] },
  { id: "ui-font-size", section: "settings-appearance", label: "界面字号", keywords: ["font size", "字体", "字号"], panel: "appearance-advanced" },
  { id: "ui-text-scale", section: "settings-appearance", label: "界面文字倍率", keywords: ["scale", "zoom", "缩放", "倍率"], panel: "appearance-advanced" },
  { id: "terminal-theme", section: "settings-appearance", label: "终端与编辑器主题", keywords: ["theme", "color scheme", "配色", "主题"], panel: "appearance-advanced" },
  { id: "terminal-contrast", section: "settings-appearance", label: "终端对比度", keywords: ["contrast", "对比度"], panel: "appearance-advanced" },
  { id: "terminal-font-size", section: "settings-appearance", label: "终端字号", keywords: ["font size", "字体", "字号"], panel: "appearance-advanced" },
  { id: "terminal-font-family", section: "settings-appearance", label: "终端字体", keywords: ["font", "family", "cascadia", "字体"], panel: "appearance-advanced" },
  { id: "default-terminal-mode", section: "settings-terminal", label: "默认终端模式", description: "持久终端或普通终端", keywords: ["durable", "persistent", "detach", "持久", "local", "本机", "本地终端", "持久组件"] },
  { id: "osc52", section: "settings-terminal", label: "允许远程主机写入剪贴板（OSC 52）", keywords: ["clipboard", "osc52", "剪贴板"] },
  { id: "close-action", section: "settings-terminal", label: "关闭运行中的终端标签时", keywords: ["close", "kill", "detach", "关闭", "后台"] },
  { id: "terminal-gpu-acceleration", section: "settings-terminal", label: "终端 GPU 加速", description: "自动、开启或关闭 WebGL 渲染", keywords: ["gpu", "webgl", "render", "renderer", "acceleration", "硬件加速", "渲染"] },
  { id: "terminal-scrollback", section: "settings-terminal", label: "终端回滚行数", description: "每个终端在这台设备上保留的滚动历史行数，不随账号同步", keywords: ["scrollback", "history", "buffer", "lines", "滚动", "回滚", "历史", "行数"] },
  { id: "terminal-durable-history", section: "settings-terminal", label: "持久终端历史上限", description: "主机为每个新开的持久终端保留的输出量，随账号同步", keywords: ["history", "durable", "limit", "quota", "scrollback", "历史", "持久", "上限", "配额"] },
  { id: "quick-commands", section: "settings-terminal", label: "快捷命令", description: "在终端按键条中一键发送的常用命令，随账号同步", keywords: ["quick command", "snippet", "macro", "命令", "常用命令", "片段", "按键条"] },
  { id: "terminal-link-activation", section: "settings-terminal", label: "链接打开方式", description: "Ctrl/Cmd+单击或单击打开终端中的链接", keywords: ["link", "url", "hyperlink", "osc8", "click", "ctrl", "cmd", "链接", "超链接", "单击"] },
  { id: "terminal-confirm-multiline-paste", section: "settings-terminal", label: "多行粘贴确认", description: "程序未开启括号粘贴时、每次或从不在粘贴多行文本前确认，随账号同步", keywords: ["paste", "bracketed paste", "括号粘贴", "multiline", "multi-line", "confirm", "clipboard", "粘贴", "多行", "确认", "提示", "剪贴板"] },
  { id: "system-notifications", section: "settings-terminal", label: "系统通知", description: "会话需要你或长命令结束时在此设备弹出系统通知", keywords: ["notification", "notify", "bell", "inbox", "osc9", "通知", "系统通知", "响铃", "铃铛", "收件箱"] },
  { id: "state-rules-import", section: "settings-state-rules", label: "导入或导出状态规则", keywords: ["import", "export", "json", "导入", "导出"] },
  { id: "state-rules-status", section: "settings-state-rules", label: "各主机的规则状态", description: "各主机的持久终端是否已应用当前规则", keywords: ["push", "helper", "host", "下发", "主机", "组件", "生效"] },
  { id: "copy-on-select", section: "settings-shortcuts", label: "选中自动复制", keywords: ["copy", "select", "clipboard", "复制", "剪贴板"], panel: "shortcuts-advanced" },
  { id: "vault-password", section: "settings-vault", label: "用密码保护凭据", keywords: ["password", "credential", "vault", "密码", "凭据"] },
  { id: "vault-auto-lock", section: "settings-vault", label: "闲置自动锁定", keywords: ["lock", "idle", "timeout", "锁定", "凭据"] },
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
