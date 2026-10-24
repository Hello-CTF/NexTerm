import { useEffect, useRef, useState } from "react";
import { useUi } from "../../app/store";
import { describeError } from "../../ui/errorText";
import { IconAlert, IconPlus, IconShield, IconXCircle } from "../../ui/icons";
import {
  productionSettingsApi,
  refreshProductionClassification,
  type ProductionSettingsDTO,
  type ProductionSettingsUpdateRequest,
} from "./productionSettings";

// R-01/R-07 per-owner production safety settings: the configurable production
// environment list and the surface connect opt-in pairs (cron, broadcast,
// target-set, and the AI terminal surface). Everything defaults to off/empty
// server-side; the opt-ins only gate connecting disconnected assets (owner /
// known host key / vault / production rules still apply, and unknown host keys
// are never auto-accepted). They never bypass the per-operation typed
// environment-name confirmation for production writes.

const MAX_ENVIRONMENT_NAMES = 32;
const MAX_ENVIRONMENT_LENGTH = 64;

type ProductionOptInKey =
  | "cronConnectOptIn"
  | "cronConnectProductionOptIn"
  | "broadcastConnectOptIn"
  | "broadcastConnectProductionOptIn"
  | "targetSetConnectOptIn"
  | "targetSetConnectProductionOptIn"
  | "aiTerminalConnectOptIn"
  | "aiTerminalConnectProductionOptIn";

interface ConnectRow {
  label: string;
  hint: string;
  connect: ProductionOptInKey;
  // production only takes effect while connect is on; the backend checks connect first.
  production: ProductionOptInKey;
}

const CONNECT_ROWS: ConnectRow[] = [
  { label: "定时任务", hint: "到点运行时目标主机未连接", connect: "cronConnectOptIn", production: "cronConnectProductionOptIn" },
  { label: "广播", hint: "广播目标的连接已断开", connect: "broadcastConnectOptIn", production: "broadcastConnectProductionOptIn" },
  { label: "目标集批量执行", hint: "目标集中有未连接的主机", connect: "targetSetConnectOptIn", production: "targetSetConnectProductionOptIn" },
  { label: "AI 打开终端", hint: "AI 要在未连接的主机上打开终端", connect: "aiTerminalConnectOptIn", production: "aiTerminalConnectProductionOptIn" },
];

