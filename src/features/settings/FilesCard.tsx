import { useEffect, useState } from "react";
import { filesApi, type FilesSettingsView } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { IconImage, IconInfo, IconRefresh, IconXCircle } from "../../ui/icons";

export type PublicBaseURLResult = { ok: true; value: string } | { ok: false; error: string };

// normalizePublicBaseURL 与后端 core.ParsePublicBaseURL 同一套校验:
// 仅 http/https、必须有 host、允许路径前缀、拒绝 userinfo/query/fragment, 去掉尾斜杠。
// 空串表示清除持久化值(回退 CLI/env 默认或同源相对链接)。后端 files_settings_set 仍是最终把关。
export function normalizePublicBaseURL(raw: string): PublicBaseURLResult {
  const trimmed = raw.trim();
  if (trimmed === "") return { ok: true, value: "" };
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return { ok: false, error: "不是合法 URL（需要以 http:// 或 https:// 开头）" };
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return { ok: false, error: "只允许 http 或 https 协议" };
  }
  if (parsed.host === "") {
    return { ok: false, error: "缺少主机名" };
  }
  if (parsed.username !== "" || parsed.password !== "") {
    return { ok: false, error: "不允许包含用户名或密码" };
  }
  if (parsed.search !== "" || trimmed.includes("?")) {
    return { ok: false, error: "不允许包含查询参数（?…）" };
  }
  if (parsed.hash !== "") {
    return { ok: false, error: "不允许包含片段（#…）" };
  }
  parsed.pathname = parsed.pathname.replace(/\/+$/, "");
  // WHATWG URL 对 http(s) 的空路径仍序列化为 "/", 这里再剥掉, 与后端 TrimRight(path,"/") 对齐。
  return { ok: true, value: parsed.toString().replace(/\/$/, "") };
}

export function FilesCard() {
  const pushToast = useUi((s) => s.pushToast);
  const [settings, setSettings] = useState<FilesSettingsView | null>(null);
  const [draft, setDraft] = useState("");
  const [loadError, setLoadError] = useState<string | null>(null);
  const [validationError, setValidationError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [reloadToken, setReloadToken] = useState(0);

  useEffect(() => {
    let cancelled = false;
    filesApi
      .settingsGet()
      .then((view) => {
        if (cancelled) return;
        setSettings(view);
        setDraft(view.publicBaseURL);
      })
      .catch((e) => {
        if (!cancelled) setLoadError(describeError(e));
      });
    return () => {
      cancelled = true;
    };
  }, [reloadToken]);

  const save = () => {
    const result = normalizePublicBaseURL(draft);
    if (!result.ok) {
      setValidationError(result.error);
      return;
    }
    setValidationError(null);
    setSaving(true);
    filesApi
      .settingsSet(result.value)
      .then((view) => {
        setSettings(view);
        setDraft(view.publicBaseURL);
        pushToast(
          "success",
          view.publicBaseURL === ""
            ? "已清除：新的公开链接将使用同源相对路径"
            : "已保存默认文件访问基础 URL",
        );
      })
      .catch((e) => pushToast("error", `保存失败：${describeError(e)}`))
      .finally(() => setSaving(false));
  };

  const configured = settings !== null && settings.publicBaseURL !== "";

  return (
    <section className="nx-card">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconImage size={15} className="text-neutral-400" />
        <span className="nx-card-title">文件公开链接</span>
        {settings && (
          <span className={`nx-badge ${configured ? "nx-badge-green" : ""}`}>
            {configured ? "已设置基础 URL" : "同源相对链接"}
          </span>
        )}
      </div>
      <p className="nx-hint mb-3.5">
        在终端粘贴或拖入图片时，图片会上传到服务端并生成<b>限时公开链接</b>
        （默认 24 小时过期，最长 7 天），随后以 Markdown 图片语法插入光标处。
        链接在过期前任何拿到的人都能查看，请不要粘贴敏感截图。
      </p>

      {loadError !== null && (
        <div className="nx-alert nx-alert-danger mb-3 flex items-center gap-2" role="alert">
          <IconXCircle size={14} className="shrink-0" />
          <span className="min-w-0 flex-1">读取文件链接设置失败：{loadError}</span>
          <button
            className="nx-btn nx-btn-outline nx-btn-sm shrink-0"
            onClick={() => {
              setLoadError(null);
              setReloadToken((n) => n + 1);
            }}
          >
            重试
          </button>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <label className="text-[12.5px] text-neutral-200" htmlFor="files-public-base-url">
          默认文件访问基础 URL
        </label>
        <input
          id="files-public-base-url"
          className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
          placeholder="未设置 = 同源相对链接（/files/image/…）"
          value={draft}
          disabled={settings === null || saving}
          onChange={(e) => {
            setDraft(e.target.value);
            setValidationError(null);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") save();
          }}
        />
        <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={saving || settings === null} onClick={save}>
          {saving ? "保存中…" : "保存"}
        </button>
      </div>
      {validationError !== null && (
        <p className="mt-1.5 text-[11.5px] text-red-400" role="alert">
          {validationError}
        </p>
      )}
      <p className="nx-hint mt-2">
        {configured
          ? `新链接将形如 ${settings?.publicBaseURL}/files/image/…；只影响保存后新生成的链接。`
          : "留空保存即可恢复同源相对链接。此设置只用于文件公开链接，与同步、舰队、API、LLM 的地址相互独立。"}
      </p>
      <div className="nx-alert nx-alert-info mt-3 flex items-start gap-2">
        <IconInfo size={14} className="mt-0.5 shrink-0" />
        <div>
          也可以用服务端启动参数 <span className="nx-code">--public-base-url</span> 或环境变量{" "}
          <span className="nx-code">NEXTERM_PUBLIC_BASE_URL</span> 提供默认值；这里保存的值优先级更高。
        </div>
      </div>
      {loadError === null && settings === null && (
        <p className="nx-hint mt-2 flex items-center gap-1.5">
          <IconRefresh size={12} className="animate-spin" />
          正在读取设置…
        </p>
      )}
    </section>
  );
}
