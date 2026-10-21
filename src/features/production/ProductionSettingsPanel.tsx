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
// environment list and the three surface connect opt-in pairs. Everything
// defaults to off/empty server-side; the opt-ins only gate connecting
// disconnected assets (owner / known host key / vault / production rules still
// apply, and unknown host keys are never auto-accepted). They never bypass the
// per-operation typed environment-name confirmation for production writes.

const MAX_ENVIRONMENT_NAMES = 32;
const MAX_ENVIRONMENT_LENGTH = 64;

type ProductionOptInKey =
  | "cronConnectOptIn"
  | "cronConnectProductionOptIn"
  | "broadcastConnectOptIn"
  | "broadcastConnectProductionOptIn"
  | "targetSetConnectOptIn"
  | "targetSetConnectProductionOptIn";

interface OptInRow {
  key: ProductionOptInKey;
  label: string;
  hint: string;
}

const OPT_IN_ROWS: OptInRow[] = [
  {
    key: "cronConnectOptIn",
    label: "cron 定时任务连接未连接资产",
    hint: "关闭时，cron 只使用已连接会话，不会为未连接资产建立连接。",
  },
  {
    key: "cronConnectProductionOptIn",
    label: "cron 连接生产环境资产",
    hint: "生产环境目标还需要此项显式允许；仍需已确认的主机密钥与可用凭据库。",
  },
  {
    key: "broadcastConnectOptIn",
    label: "广播连接未连接资产",
    hint: "关闭时，广播写入不会重连掉线的目标会话。",
  },
  {
    key: "broadcastConnectProductionOptIn",
    label: "广播连接生产环境资产",
    hint: "生产环境目标还需要此项显式允许；仍需已确认的主机密钥与可用凭据库。",
  },
  {
    key: "targetSetConnectOptIn",
    label: "目标集批量操作连接未连接资产",
    hint: "关闭时，目标集批量任务只使用已连接会话。",
  },
  {
    key: "targetSetConnectProductionOptIn",
    label: "目标集连接生产环境资产",
    hint: "生产环境目标还需要此项显式允许；仍需已确认的主机密钥与可用凭据库。",
  },
];

