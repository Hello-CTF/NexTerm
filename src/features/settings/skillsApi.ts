/**
 * Contract between the Settings skills UI and the skills backend service.
 *
 * The production backend is productionSkillsApi below: the typed
 * ai_skills_* IPC commands generated from the Go service in
 * internal/ai/skills, registered by the production runtime. Field names
 * mirror the Go service JSON. ./skillsMock is the explicit test double the
 * UI tests run against; it is never shipped as the production backend.
 */

import { call, toAppError } from "../../ipc/commands";
import type {
  Dispatch as WireDispatch,
  Entry as WireEntry,
  OverrideInput as WireOverrideInput,
} from "../../ipc/generated/types";

export type SkillSource = "embedded" | "custom";

export interface SkillOverride {
  name: string;
  description?: string;
  body?: string;
  disabled?: boolean;
  updatedAt: number;
}

export interface SkillEntry {
  name: string;
  description: string;
  source: SkillSource;
  overridden: boolean;
  disabled: boolean;
  digest: string;
  updatedAt: number;
  override?: SkillOverride;
}

export interface SkillOverrideInput {
  name: string;
  description?: string;
  body?: string;
  disabled?: boolean;
}

export interface SkillDispatch {
  name: string;
  description: string;
  body: string;
  digest: string;
  source: SkillSource;
}

export type SkillErrorCode = "not_found" | "disabled" | "invalid_param";

export class SkillError extends Error {
  readonly code: SkillErrorCode;

  constructor(code: SkillErrorCode, message: string) {
    super(message);
    this.name = "SkillError";
    this.code = code;
  }
}

export function isSkillError(error: unknown): error is SkillError {
  return error instanceof SkillError;
}

export interface SkillsApi {
  list(): Promise<SkillEntry[]>;
  saveOverride(input: SkillOverrideInput): Promise<SkillEntry>;
  deleteOverride(name: string): Promise<void>;
  dispatch(name: string): Promise<SkillDispatch>;
  /**
   * Settings read path: returns the effective material, including for
   * disabled skills. Distinct from dispatch(), which is the model
   * load_skill path and rejects disabled skills.
   */
  getEffective(name: string): Promise<SkillDispatch>;
}

export const SKILL_NAME_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/;

export function isValidSkillName(name: string): boolean {
  return name.length > 0 && name.length <= 64 && SKILL_NAME_PATTERN.test(name);
}

function toSkillSource(source: string): SkillSource {
  return source === "embedded" ? "embedded" : "custom";
}

function toSkillEntry(entry: WireEntry): SkillEntry {
  const result: SkillEntry = {
    name: entry.name,
    description: entry.description,
    source: toSkillSource(entry.source),
    overridden: entry.overridden,
    disabled: entry.disabled,
    digest: entry.digest,
    updatedAt: entry.updatedAt,
  };
  if (entry.override) {
    result.override = {
      name: entry.override.name,
      description: entry.override.description ?? undefined,
      body: entry.override.body ?? undefined,
      disabled: entry.override.disabled ?? false,
      updatedAt: entry.override.updatedAt,
    };
  }
  return result;
}

function toSkillDispatch(dispatch: WireDispatch): SkillDispatch {
  return {
    name: dispatch.name,
    description: dispatch.description,
    body: dispatch.body,
    digest: dispatch.digest,
    source: toSkillSource(dispatch.source),
  };
}

function toWireOverrideInput(input: SkillOverrideInput): WireOverrideInput {
  return {
    name: input.name,
    ...(input.description !== undefined ? { description: input.description } : {}),
    ...(input.body !== undefined ? { body: input.body } : {}),
    ...(input.disabled !== undefined ? { disabled: input.disabled } : {}),
  };
}

// The wire folds a disabled-skill rejection into bad_param, so it surfaces
// here as invalid_param with the server's disabled guidance in the message;
// every other non-domain failure propagates unchanged for describeError.
function rethrowSkillError(error: unknown): never {
  const appError = toAppError(error);
  if (appError.code === "not_found") throw new SkillError("not_found", appError.message);
  if (appError.code === "bad_param") throw new SkillError("invalid_param", appError.message);
  throw error;
}

/**
 * The production SkillsApi: typed ai_skills_* IPC calls, checked at compile
 * time against the generated contract. No unsafeCall and no mock data.
 */
export const productionSkillsApi: SkillsApi = {
  async list(): Promise<SkillEntry[]> {
    try {
      const entries = await call("ai_skills_list");
      return entries.map(toSkillEntry);
    } catch (error) {
      rethrowSkillError(error);
    }
  },
  async saveOverride(input: SkillOverrideInput): Promise<SkillEntry> {
    try {
      return toSkillEntry(await call("ai_skills_save_override", toWireOverrideInput(input)));
    } catch (error) {
      rethrowSkillError(error);
    }
  },
  async deleteOverride(name: string): Promise<void> {
    try {
      await call("ai_skills_delete_override", { name });
    } catch (error) {
      rethrowSkillError(error);
    }
  },
  async dispatch(name: string): Promise<SkillDispatch> {
    try {
      return toSkillDispatch(await call("ai_skills_dispatch", { name }));
    } catch (error) {
      rethrowSkillError(error);
    }
  },
  async getEffective(name: string): Promise<SkillDispatch> {
    try {
      return toSkillDispatch(await call("ai_skills_material", { name }));
    } catch (error) {
      rethrowSkillError(error);
    }
  },
};
