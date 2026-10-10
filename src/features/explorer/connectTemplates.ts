import { create } from "zustand";
import type { Asset } from "../../ipc/commands";

const KEY = "nexterm.connectTemplates.v1";

export interface ConnectTemplate {
  id: string;
  name: string;
  kind: Asset["kind"];
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

const KINDS = new Set(["ssh", "winrm", "local", "docker", "mysql", "postgres", "redis"]);

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
        kind: t.kind as Asset["kind"],
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
