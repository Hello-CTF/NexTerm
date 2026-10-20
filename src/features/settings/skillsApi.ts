/**
 * Contract between the Settings skills UI and the skills backend service.
 *
 * The real IPC-backed implementation lands with the load_skill IPC work;
 * until then the UI is developed and tested against this contract and the
 * explicit mock in ./skillsMock. Field names mirror the Go service JSON in
 * internal/ai/skills so the IPC binding can stay a thin translation.
 */

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
