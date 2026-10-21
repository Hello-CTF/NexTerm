/**
 * Explicit mock backend for the Settings skills contract. It reproduces the
 * semantics of the Go service in internal/ai/skills — per-owner override
 * isolation, partial override inheritance, disable and delete fallback,
 * custom-skill validation, and sha256 digests over the exact dispatched
 * material — so the UI and its tests run without a live backend. The
 * production backend is productionSkillsApi in ./skillsApi (typed
 * ai_skills_* IPC); this mock is a test double only.
 */
import {
  isValidSkillName,
  SkillError,
  type SkillDispatch,
  type SkillEntry,
  type SkillOverrideInput,
  type SkillsApi,
} from "./skillsApi";

export interface SkillSeed {
  name: string;
  description: string;
  body: string;
}

interface StoredOverride {
  description?: string;
  body?: string;
  disabled: boolean;
  updatedAt: number;
}

export interface SkillsMockBackend {
  apiFor(ownerKey: string): SkillsApi;
}

const encoder = new TextEncoder();

function byteLength(value: string): number {
  return encoder.encode(value).length;
}

// Fields are framed as "<utf8-byte-length>:<value>" so values containing
// separators cannot make distinct dispatched tuples collide; this matches
// the Go service's canonicalMaterial encoding exactly.
async function digestOf(name: string, description: string, body: string): Promise<string> {
  const canonical = [name, description, body]
    .map((field) => `${byteLength(field)}:${field}`)
    .join("");
  const digest = await globalThis.crypto.subtle.digest("SHA-256", encoder.encode(canonical));
  const hex = [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  return `sha256:${hex}`;
}

export function createSkillsMockBackend(embeddedSeeds: SkillSeed[]): SkillsMockBackend {
  const embedded = new Map(embeddedSeeds.map((seed) => [seed.name, seed]));
  const ownerStores = new Map<string, Map<string, StoredOverride>>();
  let updatedAtClock = 0;

  function storeFor(ownerKey: string): Map<string, StoredOverride> {
    let store = ownerStores.get(ownerKey);
    if (!store) {
      store = new Map();
      ownerStores.set(ownerKey, store);
    }
    return store;
  }

  function effectiveOf(
    seed: SkillSeed | undefined,
    override: StoredOverride | undefined,
  ): { description: string; body: string } {
    return {
      description: override?.description ?? seed?.description ?? "",
      body: override?.body ?? seed?.body ?? "",
    };
  }

  async function entryFor(name: string, override: StoredOverride | undefined): Promise<SkillEntry> {
    const seed = embedded.get(name);
    const effective = effectiveOf(seed, override);
    const entry: SkillEntry = {
      name,
      description: effective.description,
      source: seed ? "embedded" : "custom",
      overridden: Boolean(override && seed && (override.description !== undefined || override.body !== undefined)),
      disabled: override?.disabled ?? false,
      digest: await digestOf(name, effective.description, effective.body),
      updatedAt: override?.updatedAt ?? 0,
    };
    if (override) {
      entry.override = {
        name,
        description: override.description,
        body: override.body,
        disabled: override.disabled,
        updatedAt: override.updatedAt,
      };
    }
    return entry;
  }

  function validateOverrideInput(name: string, hasBase: boolean, input: SkillOverrideInput): void {
    if (input.description !== undefined) {
      const trimmed = input.description.trim();
      if (!trimmed || byteLength(trimmed) > 256) {
        throw new SkillError("invalid_param", `AI 技能内容无效: invalid override description for ${name}`);
      }
    }
    if (input.body !== undefined) {
      if (!input.body.trim() || byteLength(input.body) > 64 * 1024) {
        throw new SkillError("invalid_param", `AI 技能内容无效: invalid override body for ${name}`);
      }
    }
    if (!hasBase) {
      if (input.description === undefined || input.body === undefined) {
        throw new SkillError("invalid_param", `AI 技能内容无效: custom skill ${name} requires description and body`);
      }
      return;
    }
    if (input.description === undefined && input.body === undefined && !input.disabled) {
      throw new SkillError("invalid_param", `AI 技能内容无效: empty override for ${name}`);
    }
  }

  function apiFor(ownerKey: string): SkillsApi {
    async function materialFor(name: string, allowDisabled: boolean): Promise<SkillDispatch> {
      const store = storeFor(ownerKey);
      const override = store.get(name);
      const seed = embedded.get(name);
      if (!seed && !override) {
        throw new SkillError("not_found", `AI 技能不存在，可能已被删除或覆盖已移除: ${name}`);
      }
      if (!allowDisabled && override?.disabled) {
        throw new SkillError("disabled", `AI 技能已被禁用，请在设置中启用后重试: ${name}`);
      }
      const effective = effectiveOf(seed, override);
      return {
        name,
        description: effective.description,
        body: effective.body,
        digest: await digestOf(name, effective.description, effective.body),
        source: seed ? "embedded" : "custom",
      };
    }

    return {
      async list(): Promise<SkillEntry[]> {
        const store = storeFor(ownerKey);
        const names = [...new Set([...embedded.keys(), ...store.keys()])].sort();
        return Promise.all(names.map((name) => entryFor(name, store.get(name))));
      },

      async saveOverride(input: SkillOverrideInput): Promise<SkillEntry> {
        const name = input.name.trim();
        if (!isValidSkillName(name)) {
          throw new SkillError("invalid_param", `AI 技能内容无效: invalid skill name ${name}`);
        }
        const hasBase = embedded.has(name);
        validateOverrideInput(name, hasBase, input);
        const override: StoredOverride = {
          disabled: input.disabled ?? false,
          updatedAt: ++updatedAtClock,
        };
        if (input.description !== undefined) override.description = input.description.trim();
        if (input.body !== undefined) override.body = input.body;
        storeFor(ownerKey).set(name, override);
        return entryFor(name, override);
      },

      async deleteOverride(name: string): Promise<void> {
        const store = storeFor(ownerKey);
        if (!store.delete(name.trim())) {
          throw new SkillError("not_found", `AI 技能不存在，可能已被删除或覆盖已移除: ${name}`);
        }
      },

      async dispatch(name: string): Promise<SkillDispatch> {
        return materialFor(name, false);
      },

      async getEffective(name: string): Promise<SkillDispatch> {
        return materialFor(name, true);
      },
    };
  }

  return { apiFor };
}
