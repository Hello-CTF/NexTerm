import { call } from "./commands";

export interface MemoryScope {
  tenant: string;
  subject: string;
}

export interface MemoryEntry {
  id: string;
  topic: string;
  content: string;
  version: number;
  redacted: boolean;
  createdAt: number;
  updatedAt: number;
}

export interface MemoryIndexEntry {
  id: string;
  version: number;
  redacted: boolean;
  updatedAt: number;
}

export interface MemoryTopicIndex {
  topic: string;
  entries: MemoryIndexEntry[];
}

export interface MemorySettings {
  injectionEnabled: boolean;
  toolsEnabled: boolean;
  version: number;
}

export type MemorySecretPolicy = "reject" | "redact";

export const memoryApi = {
  create: (topic: string, content: string, secrets?: MemorySecretPolicy) =>
    call<MemoryEntry>("memory_create", { topic, content, secrets }),

  get: (id: string) => call<MemoryEntry>("memory_get", { id }),

  edit: (
    id: string,
    expectedVersion: number,
    patch: { topic?: string; content?: string; secrets?: MemorySecretPolicy },
  ) => call<MemoryEntry>("memory_edit", { id, expectedVersion, ...patch }),

  delete: (id: string, expectedVersion: number) =>
    call<void>("memory_delete", { id, expectedVersion }),

  index: () => call<MemoryTopicIndex[]>("memory_index", {}),

  settings: () => call<MemorySettings>("memory_settings_get", {}),

  setSettings: (
    expectedVersion: number,
    patch: { injectionEnabled?: boolean; toolsEnabled?: boolean },
  ) => call<MemorySettings>("memory_settings_set", { expectedVersion, ...patch }),
};
