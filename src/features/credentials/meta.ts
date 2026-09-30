// 凭据的类型元信息 + 时间格式化。
//
// 左栏列表、详情页、文本视图三处都要「类型图标 + 中文名 + 时间」，共用这一份
// （旧版把 KIND_LABEL 内联在面板里，再加第二处调用点就必然出现两套文案）。
import { IconCode, IconKey, IconLock, IconShieldCheck } from "../../ui/icons";

type Icon = typeof IconKey;

export interface KindMeta {
  label: string;
  Icon: Icon;
  /** 图标色（画在列表左侧的方块里）。 */
  tone: string;
  /** 方块底色。 */
  bg: string;
}

/**
 * 四种凭据类型。
 *
 * 着意克制：全站只有一支强调蓝，类型区分靠**图标形状 + 一点点紫**，
 * 不给每种类型各配一个饱和色 —— 那样列表会变成调色盘，反而看不出重点。
 */
export const KIND_META: Record<string, KindMeta> = {
  password: { label: "密码", Icon: IconLock, tone: "text-blue-400", bg: "bg-blue-500/12" },
  private_key: { label: "私钥", Icon: IconKey, tone: "text-purple-400", bg: "bg-purple-500/12" },
  api_key: { label: "API Key", Icon: IconCode, tone: "text-neutral-400", bg: "bg-white/[.06]" },
  // 旧版的独立口令凭据：新建入口已移除（口令改为跟私钥绑定），但库里可能还留着，
  // 列表与详情仍要能显示它 —— 所以留在映射表里，只是不出现在「新建」的类型选项里。
  passphrase: {
    label: "私钥口令（旧）",
    Icon: IconShieldCheck,
    tone: "text-purple-300",
    bg: "bg-purple-500/10",
  },
};

/**
 * 「新建」时可选的类型。
 *
 * 不含 `passphrase`：口令是私钥的一个属性（在私钥表单里作为选填项出现），
 * 不再是可以单独创建的凭据 —— 旧版那样会让列表里多出一条看不出属于哪把钥匙的记录。
 */
export const NEW_KIND_ORDER = ["password", "private_key", "api_key"];

/** 未知类型不猜、不隐藏：原样显示，图标退回钥匙。 */
export function kindMeta(kind: string): KindMeta {
  return (
    KIND_META[kind] ?? {
      label: kind,
      Icon: IconKey,
      tone: "text-neutral-400",
      bg: "bg-white/[.06]",
    }
  );
}

/** 类型筛选条用的顺序（与 KIND_META 声明顺序一致）。 */
export const KIND_ORDER = Object.keys(KIND_META);

const pad = (n: number) => String(n).padStart(2, "0");

/** 详情页用的绝对时间（精确到分钟：凭据的创建/修改不需要秒）。 */
export function formatTime(ms: number): string {
  const d = new Date(ms);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(
    d.getMinutes(),
  )}`;
}
