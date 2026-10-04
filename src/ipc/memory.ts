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
  create: (scope: MemoryScope, topic: string, content: string, secrets?: MemorySecretPolicy) =>
    call<MemoryEntry>("memory_create", { scope, topic, content, secrets }),

  get: (scope: MemoryScope, id: string) =>
    call<MemoryEntry>("memory_get", { scope, id }),

  edit: (
    scope: MemoryScope,
    id: string,
    expectedVersion: number,
    patch: { topic?: string; content?: string; secrets?: MemorySecretPolicy },
  ) => call<MemoryEntry>("memory_edit", { scope, id, expectedVersion, ...patch }),

  delete: (scope: MemoryScope, id: string, expectedVersion: number) =>
    call<void>("memory_delete", { scope, id, expectedVersion }),

  index: (scope: MemoryScope) =>
    call<MemoryTopicIndex[]>("memory_index", { scope }),

  settings: (scope: MemoryScope) =>
    call<MemorySettings>("memory_settings_get", { scope }),

  setSettings: (
    scope: MemoryScope,
    expectedVersion: number,
    patch: { injectionEnabled?: boolean; toolsEnabled?: boolean },
  ) => call<MemorySettings>("memory_settings_set", { scope, expectedVersion, ...patch }),
};
