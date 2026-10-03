// rename / chmod / checksum 三个文件操作的用户入口逻辑（M60）。
//
// 为什么值得一个 hook：FileTree 与 FileBrowser 两处入口必须走**完全同一套**
// 预检、提醒与错误呈现 —— 任何一边多一句提示或少一道确认，同一个操作在两个
// 界面里就是两个行为。
//
// 边界约定：
//   · 只调用既有 fs RPC facade（fsApi.rename / chmod / checksum），不加新后端命令；
//   · 前端只做输入形状预检与后果告知；存在性、覆盖、权限、算法是否被接受最终
//     由后端裁决，后端拒绝原样呈现 —— 前端状态绝不替代后端授权；
//   · 单 flight：一个操作从弹窗到 RPC 全程占用 busy，后来的触发直接挡住
//     （防 double-submit）；组件卸载后到达的完成回调不再碰任何状态
//     （防 stale completion / unmount 后 setState）；
//   · 操作不碰 editorGuards 的 dirty 集合与传输进度 —— 别人的未保存修改和
//     正在进行的传输原样保留。
//
// listKey 契约（R1 评审 P1/P2）：条目所在列表的**缓存键**必须由调用方传入 ——
// 真实后端 list `~` 时把 `~` 展开成绝对路径返回 entry.path，而前端查询键保留
// `~`/`~/sub` 形态；用 parentOf(entry.path) 反推键在根层恒失配（覆盖确认被跳过、
// 失效打不中可见列表）。调用方的键（FileTree 行的 dir / FileBrowser 当前 path）
// 与建查询时用的是同一串，天然兼容 `~`、绝对路径与 Windows 反斜杠。
import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { fsApi } from "../../ipc/commands";
import { ask, askChoice, promptText } from "../../ui/dialogs";
import { useUi, type AppTab } from "../../app/store";
import { describeError } from "../../ui/errorText";
import type { FileEntryDto } from "../../ipc/types";
import { isDirtyFileEditor } from "./editorGuards";
import { joinPath, norm, parentOf } from "./pathUtils";
import {
  CHECKSUM_ALGOS,
  DEFAULT_CHECKSUM_ALGO,
  findSibling,
  formatMode,
  losesOwnerAccess,
  octalDefaultFromEntry,
  parseOctalMode,
  validateEntryName,
} from "./fileOps";

export type FileOpKind = "rename" | "chmod" | "checksum";

const OP_LABEL: Record<FileOpKind, string> = {
  rename: "重命名",
  chmod: "修改权限",
  checksum: "计算校验值",
};

/**
 * 该会话里受重命名影响的文件编辑标签。
 *
 * 目录要按前缀匹配后代（`~/sub` 改名会让 `~/sub/a.txt` 的标签指向旧路径）；
 * 两侧统一 norm —— 路径形态（正/反斜杠）不该影响匹配结果。
 */
function editorTabsOf(sessionId: string, path: string, kind: string): AppTab[] {
  const target = norm(path);
  const hit = (p: string) => {
    const pn = norm(p);
    return kind === "dir" ? pn === target || pn.startsWith(`${target}/`) : pn === target;
  };
  const out: AppTab[] = [];
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind === "files" && t.sessionId === sessionId && t.path && hit(t.path)) {
          out.push(t);
        }
      }
    }
  }
  return out;
}

