import { useState } from "react";
import { hostPortsApi, type ConnectivityResult } from "../../ipc/hostPortsApi";
import { confirmDialog } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { useAssetProductionClassification } from "../production/productionSettings";
import { t } from "../../i18n";

type Method = "tcp" | "http";

// Runs one connection check from the host itself. The exact command is shown and confirmed before it runs.
export function ConnectivityTest({
  sessionId,
  hostName,
  assetId,
}: {
  sessionId: string;
  hostName: string;
  assetId?: string | null;
}) {
  const production = useAssetProductionClassification(assetId)?.production === true;
  const [method, setMethod] = useState<Method>("tcp");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<ConnectivityResult | null>(null);

  const run = async () => {
    setError(null);
    setResult(null);
    const portNumber = Number(port.trim());
    if (method === "tcp" && (!host.trim() || !port.trim())) {
      setError(t("请填写目标主机和端口"));
      return;
    }
    if (method === "http" && !url.trim()) {
      setError(t("请填写 URL"));
      return;
    }
    const request =
      method === "tcp"
        ? { sessionId, method, host: host.trim(), port: portNumber }
        : { sessionId, method, url: url.trim() };
    setBusy(true);
    try {
      const plan = await hostPortsApi.connectivityTest({ ...request, dryRun: true });
      const warning = production ? t("【生产主机】此命令将在生产环境的主机上执行，请确认目标无误。\n\n") : "";
      const script = plan.script ? t("实际执行的脚本：\n{script}\n\n原始命令：\n", { script: plan.script }) : "";
      const ok = await confirmDialog(t("{warning}将在 {hostName} 上执行：\n{script}{command}", { warning, hostName, script, command: plan.command }));
      if (!ok) return;
      setResult(await hostPortsApi.connectivityTest(request));
    } catch (e) {
      setError(describeError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-1.5 border-t border-neutral-800/60 pt-2" data-testid="connectivity-test">
      <div className="text-[12px] font-semibold text-neutral-300">{t("连通性测试")}</div>
      <div className="flex flex-wrap items-center gap-1.5">
        <div className="nx-segment" role="group" aria-label={t("测试方式")}>
          {(["tcp", "http"] as const).map((m) => (
            <button
              key={m}
              type="button"
              className={`nx-segment-item ${method === m ? "is-active" : ""}`}
              aria-pressed={method === m}
              onClick={() => setMethod(m)}
            >
              {m === "tcp" ? "TCP" : "HTTP"}
            </button>
          ))}
        </div>
        {method === "tcp" ? (
          <>
            <input
              className="nx-input min-w-0 flex-1 basis-32"
              aria-label={t("目标主机")}
              placeholder={t("目标主机")}
              value={host}
              onChange={(e) => setHost(e.target.value)}
            />
            <input
              className="nx-input w-20"
              aria-label={t("目标端口")}
              placeholder={t("端口")}
              inputMode="numeric"
              value={port}
              onChange={(e) => setPort(e.target.value)}
            />
          </>
        ) : (
          <input
            className="nx-input min-w-0 flex-1 basis-48"
            aria-label="URL"
            placeholder="http://127.0.0.1:8080/health"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
          />
        )}
        <button type="button" className="nx-btn nx-btn-primary nx-btn-xs" disabled={busy} onClick={() => void run()}>
          {t("测试")}
        </button>
      </div>
      {error && <p className="m-0 text-[12px] text-red-300" role="alert">{error}</p>}
      {result && (
        <div className="flex flex-col gap-1" data-testid="connectivity-result">
          <div className={`text-[12px] ${result.ok ? "text-green-300" : "text-red-300"}`}>
            {result.ok ? t("成功") : t("失败")}
            {result.exitCode !== undefined && t("，退出码 {exitCode}", { exitCode: result.exitCode })}{t("，耗时 {durationMs} ms", { durationMs: result.durationMs })}</div>
          {result.output && (
            <pre className="nx-mono m-0 max-h-40 overflow-auto whitespace-pre-wrap break-all text-[11px] text-neutral-300">
              {result.output}
            </pre>
          )}
        </div>
      )}
    </div>
  );
}
