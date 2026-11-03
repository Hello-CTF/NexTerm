import { create } from "zustand";
import type { Asset } from "../../ipc/commands";

const KEY = "nexterm.connectTemplates.v1";

export interface ConnectTemplate {
  id: string;
  name: string;
  kind: Exclude<Asset["kind"], "device">;
  port: number;
  username: string;
  authKind: string;
  keyOrigin: "ref" | "vault";
  keyPath: string;
  vaultCredId: string;
  jumpAssetId: string;
  proxyCommand: string;
  forwardAgent: boolean;
  agentSocket: string;
  certPath: string;
  startupCommand: string;
  encoding: string;
  envText: string;
}

export type ConnectTemplateInput = Omit<ConnectTemplate, "id">;

const KINDS = new Set(["ssh", "winrm", "local", "docker", "mysql", "postgres", "redis", "rdp", "vnc"]);

function load(): ConnectTemplate[] {
  try {
    const raw = localStorage.getItem(KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) return [];
    const templates: ConnectTemplate[] = [];
    for (const item of parsed) {
      if (!item || typeof item !== "object") continue;
      const t = item as Record<string, unknown>;
      if (typeof t.id !== "string" || typeof t.name !== "string") continue;
      if (typeof t.kind !== "string" || !KINDS.has(t.kind)) continue;
      templates.push({
        id: t.id,
        name: t.name,
        kind: t.kind as Exclude<Asset["kind"], "device">,
        port: typeof t.port === "number" ? t.port : 22,
        username: typeof t.username === "string" ? t.username : "",
        authKind: typeof t.authKind === "string" ? t.authKind : "password",
        keyOrigin: t.keyOrigin === "vault" ? "vault" : "ref",
        keyPath: typeof t.keyPath === "string" ? t.keyPath : "",
        vaultCredId: typeof t.vaultCredId === "string" ? t.vaultCredId : "",
        jumpAssetId: typeof t.jumpAssetId === "string" ? t.jumpAssetId : "",
        proxyCommand: typeof t.proxyCommand === "string" ? t.proxyCommand : "",
        forwardAgent: t.forwardAgent === true,
        agentSocket: typeof t.agentSocket === "string" ? t.agentSocket : "",
        certPath: typeof t.certPath === "string" ? t.certPath : "",
        startupCommand: typeof t.startupCommand === "string" ? t.startupCommand : "",
        encoding: typeof t.encoding === "string" ? t.encoding : "utf-8",
        envText: typeof t.envText === "string" ? t.envText : "",
      });
    }
    return templates;
  } catch {
    return [];
  }
}

function save(templates: ConnectTemplate[]): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(templates));
  } catch {
  }
}

let templateSeq = 1;

interface ConnectTemplatesState {
  templates: ConnectTemplate[];
  add: (input: ConnectTemplateInput) => ConnectTemplate;
  remove: (id: string) => void;
}

export const useConnectTemplates = create<ConnectTemplatesState>((set) => ({
  templates: load(),
  add: (input) => {
    const template: ConnectTemplate = {
      ...input,
      id: `tpl-${Date.now().toString(36)}-${templateSeq++}`,
    };
    set((s) => {
      const templates = [...s.templates, template];
      save(templates);
      return { templates };
    });
    return template;
  },
  remove: (id) =>
    set((s) => {
      if (!s.templates.some((t) => t.id === id)) return s;
      const templates = s.templates.filter((t) => t.id !== id);
      save(templates);
      return { templates };
    }),
}));

// restrictTemplateForServer drops the settings a multi-user server rejects, so applying a template
// there never selects them. It reports which fields were dropped.
export function restrictTemplateForServer(t: ConnectTemplate): { template: ConnectTemplate; dropped: string[] } {
  const dropped: string[] = [];
  const next = { ...t };
  if (next.kind === "local") {
    next.kind = "ssh";
    dropped.push("本地终端类型");
  }
  if (next.authKind === "agent") {
    next.authKind = "password";
    dropped.push("SSH Agent 认证");
  }
  if (next.proxyCommand) {
    next.proxyCommand = "";
    dropped.push("代理命令");
  }
  if (next.forwardAgent) {
    next.forwardAgent = false;
    dropped.push("Agent 转发");
  }
  if (next.agentSocket) {
    next.agentSocket = "";
    dropped.push("Agent socket");
  }
  if (next.certPath) {
    next.certPath = "";
    dropped.push("证书路径");
  }
  if (next.keyPath || next.keyOrigin === "ref") {
    if (next.keyPath) dropped.push("私钥路径");
    next.keyPath = "";
    next.keyOrigin = "vault";
  }
  return { template: next, dropped };
}
