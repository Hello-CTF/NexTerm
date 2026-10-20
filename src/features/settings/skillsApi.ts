/**
 * Contract between the Settings skills UI and the skills backend service.
 *
 * Field names mirror the Go service JSON in internal/ai/skills so the IPC
 * binding stays a thin translation. createCallSkillsApi is the real
 * call-based adapter over the project IPC bridge; the explicit mock in
 * ./skillsMock mirrors the same semantics for tests and local UI work.
 * The adapter reaches the backend only once the shared IPC owner registers
 * the Go commands (skills.RegisterIPC) — until then calls fail with an
 * unknown-command error, so no end-to-end flow is claimed here.
 */
import { unsafeCall } from "../../ipc/commands";

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

// Command names mirroring the Go constants in internal/ai/skills/ipc.go.
export const SKILL_COMMANDS = {
  list: "ai_skills_list",
  saveOverride: "ai_skills_save_override",
  deleteOverride: "ai_skills_delete_override",
  dispatch: "ai_skills_dispatch",
  material: "ai_skills_material",
} as const;

export type SkillsCall = <T>(command: string, args?: Record<string, unknown>) => Promise<T>;

/**
 * Real call-based adapter over the project IPC bridge. New command names
 * cannot enter the generated manifest (owned by the shared IPC owner), so
 * this goes through unsafeCall; errors surface as AppError with the Go
 * service's codes (not_found, bad_param, forbidden).
 */
export function createCallSkillsApi(invoke: SkillsCall = unsafeCall): SkillsApi {
  return {
    list: () => invoke<SkillEntry[]>(SKILL_COMMANDS.list, {}),
    saveOverride: (input) => invoke<SkillEntry>(SKILL_COMMANDS.saveOverride, { ...input }),
    deleteOverride: (name) => invoke<void>(SKILL_COMMANDS.deleteOverride, { name }),
    dispatch: (name) => invoke<SkillDispatch>(SKILL_COMMANDS.dispatch, { name }),
    getEffective: (name) => invoke<SkillDispatch>(SKILL_COMMANDS.material, { name }),
  };
}

export const skillsApi: SkillsApi = createCallSkillsApi();

