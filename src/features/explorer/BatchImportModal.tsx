// 批量导入资产（粘贴 → 预览逐行结论 → 落库）。
//
// 解决的是真实场景：攻防/巡检时手头有一份"主机 + 账号 + 密码"的清单，
// 现在是逐台点「新建资产」填三遍。这里把清单一次吃进来。
//
// 两条输入格式：
//   ① 导出格式（与 ExportAssetsModal 的 buildExportLine 闭环）：
//        host=10.78.78.82 port=22 user=yun authType=password password=111111 title=web-01 note="..."
//   ② 极简兜底（手抄常见）：
//        yun@10.78.78.82:22   /   10.78.78.82:22   （密码可缺省）
//
// 三条刻意的边界：
// · 解析器**不猜**：认不出来的行明确标「格式不识别」跳过，绝不塞一条似是而非的资产；
// · 「已存在」按 host + port + username 三元组判定（host 归一为小写、全部 trim），
//   默认跳过并在预览里逐行标出，想要强行添加有开关；
// · 同一条密码只建**一条**凭据（200 台同密码 = 1 条），命名「导入批次 YYYY-MM-DD」，
//   同名批次自动加序号。无密码的行不建凭据。
import { useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { assetApi, vaultApi, type Asset, type Credential } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { useVaultUnlock } from "../credentials/useVaultUnlock";
import { IconAlert, IconCheckCircle, IconClose, IconUpload } from "../../ui/icons";

/** 一行解析结果的状态。`new` 是唯一会落库的（force 打开时 `dup` 也会）。 */
type RowStatus = "new" | "dup" | "unrecognized" | "error";

interface ParsedRow {
  lineNo: number;
  raw: string;
  status: RowStatus;
  /** `dup` 时是撞上的现有资产名；`unrecognized`/`error` 时是原因。 */
  detail?: string;
  name: string;
  host: string;
  port: number | null;
  username: string;
  authKind: string;
  password: string;
  note: string;
}

/** host 归一：去空白 + 转小写（域名/主机名大小写不敏感，避免把 Web-01 与 web-01 当两台）。 */
function normHost(h: string): string {
  return h.trim().toLowerCase();
}

/** 三元组判重键。username 只 trim 不改大小写 —— Linux 用户名是区分大小写的。 */
function dedupKey(host: string, port: number | null, username: string): string {
  return `${normHost(host)}|${port ?? ""}|${username.trim()}`;
}

/**
 * 把一行拆成 `key=value` 词表。
 *
 * 值可被双引号包裹（导出格式对含空白的值就是这么写的），引号内的空格不算分隔。
 * 行里出现**词表之外**的残留（两个 token 之间夹了非空白、或末尾有剩料）就判为
 * 不是合法的 key=value 行 —— 返回 null，交给极简格式或直接判不识别。
 */
function tokenizeKv(line: string): Record<string, string> | null {
  const re = /([A-Za-z][A-Za-z0-9_]*)\s*=\s*("(?:[^"\\]|\\.)*"|\S+)/g;
  const out: Record<string, string> = {};
  let last = 0;
  let count = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(line)) !== null) {
    if (line.slice(last, m.index).trim() !== "") return null;
    let v = m[2];
    if (v.startsWith('"') && v.endsWith('"')) {
      v = v.slice(1, -1).replace(/\\"/g, '"').replace(/\\\\/g, "\\");
    }
    out[m[1]] = v;
    last = m.index + m[0].length;
    count++;
  }
  if (line.slice(last).trim() !== "") return null;
  return count > 0 ? out : null;
}

/** 极简格式：`[user@]host[:port]`。认不出来返回 null。 */
function parseMinimal(
  line: string,
): { host: string; port: number | null; username: string } | null {
  const m = /^(?:([^@\s]+)@)?([^\s@:]+)(?::(\d+))?$/.exec(line.trim());
  if (!m) return null;
  return { username: m[1] ?? "", host: m[2], port: m[3] ? Number(m[3]) : null };
}

