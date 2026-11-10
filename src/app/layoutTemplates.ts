import { useSyncExternalStore } from "react";
import { createSyncedPreference } from "./syncedPreference";
import { workspaceLayoutTree } from "./paneLayout";
import {
  clampRatio,
  defaultIdFactory,
  mintUnique,
  SPLIT_RATIO_MAX,
  SPLIT_RATIO_MIN,
  SPLIT_TREE_MAX_DEPTH,
  type SplitDir,
  type SplitNode,
} from "./splitTree";
import type { Workspace } from "./store";
import { t } from "../i18n";

// A layout template is a named split tree for one host: every pane opens a terminal on the template's asset and may
// run a snippet once that terminal is ready. Templates reference the asset and snippets by id, never live sessions.
// A workspace holds the terminals of one host, so the host belongs to the template rather than to each pane.

export type LayoutTemplateNode =
  | { type: "leaf"; snippetId?: string }
  | { type: "split"; dir: SplitDir; ratio: number; a: LayoutTemplateNode; b: LayoutTemplateNode };

export interface LayoutTemplate {
  id: string;
  name: string;
  description?: string;
  /** An asset icon name (see ASSET_ICON_NAMES). */
  icon?: string;
  assetId: string;
  layout: LayoutTemplateNode;
}

// The same limits internal/account/layout_templates.go enforces for workspace.layoutTemplates.
export const LAYOUT_TEMPLATE_LIMITS = {
  count: 30,
  panes: 16,
  depth: SPLIT_TREE_MAX_DEPTH,
  nameChars: 64,
  descriptionChars: 200,
  totalBytes: 48 * 1024,
} as const;

export const LAYOUT_TEMPLATES_PREFERENCE_KEY = "workspace.layoutTemplates";
const STORAGE_KEY = "nexterm.layoutTemplates.v1";

const encoder = new TextEncoder();
const CONTROL_CHARS = /[\u0000-\u001f\u007f-\u009f]/;
const ID_PATTERN = /^[A-Za-z0-9_-]{1,64}$/;
const REF_PATTERN = /^[A-Za-z0-9_.:-]{1,128}$/;
const ICON_PATTERN = /^[A-Za-z][A-Za-z0-9]{0,47}$/;

/** Panes of a template layout in visual reading order (left to right, top to bottom). */
export function layoutTemplatePanes(node: LayoutTemplateNode): { snippetId?: string }[] {
  if (node.type === "leaf") return [node];
  return [...layoutTemplatePanes(node.a), ...layoutTemplatePanes(node.b)];
}

function layoutNodeError(node: unknown, depth: number, panes: { count: number }): string | null {
  if (!node || typeof node !== "object") return t("布局结构无效");
  const raw = node as Record<string, unknown>;
  if (raw.type === "leaf") {
    panes.count += 1;
    if (panes.count > LAYOUT_TEMPLATE_LIMITS.panes) return t("模板最多 {panes} 个窗格", { panes: LAYOUT_TEMPLATE_LIMITS.panes });
    if (Object.keys(raw).some((key) => key !== "type" && key !== "snippetId")) return t("布局结构无效");
    if (raw.snippetId !== undefined && (typeof raw.snippetId !== "string" || !REF_PATTERN.test(raw.snippetId))) return t("启动片段无效");
    return null;
  }
  if (raw.type !== "split") return t("布局结构无效");
  if (depth >= LAYOUT_TEMPLATE_LIMITS.depth) return t("分屏最多嵌套 {depth} 层", { depth: LAYOUT_TEMPLATE_LIMITS.depth });
  if (Object.keys(raw).some((key) => !["type", "dir", "ratio", "a", "b"].includes(key))) return t("布局结构无效");
  if (raw.dir !== "row" && raw.dir !== "col") return t("布局结构无效");
  if (typeof raw.ratio !== "number" || !(raw.ratio >= SPLIT_RATIO_MIN && raw.ratio <= SPLIT_RATIO_MAX)) return t("布局结构无效");
  return layoutNodeError(raw.a, depth + 1, panes) ?? layoutNodeError(raw.b, depth + 1, panes);
}