export function useFileOps(sessionId: string) {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  /** 渲染用（菜单项据此禁用）；判定用 ref，避免两次触发挤在同一帧里漏判。 */
  const [busy, setBusy] = useState<{ op: FileOpKind; path: string } | null>(null);
  const busyRef = useRef<{ op: FileOpKind; path: string } | null>(null);
  const mountedRef = useRef(true);
  const seqRef = useRef(0);

  // setup 必须显式重置 true：StrictMode（main.tsx 全 App 启用）在开发模式会
  // 重放 setup→cleanup→setup，只靠 useRef 初值的话 mountedRef 会永远停在 false，
  // 三个操作都会在首个 await 后静默中止。
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      // 作废所有在途操作：它们随后的完成回调会因序号对不上而静默退出。
      seqRef.current++;
    };
  }, []);

  /** 还活着吗：组件没卸载，且这是最新一次操作（防 stale completion）。 */
  const alive = (seq: number) => mountedRef.current && seqRef.current === seq;

  const begin = (op: FileOpKind, path: string): number | null => {
    if (busyRef.current) {
      pushToast("info", `上一个文件操作（${OP_LABEL[busyRef.current.op]}）还在进行，请等它完成`);
      return null;
    }
    busyRef.current = { op, path };
    setBusy(busyRef.current);
    return ++seqRef.current;
  };

  const end = (seq: number) => {
    if (seqRef.current !== seq) return;
    busyRef.current = null;
    if (mountedRef.current) setBusy(null);
  };

  /**
   * 成功后刷新受影响的目录缓存。
   *
   * · `listKey`（调用方传入）：条目实际所在列表的键 —— 改名后列表里的名字靠它刷新；
   * · `from`：被改名目录自己的旧键作废。注意 react-query 的 queryKey 匹配没有
   *   字符串前缀语义，后代目录**不是**被这里失效的 —— FileTree 把展开态 remap 到
   *   新路径后，新键无缓存会自动拉取。
   */
  const invalidateAfterRename = (listKey: string, from: string) => {
    void qc.invalidateQueries({ queryKey: ["fs", sessionId, listKey] });
    void qc.invalidateQueries({ queryKey: ["fs", sessionId, from] });
  };

  /**
   * 重命名：只改同目录下的名字，不做移动。
   *
   * `listKey` 是条目所在列表的缓存键（见文件头的 listKey 契约）；`siblings` 是
   * 该键对应的列表，两者都由调用方给出，不做任何路径反推。
   *
   * 三道前端关卡（都只是预检/告知，裁决权在后端）：
   *   1. 符号链接 → 提醒「改的是链接本身，不是它指向的目标」；
   *   2. 编辑器里开着且有未保存修改 → 提醒「标签仍指向旧路径，未保存内容会写到旧文件」；
   *   3. 新名字与同目录现有项冲突 → 显式确认「将请求后端覆盖，能否覆盖由后端决定」。
   */
  const renameEntry = async (
    entry: FileEntryDto,
    siblings: FileEntryDto[],
    listKey: string,
    onRenamed?: (from: string, to: string) => void,
  ) => {
    const seq = begin("rename", entry.path);
    if (seq === null) return;
    try {
      const notes: string[] = [];
      if (entry.kind === "symlink") {
        notes.push(
          `「${entry.name}」是符号链接（→ ${entry.symlinkTarget ?? "未知目标"}）。重命名只改链接本身的名字，不影响它指向的目标。`,
        );
      }
      const tabs = editorTabsOf(sessionId, entry.path, entry.kind);
      const dirty = tabs.some(isDirtyFileEditor);
      if (dirty) {
        notes.push(
          entry.kind === "dir"
            ? "该目录（含其子目录）内有文件正在编辑器中打开且有未保存的修改。重命名后，已打开的标签仍指向旧路径，未保存的内容仍会写到旧文件。"
            : "该文件正在编辑器中打开且有未保存的修改。重命名后，已打开的标签仍指向旧路径，未保存的内容仍会写到旧文件。",
        );
      }
      if (notes.length) {
        const go = await ask(`${notes.join("\n\n")}\n\n继续重命名？`, {
          title: "重命名",
          kind: "warning",
        });
        if (!go || !alive(seq)) return;
      }
      const openNote = tabs.length && !dirty ? "（编辑器中已打开；重命名后标签仍指向旧路径）" : "";
      const name = await promptText(`重命名 ${entry.path}${openNote}`, entry.name);
      if (name === null || !alive(seq)) return;
      const problem = validateEntryName(name);
      if (problem) {
        pushToast("error", `无法重命名：${problem}`);
        return;
      }
      if (name === entry.name) return;
      const to = joinPath(parentOf(entry.path) ?? "", name);
      const conflict = findSibling(siblings, name);
      if (conflict) {
        const go = await ask(
          `「${name}」已存在（${conflict.kind === "dir" ? "目录" : "文件"}）。继续将请求后端用重命名后的项替换已存在的「${name}」—— 旧「${name}」的内容将丢失；能否覆盖由后端最终决定，部分后端（如 SFTP）会拒绝。\n\n替换「${name}」？`,
          { title: "覆盖确认", kind: "warning" },
        );
        if (!go || !alive(seq)) return;
      }
      await fsApi.rename(sessionId, entry.path, to);
      if (!alive(seq)) return;
      invalidateAfterRename(listKey, entry.path);
      onRenamed?.(entry.path, to);
      pushToast("success", `已重命名：${entry.name} → ${name}`);
    } catch (e) {
      if (alive(seq)) pushToast("error", `重命名失败：${describeError(e)}`);
    } finally {
      end(seq);
    }
  };

  /**
   * 改权限（chmod）：八进制 000–777。
   *
   * `listKey` 是条目所在列表的缓存键（见文件头的 listKey 契约）——mode 展示
   * 靠刷新这个键更新。
   *
   * · 符号链接 → chmod 跟随链接改的是**目标**，先讲清楚；
   * · 新权限移除所有者的读/写 → 危险确认（可能自我锁死）；
   * · Windows 类目标 → 后端报「不支持」，原样呈现，前端不预先藏入口
   *   （入口可见性不是授权，且平台判定只有后端说了算）。
   */
  const chmodEntry = async (entry: FileEntryDto, listKey: string) => {
    const seq = begin("chmod", entry.path);
    if (seq === null) return;
    try {
      if (entry.kind === "symlink") {
        const go = await ask(
          `「${entry.name}」是符号链接（→ ${entry.symlinkTarget ?? "未知目标"}）。chmod 会跟随链接，修改它指向的**目标**的权限。\n\n继续？`,
          { title: "权限", kind: "warning" },
        );
        if (!go || !alive(seq)) return;
      }
      const current = octalDefaultFromEntry(entry.mode);
      const input = await promptText(
        `权限（八进制 000–777，当前 ${entry.mode || "未知"}）· ${entry.path}`,
        current,
      );
      if (input === null || !alive(seq)) return;
      const parsed = parseOctalMode(input);
      if (!parsed.ok) {
        pushToast("error", `无法修改权限：${parsed.reason}`);
        return;
      }
      const next = parsed.mode;
      const cur = current ? parseOctalMode(current) : null;
      const curMode = cur && cur.ok ? cur.mode : null;
      const danger = curMode !== null && losesOwnerAccess(curMode, next);
      const confirmText = `把 ${entry.path} 的权限从 ${
        curMode !== null ? formatMode(curMode) : (entry.mode || "未知")
      } 改为 ${formatMode(next)}？${
        danger ? "\n\n⚠️ 新权限移除了所有者的读取或写入，之后可能无法再打开或保存它。" : ""
      }`;
      const go = await ask(confirmText, { title: "权限", kind: danger ? "warning" : "info" });
      if (!go || !alive(seq)) return;
      await fsApi.chmod(sessionId, entry.path, next);
      if (!alive(seq)) return;
      void qc.invalidateQueries({ queryKey: ["fs", sessionId, listKey] });
      pushToast("success", `权限已更新：${entry.path} → ${formatMode(next)}`);
    } catch (e) {
      if (alive(seq)) pushToast("error", `修改权限失败：${describeError(e)}`);
    } finally {
      end(seq);
    }
  };

  /**
   * 计算校验值：md5 / sha256（与后端支持的算法对齐，缺省 sha256）。
   *
   * 结果用 promptText 的单行输入框呈现：打开即全选，Cmd/Ctrl+C 直接复制 ——
   * 比「看一眼就关」的消息框实用，也没有自动写剪贴板的副作用。
   */
  const checksumEntry = async (entry: FileEntryDto) => {
    const seq = begin("checksum", entry.path);
    if (seq === null) return;
    try {
      const algo = await askChoice(`计算 ${entry.path} 的校验值：`, {
        title: "校验",
        level: "info",
        choices: CHECKSUM_ALGOS.map((a) => ({
          key: a.key,
          label: a.label,
          hint: a.hint,
          primary: a.key === DEFAULT_CHECKSUM_ALGO,
        })),
      });
      if (algo === null || !alive(seq)) return;
      const label = CHECKSUM_ALGOS.find((a) => a.key === algo)?.label ?? algo;
      pushToast("info", `正在计算 ${label}…`);
      const hash = await fsApi.checksum(sessionId, entry.path, algo);
      if (!alive(seq)) return;
      await promptText(`${label} · ${entry.path}（已全选，可直接复制）`, hash, {
        multiLine: false,
      });
      if (!alive(seq)) return;
      pushToast("success", `校验完成：${entry.name}`);
    } catch (e) {
      if (alive(seq)) pushToast("error", `计算校验值失败：${describeError(e)}`);
    } finally {
      end(seq);
    }
  };

  return { busy, renameEntry, chmodEntry, checksumEntry };
}