/** 解析整段粘贴文本，合并现有资产给出逐行结论。 */
function parsePasted(text: string, existing: Asset[]): ParsedRow[] {
  const seen = new Set<string>();
  for (const a of existing) {
    if (a.host) seen.add(dedupKey(a.host, a.port, a.username ?? ""));
  }
  const existingByKey = new Map<string, string>();
  for (const a of existing) {
    if (a.host) existingByKey.set(dedupKey(a.host, a.port, a.username ?? ""), a.name);
  }

  const rows: ParsedRow[] = [];
  text.split(/\r?\n/).forEach((raw, i) => {
    const lineNo = i + 1;
    const line = raw.trim();
    if (!line || line.startsWith("#")) return;

    let parsed: Omit<ParsedRow, "lineNo" | "raw" | "status" | "detail"> | null = null;
    let fail: { status: RowStatus; detail: string } | null = null;

    if (line.includes("=")) {
      const kv = tokenizeKv(line);
      if (!kv) {
        fail = { status: "unrecognized", detail: "不是合法的 key=value 行" };
      } else if (!kv.host?.trim()) {
        fail = { status: "error", detail: "缺少 host" };
      } else {
        const port = kv.port ? Number(kv.port) : 22;
        if (kv.port && (!Number.isFinite(port) || port <= 0)) {
          fail = { status: "error", detail: `端口不是正整数：${kv.port}` };
        } else {
          parsed = {
            name: (kv.title?.trim() || "").trim() || kv.host.trim(),
            host: kv.host.trim(),
            port,
            username: (kv.user ?? "").trim(),
            authKind: kv.authType?.trim() || "password",
            password: kv.password ?? "",
            note: kv.note ?? "",
          };
        }
      }
    } else {
      const min = parseMinimal(line);
      if (!min) {
        fail = { status: "unrecognized", detail: "既不是 key=value，也不是 user@host:port" };
      } else {
        parsed = {
          name: min.host,
          host: min.host,
          port: min.port ?? 22,
          username: min.username,
          authKind: "password",
          password: "",
          note: "",
        };
      }
    }

    if (fail) {
      rows.push({
        lineNo,
        raw,
        status: fail.status,
        detail: fail.detail,
        name: "",
        host: "",
        port: null,
        username: "",
        authKind: "",
        password: "",
        note: "",
      });
      return;
    }

    const p = parsed!;
    const key = dedupKey(p.host, p.port, p.username);
    if (seen.has(key)) {
      const dupName = existingByKey.get(key);
      rows.push({
        ...p,
        lineNo,
        raw,
        status: "dup",
        detail: dupName ? `已存在「${dupName}」` : "与本批前面某行重复",
      });
      return;
    }
    seen.add(key);
    rows.push({ ...p, lineNo, raw, status: "new" });
  });

  return rows;
}

