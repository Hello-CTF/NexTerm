import type { ModelProfile, ModelProfilesView } from "../../ipc/commands";

export function selectModelProfileId(
  view: ModelProfilesView,
  preferredId?: string,
): string | null {
  const candidates = [preferredId, view.activeId, view.profiles[0]?.id];
  for (const candidate of candidates) {
    if (candidate && view.profiles.some((profile) => profile.id === candidate)) return candidate;
  }
  return null;
}

/**
 * 回退模型输入框 → 档案字段。
 *
 * 只有两种落法：非空 = 设置；空 = **明确清除**（null）。
 * 「没碰过这个框」不经过这里 —— 草稿里读到的原值原样保留，
 * 所以保存无关字段不会顺带清掉已设的回退模型。
 */
export function fallbackModelFromInput(raw: string): string | null {
  const trimmed = raw.trim();
  return trimmed ? trimmed : null;
}

/** 展示层统一空值口径：undefined（旧数据没有这项）与 null 都视为「未设置」。 */
export function fallbackModelLabel(profile: ModelProfile): string {
  return profile.fallbackModel ?? "";
}

/** 两份档案在「可编辑字段」上是否等价（用于判断有没有未保存的改动）。 */
export function sameModelProfile(a: ModelProfile, b: ModelProfile): boolean {
  return (
    a.name === b.name &&
    a.baseUrl === b.baseUrl &&
    a.apiKey === b.apiKey &&
    a.model === b.model &&
    a.temperature === b.temperature &&
    a.contextWindow === b.contextWindow &&
    (a.proxy ?? "") === (b.proxy ?? "") &&
    a.stream === b.stream &&
    // 回退模型也是可编辑字段：只改它同样算未保存改动；
    // undefined 与 null 都是「未设置」，不算差异。
    fallbackModelLabel(a) === fallbackModelLabel(b)
  );
}
