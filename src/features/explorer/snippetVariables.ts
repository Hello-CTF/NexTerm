import { assetTagValues } from "./assetSearch";

export interface SnippetVariable {
  name: string;
  required: boolean;
  defaultValue?: string;
  multiline: boolean;
}

export type SnippetVariableValues = Readonly<Record<string, string>>;

export interface SnippetAsset {
  name: string;
  host: string | null;
  tags: string;
}

export interface SnippetTemplateAnalysis {
  hasVariables: boolean;
  usesAsset: boolean;
  variables: SnippetVariable[];
  unknownVariables: string[];
}

export interface SnippetRenderTarget {
  id: string;
  label?: string;
  asset?: SnippetAsset;
}

export interface RenderedSnippetTarget {
  id: string;
  command: string;
}

type ParsedVariable = SnippetVariable & { builtin: "name" | "host" | "tags" | "unknown" | null };

type ParsedToken = {
  variable: ParsedVariable;
};

const TOKEN_PATTERN = /\{\{\s*([^{}]*?)\s*\}\}/g;
const NAME_PATTERN = /^[\p{L}_][\p{L}\p{N}_.-]*$/u;

function builtinKind(name: string): ParsedVariable["builtin"] {
  const normalized = name.toLowerCase();
  if (!normalized.startsWith("asset.")) return null;
  if (normalized === "asset.name") return "name";
  if (normalized === "asset.host") return "host";
  if (normalized === "asset.tags") return "tags";
  return "unknown";
}

function parseToken(source: string): ParsedToken | null {
  const sections = source.split("|");
  const head = sections[0]?.trim() ?? "";
  const equals = head.indexOf("=");
  const name = (equals >= 0 ? head.slice(0, equals) : head).trim();
  if (!NAME_PATTERN.test(name)) return null;

  let defaultValue = equals >= 0 ? head.slice(equals + 1).trim() : undefined;
  let required = true;
  let multiline = false;
  for (const section of sections.slice(1)) {
    const option = section.trim();
    if (option === "required") {
      required = true;
    } else if (option === "optional") {
      required = false;
    } else if (option === "multiline") {
      multiline = true;
    } else if (option.startsWith("default=")) {
      if (defaultValue !== undefined) throw new Error(`变量「${name}」重复定义默认值`);
      defaultValue = option.slice("default=".length).trim();
    } else {
      throw new Error(`变量「${name}」包含未知选项「${option}」`);
    }
  }

  const builtin = builtinKind(name);
  if (builtin && (defaultValue !== undefined || sections.length > 1)) {
    throw new Error(`内建变量「${name}」不支持默认值或选项`);
  }
  return { variable: { name, required, defaultValue, multiline, builtin } };
}

function parseTemplate(template: string): { tokens: ParsedToken[]; variables: SnippetVariable[]; unknownVariables: string[] } {
  const tokens: ParsedToken[] = [];
  const variables = new Map<string, SnippetVariable>();
  const unknownVariables = new Set<string>();
  for (const match of template.matchAll(TOKEN_PATTERN)) {
    const token = parseToken(match[1] ?? "");
    if (!token) continue;
    tokens.push(token);
    const { variable } = token;
    if (variable.builtin === "unknown") {
      unknownVariables.add(variable.name);
    } else if (variable.builtin === null) {
      const existing = variables.get(variable.name);
      if (existing) {
        if (
          existing.required !== variable.required ||
          existing.defaultValue !== variable.defaultValue
        ) {
          throw new Error(`变量「${variable.name}」的必填或默认值定义不一致`);
        }
        existing.multiline ||= variable.multiline;
      } else {
        variables.set(variable.name, {
          name: variable.name,
          required: variable.required,
          defaultValue: variable.defaultValue,
          multiline: variable.multiline,
        });
      }
    }
  }
  return { tokens, variables: [...variables.values()], unknownVariables: [...unknownVariables] };
}

export function analyzeSnippetTemplate(template: string): SnippetTemplateAnalysis {
  const parsed = parseTemplate(template);
  return {
    hasVariables: parsed.tokens.length > 0,
    usesAsset: parsed.tokens.some((token) => token.variable.builtin !== null),
    variables: parsed.variables,
    unknownVariables: parsed.unknownVariables,
  };
}

function assetValue(variable: ParsedVariable, asset: SnippetAsset | undefined): string {
  if (!asset) throw new Error(`缺少目标资产信息，无法解析内建变量「${variable.name}」`);
  if (variable.builtin === "name") return asset.name;
  if (variable.builtin === "host") {
    if (!asset.host) throw new Error(`目标资产缺少主机地址，无法解析内建变量「${variable.name}」`);
    return asset.host;
  }
  if (variable.builtin === "tags") return assetTagValues(asset.tags).join(", ");
  throw new Error(`未知变量「${variable.name}」`);
}

function customValue(variable: ParsedVariable, values: SnippetVariableValues): string {
  const provided = Object.prototype.hasOwnProperty.call(values, variable.name);
  const value = provided ? values[variable.name] : variable.defaultValue;
  if (value === undefined) {
    if (variable.required) throw new Error(`缺少必填变量「${variable.name}」`);
    return "";
  }
  if (variable.required && value.length === 0) throw new Error(`必填变量「${variable.name}」不能为空`);
  if (!variable.multiline && /[\r\n]/.test(value)) {
    throw new Error(`变量「${variable.name}」是单行变量，值不能包含换行`);
  }
  return value;
}

export function renderSnippetTemplate(
  template: string,
  values: SnippetVariableValues = {},
  asset?: SnippetAsset,
): string {
  const parsed = parseTemplate(template);
  if (parsed.tokens.length === 0) return template;
  if (parsed.unknownVariables.length > 0) {
    throw new Error(`未知变量：${parsed.unknownVariables.map((name) => `「${name}」`).join("、")}`);
  }
  const definitions = new Map(parsed.variables.map((variable) => [variable.name, variable]));
  return template.replace(TOKEN_PATTERN, (raw, source: string) => {
    const token = parseToken(source);
    if (!token) return raw;
    const { variable } = token;
    if (variable.builtin) return assetValue(variable, asset);
    const definition = definitions.get(variable.name);
    if (!definition) throw new Error(`未知变量「${variable.name}」`);
    return customValue({ ...variable, ...definition, builtin: null }, values);
  });
}

export function renderSnippetForTargets(
  template: string,
  values: SnippetVariableValues,
  targets: readonly SnippetRenderTarget[],
): RenderedSnippetTarget[] {
  return targets.map((target) => {
    try {
      return { id: target.id, command: renderSnippetTemplate(template, values, target.asset) };
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause);
      throw new Error(`目标「${target.label ?? target.id}」：${message}`);
    }
  });
}
