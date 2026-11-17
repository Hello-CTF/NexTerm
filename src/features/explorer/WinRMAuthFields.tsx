import { useId, useState } from "react";
import { Select } from "../../ui/Select";
import type { WinRMAuth, WinRMAuthOptions } from "./assetKindFields";
import { t } from "../../i18n";

// The WinRM part of the asset form: the authentication protocol and, for Kerberos, the realm with the KDC and SPN
// overrides folded away, since DNS discovery and HTTP/<host> fit most domains.
export function WinRMAuthFields({
  values,
  onChange,
}: {
  values: WinRMAuthOptions;
  onChange: (patch: Partial<WinRMAuthOptions>) => void;
}) {
  const advancedId = useId();
  const [advancedOpen, setAdvancedOpen] = useState(() => values.kerberosKdc.trim() !== "" || values.kerberosSpn.trim() !== "");
  return (
    <>
      <div className="nx-form-row">
        <label className="nx-label">{t("WinRM 认证协议")}</label>
        <Select
          ariaLabel={t("WinRM 认证协议")}
          value={values.auth}
          onChange={(value) => onChange({ auth: value as WinRMAuth })}
          options={[
            { value: "ntlm", label: "NTLM" },
            { value: "kerberos", label: "Kerberos" },
            ...(values.auth === "basic" ? [{ value: "basic", label: t("Basic（明文密码，仅限 HTTPS）") }] : []),
          ]}
        />
        {values.auth === "kerberos" && (
          <div className="nx-hint mt-1.5">
            {t("↳ 用户名填写 user@域名 或 域\\user，主机地址填写主机在域中的完整域名；HTTP 端口的消息由 Kerberos 加密。")}
          </div>
        )}
      </div>
      {values.auth === "kerberos" && (
        <>
          <div className="nx-form-row">
            <label className="nx-label">{t("Kerberos 域")}</label>
            <input
              className="nx-input"
              aria-label={t("Kerberos 域")}
              value={values.kerberosRealm}
              onChange={(event) => onChange({ kerberosRealm: event.target.value })}
              placeholder={t("可选，默认取用户名中的域，如 CORP.EXAMPLE.COM")}
              spellCheck={false}
              autoCapitalize="off"
              autoCorrect="off"
            />
          </div>
          <div className="nx-form-row">
            <button
              type="button"
              className="text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
              aria-expanded={advancedOpen}
              aria-controls={advancedId}
              onClick={() => setAdvancedOpen((open) => !open)}
            >
              {t("Kerberos 高级设置")}
            </button>
            {advancedOpen && (
              <div id={advancedId} className="mt-2 space-y-2">
                <input
                  className="nx-input"
                  aria-label={t("KDC 地址")}
                  value={values.kerberosKdc}
                  onChange={(event) => onChange({ kerberosKdc: event.target.value })}
                  placeholder={t("KDC 地址，可选，多个用逗号分隔；留空则通过 DNS 查找")}
                  spellCheck={false}
                  autoCapitalize="off"
                  autoCorrect="off"
                />
                <input
                  className="nx-input"
                  aria-label={t("服务主体名称（SPN）")}
                  value={values.kerberosSpn}
                  onChange={(event) => onChange({ kerberosSpn: event.target.value })}
                  placeholder={t("SPN，可选，默认 HTTP/主机地址")}
                  spellCheck={false}
                  autoCapitalize="off"
                  autoCorrect="off"
                />
                <p className="nx-hint">{t("KDC 连接不经过代理，需能直接访问 TCP 88 端口。")}</p>
              </div>
            )}
          </div>
        </>
      )}
    </>
  );
}