export function ProductionSettingsPanel() {
  const pushToast = useUi((s) => s.pushToast);
  const accountEpoch = useUi((s) => s.accountEpoch);
  const [settings, setSettings] = useState<ProductionSettingsDTO | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [nameInput, setNameInput] = useState("");
  const [pendingKey, setPendingKey] = useState<string | null>(null);
  const loadGenRef = useRef(0);
  const aliveRef = useRef(true);

  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  useEffect(() => {
    const gen = ++loadGenRef.current;
    setSettings(null);
    setError(null);
    productionSettingsApi
      .get()
      .then((dto) => {
        if (!aliveRef.current || gen !== loadGenRef.current) return;
        if (useUi.getState().accountEpoch !== accountEpoch) return;
        setSettings(dto);
      })
      .catch((cause: unknown) => {
        if (!aliveRef.current || gen !== loadGenRef.current) return;
        if (useUi.getState().accountEpoch !== accountEpoch) return;
        setError(describeError(cause));
      });
  }, [accountEpoch]);

  const save = async (patch: ProductionSettingsUpdateRequest, successMessage: string) => {
    const gen = loadGenRef.current;
    try {
      const dto = await productionSettingsApi.set(patch);
      if (!aliveRef.current || gen !== loadGenRef.current) return false;
      if (useUi.getState().accountEpoch !== accountEpoch) return false;
      setSettings(dto);
      pushToast("success", successMessage);
      return true;
    } catch (cause) {
      if (!aliveRef.current || gen !== loadGenRef.current) return false;
      if (useUi.getState().accountEpoch !== accountEpoch) return false;
      pushToast("error", `保存失败：${describeError(cause)}`);
      return false;
    }
  };

  const names = settings?.productionEnvironments ?? [];

  const saveNames = async (next: string[], successMessage: string) => {
    setPendingKey("productionEnvironments");
    const saved = await save({ productionEnvironments: next }, successMessage);
    setPendingKey(null);
    // The list feeds the workspace/terminal/AI warnings: reload the
    // classification so mounted consumers pick it up without an account switch.
    if (saved) refreshProductionClassification();
    return saved;
  };

  const addName = async () => {
    const name = nameInput.trim();
    if (name === "") return;
    if (name.length > MAX_ENVIRONMENT_LENGTH) {
      setError(`环境名最长 ${MAX_ENVIRONMENT_LENGTH} 个字符`);
      return;
    }
    if (names.length >= MAX_ENVIRONMENT_NAMES) {
      setError(`最多添加 ${MAX_ENVIRONMENT_NAMES} 个环境名`);
      return;
    }
    setError(null);
    if (names.some((existing) => existing.toLowerCase() === name.toLowerCase())) {
      setNameInput("");
      return;
    }
    if (await saveNames([...names, name], `已将 ${name} 视为生产环境`)) setNameInput("");
  };

  const removeName = (name: string) =>
    void saveNames(names.filter((existing) => existing !== name), `${name} 不再视为生产环境`);

  const toggle = async (key: ProductionOptInKey, patch: ProductionSettingsUpdateRequest, message: string) => {
    setPendingKey(key);
    await save(patch, message);
    setPendingKey(null);
  };

  return (
    <section id="settings-production" className="nx-card scroll-mt-12" aria-label="生产安全">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">生产安全</span>
        <span className="nx-badge">仅当前账号</span>
      </div>
      <p className="nx-hint mb-3">
        标签为 production 或 prod 的主机始终按生产环境对待。对生产环境的写操作每次都要输入环境名确认，下面的设置不会跳过这一步。
      </p>

      {error && (
        <div role="alert" className="mb-2 rounded border border-red-900/60 bg-red-950/20 px-2 py-1.5 text-[11px] text-red-300">
          {error}
        </div>
      )}

      <div className="mb-1 text-[12.5px] text-neutral-200">其他生产环境名</div>
      <p className="nx-hint mb-2">
        在主机标签里写 <code>env:名称</code>（如 <code>env:live</code>）标记环境；名称在这里的主机也按生产环境对待。
      </p>
      {settings === null && error === null ? (
        <div className="nx-hint mb-2" role="status">正在加载…</div>
      ) : (
        <>
          <div className="mb-2 flex flex-wrap items-center gap-1.5">
            {names.length === 0 && <span className="nx-hint">暂无，只有 production / prod 按生产环境对待。</span>}
            {names.map((name) => (
              <span key={name} className="nx-chip" title={name}>
                <span className="truncate">{name}</span>
                <button
                  type="button"
                  className="nx-chip-x"
                  aria-label={`移除生产环境名 ${name}`}
                  disabled={pendingKey !== null}
                  onClick={() => removeName(name)}
                >
                  <IconXCircle size={10} />
                </button>
              </span>
            ))}
          </div>
          <div className="mb-2 flex flex-wrap items-center gap-1.5">
            <input
              className="nx-input max-w-56"
              aria-label="新增生产环境名"
              placeholder="例如 live"
              value={nameInput}
              maxLength={MAX_ENVIRONMENT_LENGTH}
              disabled={settings === null || pendingKey !== null}
              onChange={(event) => setNameInput(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  void addName();
                }
              }}
            />
            <button
              type="button"
              className="nx-btn nx-btn-sm"
              aria-label="添加生产环境名"
              disabled={settings === null || pendingKey !== null || nameInput.trim() === ""}
              onClick={() => void addName()}
            >
              <IconPlus size={12} />
              {pendingKey === "productionEnvironments" ? "正在保存…" : "添加"}
            </button>
          </div>
        </>
      )}

      <div className="mb-1 mt-4 text-[12.5px] text-neutral-200">自动连接</div>
      <p className="nx-hint mb-2 flex items-start gap-1.5">
        <IconAlert size={12} className="mt-0.5 shrink-0 text-amber-300" />
        默认关闭：这些功能只用已经打开的连接，遇到未连接的主机会直接失败。开启后它们可以自己连上主机，但只连主机密钥已确认过的主机，且凭据库需已解锁。
      </p>
      <table className="w-full text-[12.5px]">
        <thead>
          <tr className="text-left text-[11px] text-neutral-500">
            <th className="py-1 font-normal">功能</th>
            <th className="w-20 py-1 text-center font-normal">自动连接</th>
            <th className="w-20 py-1 text-center font-normal">含生产环境</th>
          </tr>
        </thead>
        <tbody>
          {CONNECT_ROWS.map((row) => {
            const connect = settings?.[row.connect] ?? false;
            const production = settings?.[row.production] ?? false;
            const busy = settings === null || pendingKey !== null;
            return (
              <tr key={row.connect} className="border-t border-neutral-800/60">
                <td className="py-1.5 pr-2">
                  <span className="block text-neutral-200">{row.label}</span>
                  <span className="nx-hint block">{row.hint}时自动连接</span>
                </td>
                <td className="py-1.5 text-center">
                  <input
                    type="checkbox"
                    className="h-4 w-4 max-sm:h-5 max-sm:w-5"
                    aria-label={`${row.label}：自动连接未连接的主机`}
                    checked={connect}
                    disabled={busy}
                    onChange={(event) => void toggle(
                      row.connect,
                      { [row.connect]: event.target.checked },
                      event.target.checked ? `${row.label}可以自动连接主机` : `${row.label}不再自动连接主机`,
                    )}
                  />
                </td>
                <td className="py-1.5 text-center">
                  <input
                    type="checkbox"
                    className="h-4 w-4 max-sm:h-5 max-sm:w-5"
                    aria-label={`${row.label}：也自动连接生产环境主机`}
                    title={connect ? undefined : "先开启自动连接"}
                    checked={connect && production}
                    disabled={busy || !connect}
                    onChange={(event) => void toggle(
                      row.production,
                      { [row.production]: event.target.checked },
                      event.target.checked ? `${row.label}也会自动连接生产环境主机` : `${row.label}不再自动连接生产环境主机`,
                    )}
                  />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </section>
  );
}