/** 本地日期 `YYYY-MM-DD`。 */
function todayStr(): string {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

const STATUS_META: Record<RowStatus, { label: string; badge: string }> = {
  new: { label: "新增", badge: "nx-badge nx-badge-green" },
  dup: { label: "跳过（已存在）", badge: "nx-badge" },
  unrecognized: { label: "跳过（格式不识别）", badge: "nx-badge nx-badge-amber" },
  error: { label: "错误", badge: "nx-badge nx-badge-red" },
};

export function BatchImportModal({
  assets,
  credentials,
  onClose,
}: {
  assets: Asset[];
  credentials: Credential[];
  onClose: () => void;
}) {
  const { pushToast } = useUi();
  const qc = useQueryClient();
  const unlock = useVaultUnlock();

  const [stage, setStage] = useState<"input" | "preview" | "result">("input");
  const [text, setText] = useState("");
  const [forceAdd, setForceAdd] = useState(false);
  const [busy, setBusy] = useState(false);

  const rows = useMemo(
    () => (stage === "input" ? [] : parsePasted(text, assets)),
    [stage, text, assets],
  );

  const newCount = rows.filter((r) => r.status === "new").length;
  const dupCount = rows.filter((r) => r.status === "dup").length;
  const toAdd = rows.filter((r) => r.status === "new" || (forceAdd && r.status === "dup"));
  const passwordCount = new Set(toAdd.map((r) => r.password).filter(Boolean)).size;

  const [report, setReport] = useState<{
    okCount: number;
    credCount: number;
    failures: { name: string; reason: string }[];
  } | null>(null);

  const run = async () => {
    if (toAdd.length === 0) return;
    setBusy(true);
    try {
      // 同密码合并：先去重出「本条批次要用到的不同密码集合」。
      const passwords = [...new Set(toAdd.map((r) => r.password).filter(Boolean))];
      const credIdByPassword = new Map<string, string>();

      if (passwords.length > 0) {
        // 写凭据需要库已解锁 —— 锁定态先走既有解锁流程，不绕过。
        const st = await vaultApi.status();
        if (st.initialized && !st.unlocked) {
          const ok = await unlock("导入需要把密码写入凭据库，请先解锁：");
          if (!ok) {
            setBusy(false);
            return;
          }
        }
        const baseName = `导入批次 ${todayStr()}`;
        const taken = new Set(credentials.map((c) => c.name));
        let n = 1;
        const nextName = () => {
          let name = n === 1 ? baseName : `${baseName} #${n}`;
          while (taken.has(name)) {
            n++;
            name = n === 1 ? baseName : `${baseName} #${n}`;
          }
          taken.add(name);
          n++;
          return name;
        };
        for (const pwd of passwords) {
          const res = await vaultApi.setCredential(nextName(), "password", pwd);
          credIdByPassword.set(pwd, res.id);
        }
      }

      // 纯前端循环逐条落库，逐条收集成功/失败。
      let okCount = 0;
      const failures: { name: string; reason: string }[] = [];
      for (const r of toAdd) {
        try {
          await assetApi.create({
            kind: "ssh",
            name: r.name,
            host: r.host,
            port: r.port,
            username: r.username || null,
            authKind: r.authKind || "password",
            credId: r.password ? (credIdByPassword.get(r.password) ?? null) : null,
            note: r.note || "",
            groupId: null,
          });
          okCount++;
        } catch (e) {
          failures.push({ name: r.name, reason: describeError(e) });
        }
      }

      void qc.invalidateQueries({ queryKey: ["assets"] });
      void qc.invalidateQueries({ queryKey: ["credentials"] });
      setReport({ okCount, credCount: passwords.length, failures });
      setStage("result");
    } catch (e) {
      pushToast("error", `导入失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const title =
    stage === "input" ? "批量导入资产" : stage === "preview" ? "预览导入结果" : "导入完成";

  return (
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal max-w-[720px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">{title}</span>
          <div className="nx-spacer" />
          <button className="nx-icon-btn nx-icon-btn-sm" title="关闭" onClick={onClose}>
            <IconClose size={14} />
          </button>
        </div>

        {stage === "input" && (
          <div className="nx-modal-body">
            <div className="mb-2">
              <label className="nx-label">粘贴资产清单（每行一条）</label>
              <textarea
                className="nx-textarea nx-mono text-[11.5px]"
                rows={10}
                autoFocus
                value={text}
                onChange={(e) => setText(e.target.value)}
                placeholder={
                  'host=10.78.78.82 port=22 user=yun authType=password password=111111 title=web-01 note="..."\n' +
                  "yun@10.78.78.82:22\n10.78.78.83:22\n# 以 # 开头的行会被忽略"
                }
              />
            </div>
            <div className="nx-hint">
              支持两种格式：① 导出格式 <code>host= port= user= authType= password= title= note=</code>
              （与「导出到剪贴板」闭环）；② 极简 <code>user@host:port</code> 或 <code>host:port</code>，
              密码可缺省。认不出来的行会明确跳过。
            </div>
          </div>
        )}

        {stage === "preview" && (
          <div className="nx-modal-body">
            <div className="mb-2 flex flex-wrap items-center gap-2 text-[12px]">
              <span className="nx-badge nx-badge-green">新增 {newCount}</span>
              <span className="nx-badge">已存在 {dupCount}</span>
              {(rows.length - newCount - dupCount) > 0 && (
                <span className="nx-badge nx-badge-amber">
                  问题行 {rows.length - newCount - dupCount}
                </span>
              )}
              <div className="nx-spacer" />
              <label className="flex cursor-pointer items-center gap-2 text-neutral-400">
                <input
                  className="nx-check"
                  type="checkbox"
                  checked={forceAdd}
                  onChange={(e) => setForceAdd(e.target.checked)}
                />
                已存在的也照样添加
              </label>
            </div>

            <div className="max-h-[48vh] overflow-y-auto rounded-lg border border-neutral-800/70 bg-neutral-950/40">
              <table className="nx-table">
                <thead>
                  <tr>
                    <th style={{ width: 52 }}>行</th>
                    <th style={{ width: 120 }}>结论</th>
                    <th style={{ width: 160 }}>名称</th>
                    <th>地址</th>
                    <th style={{ width: 90 }}>密码</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r) => {
                    const meta = STATUS_META[r.status];
                    const skip = r.status === "dup" && !forceAdd;
                    const wrong = r.status === "unrecognized" || r.status === "error";
                    return (
                      <tr
                        key={r.lineNo}
                        className={wrong ? "opacity-60" : skip ? "opacity-70" : ""}
                      >
                        <td className="nx-mono text-neutral-500">{r.lineNo}</td>
                        <td>
                          <span className={meta.badge}>{meta.label}</span>
                        </td>
                        <td className="truncate">{r.name || "—"}</td>
                        <td className="nx-mono truncate">
                          {r.host ? `${r.username ? `${r.username}@` : ""}${r.host}:${r.port}` : r.raw}
                          {r.detail && (
                            <span className="ml-1.5 text-neutral-500">{r.detail}</span>
                          )}
                        </td>
                        <td>{r.password ? "有" : "—"}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>

            <div className="nx-hint mt-2">
              将新增 <span className="text-neutral-300">{toAdd.length}</span> 个资产
              {passwordCount > 0 && (
                <>
                  ；{passwordCount} 条不同密码会各自合并成一条凭据（命名「导入批次 {todayStr()}」）
                </>
              )}
              。无密码的行不建凭据。
            </div>
          </div>
        )}

        {stage === "result" && report && (
          <div className="nx-modal-body">
            <div className="mb-3 flex items-start gap-2 rounded-md border border-green-500/25 bg-green-500/10 px-2.5 py-2 text-[12px] text-green-300/90">
              <IconCheckCircle size={15} className="mt-[1px] shrink-0" />
              <span>
                成功新增 <span className="font-semibold">{report.okCount}</span> 个资产
                {report.credCount > 0 && <>，新建 {report.credCount} 条凭据</>}。
              </span>
            </div>
            {report.failures.length > 0 && (
              <div className="rounded-md border border-red-500/25 bg-red-500/10 px-2.5 py-2 text-[11.5px] text-red-300/90">
                <div className="mb-1 flex items-center gap-1.5 font-semibold">
                  <IconAlert size={13} />
                  {report.failures.length} 条失败
                </div>
                {report.failures.map((f, i) => (
                  <div key={i} className="truncate">
                    {f.name}：{f.reason}
                  </div>
                ))}
              </div>
            )}
          </div>
        )}

        <div className="nx-modal-footer">
          {stage === "input" && (
            <>
              <button className="nx-btn nx-btn-ghost" onClick={onClose}>
                取消
              </button>
              <button
                className="nx-btn nx-btn-primary"
                disabled={!text.trim()}
                onClick={() => setStage("preview")}
              >
                <IconUpload size={12} />
                解析预览
              </button>
            </>
          )}
          {stage === "preview" && (
            <>
              <button className="nx-btn nx-btn-ghost" onClick={() => setStage("input")} disabled={busy}>
                上一步
              </button>
              <button
                className="nx-btn nx-btn-primary"
                disabled={toAdd.length === 0 || busy}
                onClick={() => void run()}
              >
                <IconUpload size={12} />
                {busy ? "导入中…" : `开始导入 (${toAdd.length})`}
              </button>
            </>
          )}
          {stage === "result" && (
            <button className="nx-btn nx-btn-primary" onClick={onClose}>
              完成
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