export function ProductionSettingsPanel() {
  const pushToast = useUi((s) => s.pushToast);
  const accountEpoch = useUi((s) => s.accountEpoch);
  const [settings, setSettings] = useState<ProductionSettingsDTO | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [draftNames, setDraftNames] = useState<string[] | null>(null);
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
    setDraftNames(null);
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

  const names = draftNames ?? settings?.productionEnvironments ?? [];
  const namesDirty = draftNames !== null && settings !== null && draftNames.join("\u0000") !== settings.productionEnvironments.join("\u0000");

  const addName = () => {
    const name = nameInput.trim();
    if (name === "") return;
    if (name.length > MAX_ENVIRONMENT_LENGTH) {
      setError(`生产环境名最长 ${MAX_ENVIRONMENT_LENGTH} 字符`);
      return;
    }
    if (names.length >= MAX_ENVIRONMENT_NAMES) {
      setError(`生产环境名最多 ${MAX_ENVIRONMENT_NAMES} 个`);
      return;
    }
    if (names.some((existing) => existing.toLowerCase() === name.toLowerCase())) {
      setNameInput("");
      return;
    }
    setError(null);
    setDraftNames([...names, name]);
    setNameInput("");
  };

  const saveNames = async () => {
    if (draftNames === null) return;
    const invalid = draftNames.find((name) => name.trim() === "" || name.length > MAX_ENVIRONMENT_LENGTH);
    if (invalid !== undefined || draftNames.length > MAX_ENVIRONMENT_NAMES) {
      setError("生产环境名列表未通过校验，请检查空项、长度与数量");
      return;
    }
    setPendingKey("productionEnvironments");
    const saved = await save({ productionEnvironments: draftNames }, "生产环境列表已保存");
    setPendingKey(null);
    if (saved) {
      setDraftNames(null);
      // The configured list feeds the workspace/terminal/AI warnings: reload
      // the classification at the current epoch so mounted consumers pick the
      // new list up without an account switch.
      refreshProductionClassification();
    }
  };

  const toggleOptIn = async (row: OptInRow, value: boolean) => {
    setPendingKey(row.key);
    const patch = { [row.key]: value } as ProductionSettingsUpdateRequest;
    await save(patch, value ? "已开启（仅对当前账号生效）" : "已关闭");
    setPendingKey(null);
  };

  return (
    <section id="settings-production" className="nx-card scroll-mt-12" aria-label="生产安全">
      <div className="mb-1 flex items-center gap-2">
        <IconShield size={15} className="text-neutral-400" />
        <span className="nx-card-title">生产安全</span>
        <span className="nx-badge">仅当前账号</span>
      </div>
      <p className="nx-hint mb-3">
        生产环境列表之外，内置的 production / prod 始终视为生产。写入生产目标仍需逐次输入实际环境名确认，本页任何开关都不会放行该确认。
      </p>

      {error && (
        <div role="alert" className="mb-2 rounded border border-red-900/60 bg-red-950/20 px-2 py-1.5 text-[11px] text-red-300">
          {error}
        </div>
      )}

      <div className="mb-1 text-[12.5px] text-neutral-200">生产环境列表</div>
      <p className="nx-hint mb-2">
        资产通过 <code>env:</code> 标签或 canonical 名称标记环境；此处添加的自定义环境名（如 live）与内置名称一样触发生产警戒。
      </p>
      {settings === null && error === null ? (
        <div className="nx-hint mb-2" role="status">正在加载…</div>
      ) : (
        <>
          <div className="mb-2 flex flex-wrap items-center gap-1.5">
            {names.length === 0 && <span className="nx-hint">未添加自定义环境名。</span>}
            {names.map((name) => (
              <span key={name} className="nx-chip" title={name}>
                <span className="truncate">{name}</span>
                <button
                  type="button"
                  className="nx-chip-x"
                  aria-label={`移除生产环境名 ${name}`}
                  disabled={pendingKey !== null}
                  onClick={() => setDraftNames(names.filter((existing) => existing !== name))}
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
              disabled={pendingKey !== null}
              onChange={(event) => setNameInput(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  event.preventDefault();
                  addName();
                }
              }}
            />
            <button
              type="button"
              className="nx-btn nx-btn-sm"
              aria-label="添加生产环境名"
              disabled={pendingKey !== null || nameInput.trim() === ""}
              onClick={addName}
            >
              <IconPlus size={12} />
              添加
            </button>
            <button
              type="button"
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={pendingKey !== null || !namesDirty}
              onClick={() => void saveNames()}
            >
              {pendingKey === "productionEnvironments" ? "正在保存…" : "保存列表"}
            </button>
          </div>
        </>
      )}

      <div className="mb-1 mt-4 text-[12.5px] text-neutral-200">连接 opt-in（R-07，默认全部关闭）</div>
      <p className="nx-hint mb-2 flex items-start gap-1.5">
        <IconAlert size={12} className="mt-0.5 shrink-0 text-amber-300" />
        开启后对应 surface 才可为未连接资产建立连接；属主一致、主机密钥已确认、凭据库可用等规则仍然生效，未知主机密钥不会被自动接受。
      </p>
      <div className="flex flex-col gap-2">
        {OPT_IN_ROWS.map((row) => {
          const value = settings?.[row.key] ?? false;
          return (
            <label key={row.key} className="flex cursor-pointer items-start gap-3">
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 shrink-0"
                aria-label={row.label}
                checked={value}
                disabled={settings === null || pendingKey !== null}
                onChange={(event) => void toggleOptIn(row, event.target.checked)}
              />
              <span className="min-w-0 flex-1">
                <span className="block text-[12.5px] text-neutral-200">{row.label}</span>
                <span className="nx-hint mt-0.5 block">{row.hint}</span>
              </span>
            </label>
          );
        })}
      </div>
    </section>
  );
}