/** Returns why a template cannot be saved, or null when it is valid. */
export function layoutTemplateError(template: LayoutTemplate): string | null {
  if (!ID_PATTERN.test(template.id)) return t("模板标识无效");
  const name = template.name;
  if (!name.trim()) return t("请填写名称");
  if ([...name].length > LAYOUT_TEMPLATE_LIMITS.nameChars) return t("名称最多 {nameChars} 个字符", { nameChars: LAYOUT_TEMPLATE_LIMITS.nameChars });
  if (CONTROL_CHARS.test(name)) return t("名称不能包含换行或控制字符");
  const description = template.description ?? "";
  if ([...description].length > LAYOUT_TEMPLATE_LIMITS.descriptionChars) {
    return t("说明最多 {descriptionChars} 个字符", { descriptionChars: LAYOUT_TEMPLATE_LIMITS.descriptionChars });
  }
  if (CONTROL_CHARS.test(description)) return t("说明不能包含换行或控制字符");
  if (template.icon !== undefined && !ICON_PATTERN.test(template.icon)) return t("图标无效");
  if (!REF_PATTERN.test(template.assetId)) return t("模板未关联主机");
  return layoutNodeError(template.layout, 0, { count: 0 });
}

/** Returns why the whole list cannot be saved, or null when it is valid. */
export function layoutTemplateListError(templates: readonly LayoutTemplate[]): string | null {
  if (templates.length > LAYOUT_TEMPLATE_LIMITS.count) return t("最多保存 {count} 个布局模板", { count: LAYOUT_TEMPLATE_LIMITS.count });
  const ids = new Set<string>();
  for (const template of templates) {
    const error = layoutTemplateError(template);
    if (error) return `「${template.name}」：${error}`;
    if (ids.has(template.id)) return t("模板标识重复");
    ids.add(template.id);
  }
  if (encoder.encode(JSON.stringify(templates)).length > LAYOUT_TEMPLATE_LIMITS.totalBytes) {
    return t("布局模板总大小超过 48 KiB，请删减内容");
  }
  return null;
}

function cleanNode(node: LayoutTemplateNode): LayoutTemplateNode {
  if (node.type === "leaf") return node.snippetId ? { type: "leaf", snippetId: node.snippetId } : { type: "leaf" };
  return { type: "split", dir: node.dir, ratio: node.ratio, a: cleanNode(node.a), b: cleanNode(node.b) };
}

/** A copy with only the stored fields, in the order the backend writes them. */
export function cleanLayoutTemplate(template: LayoutTemplate): LayoutTemplate {
  return {
    id: template.id,
    name: template.name,
    ...(template.description ? { description: template.description } : {}),
    ...(template.icon ? { icon: template.icon } : {}),
    assetId: template.assetId,
    layout: cleanNode(template.layout),
  };
}

/** Keeps the valid templates of a stored list; null when the value is not a list. */
export function parseLayoutTemplates(value: unknown): LayoutTemplate[] | null {
  if (!Array.isArray(value)) return null;
  const templates: LayoutTemplate[] = [];
  const ids = new Set<string>();
  for (const entry of value) {
    if (!entry || typeof entry !== "object") continue;
    const { id, name, description, icon, assetId, layout } = entry as Record<string, unknown>;
    if (typeof id !== "string" || typeof name !== "string" || typeof assetId !== "string") continue;
    if (description !== undefined && typeof description !== "string") continue;
    if (icon !== undefined && typeof icon !== "string") continue;
    const candidate = { id, name, description, icon, assetId, layout } as LayoutTemplate;
    if (ids.has(id) || layoutTemplateError(candidate) !== null) continue;
    ids.add(id);
    templates.push(cleanLayoutTemplate(candidate));
    if (templates.length === LAYOUT_TEMPLATE_LIMITS.count) break;
  }
  return templates;
}

export function newLayoutTemplateId(): string {
  const random = Math.random().toString(36).slice(2, 10);
  return `lt-${Date.now().toString(36)}-${random}`;
}

// Ratios are stored with four decimals: enough to restore a dragged divider, short enough to keep templates small.
function storedRatio(ratio: number): number {
  return Math.round(clampRatio(ratio) * 10_000) / 10_000;
}

function nodeFromTree(node: SplitNode): LayoutTemplateNode {
  if (node.type === "leaf") return { type: "leaf" };
  return { type: "split", dir: node.dir, ratio: storedRatio(node.ratio), a: nodeFromTree(node.a), b: nodeFromTree(node.b) };
}

export type LayoutCapture = { template: LayoutTemplate } | { error: string };

/** Asset kinds whose workspace holds terminals, so a template can open them again. */
export const LAYOUT_TEMPLATE_ASSET_KINDS: ReadonlySet<string> = new Set(["ssh", "local", "winrm"]);

/** Whether the workspace is a saved terminal host, the only kind a template can capture. */
export function canSaveLayoutTemplate(workspace: Pick<Workspace, "kind" | "assetId" | "assetKind">): boolean {
  return workspace.kind === "session" && Boolean(workspace.assetId) && LAYOUT_TEMPLATE_ASSET_KINDS.has(workspace.assetKind ?? "ssh");
}

/**
 * Captures the split tree shape and ratios of a host workspace as a template. Only the host is recorded; open tabs,
 * live sessions and terminal ids are not, and every pane of the template opens a terminal.
 */
export function captureLayoutTemplate(workspace: Workspace, name: string, id = newLayoutTemplateId()): LayoutCapture {
  if (workspace.kind === "session" && !workspace.assetId) return { error: t("临时连接没有保存为资产，无法保存为布局模板") };
  if (!canSaveLayoutTemplate(workspace) || !workspace.assetId) return { error: t("只有主机工作区可以保存为布局模板") };
  const tree = workspaceLayoutTree(workspace);
  if (!tree) return { error: t("工作区没有可保存的窗格") };
  const template: LayoutTemplate = { id, name: name.trim(), assetId: workspace.assetId, layout: nodeFromTree(tree) };
  const error = layoutTemplateError(template);
  return error ? { error } : { template };
}

/** Returns the layout with the snippet of pane `index` (reading order) replaced; undefined clears it. */
export function withPaneSnippet(node: LayoutTemplateNode, index: number, snippetId: string | undefined): LayoutTemplateNode {
  let next = 0;
  const walk = (current: LayoutTemplateNode): LayoutTemplateNode => {
    if (current.type === "leaf") {
      const own = next;
      next += 1;
      if (own !== index) return current;
      return snippetId ? { type: "leaf", snippetId } : { type: "leaf" };
    }
    return { ...current, a: walk(current.a), b: walk(current.b) };
  };
  return walk(node);
}

/**
 * Builds a workspace split tree from a template layout; `paneIds` are the new panes in reading order, which is also
 * the order of layoutTemplatePanes.
 */
export function workspaceTreeFromTemplate(node: LayoutTemplateNode, paneIds: readonly string[]): SplitNode {
  const used = new Set<string>();
  let next = 0;
  const walk = (current: LayoutTemplateNode, at: string): SplitNode => {
    if (current.type === "leaf") {
      const paneId = paneIds[next];
      next += 1;
      return { type: "leaf", id: mintUnique(used, `leaf:${paneId}`, defaultIdFactory), paneId };
    }
    const id = mintUnique(used, `split:${at}:${paneIds.join(",")}`, defaultIdFactory);
    const a = walk(current.a, `${at}.a`);
    const b = walk(current.b, `${at}.b`);
    return { type: "split", id, dir: current.dir, ratio: clampRatio(current.ratio), a, b };
  };
  return walk(node, "root");
}

const layoutTemplates = createSyncedPreference<LayoutTemplate[]>({
  key: LAYOUT_TEMPLATES_PREFERENCE_KEY,
  localKey: STORAGE_KEY,
  parse: parseLayoutTemplates,
  fallback: () => [],
});

export function getLayoutTemplates(): LayoutTemplate[] {
  return layoutTemplates.get();
}

export function useLayoutTemplates(): LayoutTemplate[] {
  return useSyncExternalStore(layoutTemplates.subscribe, layoutTemplates.get);
}

/** Saves the list to the account (or the device without one); rejects an invalid list or a failed account write. */
export async function saveLayoutTemplates(templates: readonly LayoutTemplate[]): Promise<void> {
  const error = layoutTemplateListError(templates);
  if (error) throw new Error(error);
  await layoutTemplates.set(templates.map(cleanLayoutTemplate));
}

export async function addLayoutTemplate(template: LayoutTemplate): Promise<void> {
  await saveLayoutTemplates([...getLayoutTemplates(), template]);
}

export async function updateLayoutTemplate(template: LayoutTemplate): Promise<void> {
  const current = getLayoutTemplates();
  if (!current.some((entry) => entry.id === template.id)) throw new Error(t("布局模板已不存在"));
  await saveLayoutTemplates(current.map((entry) => (entry.id === template.id ? template : entry)));
}

export async function deleteLayoutTemplate(id: string): Promise<void> {
  await saveLayoutTemplates(getLayoutTemplates().filter((entry) => entry.id !== id));
}
