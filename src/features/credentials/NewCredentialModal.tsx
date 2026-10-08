import { useId, useRef, useState } from "react";
import { assetApi, vaultApi, type CredentialSource } from "../../ipc/commands";
import { pickKeyFile } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { KIND_META, NEW_KIND_ORDER } from "./meta";
import { resolveInlineKeyContent, useInlineKeyPicker } from "./keyStaging";
import { useVaultInitGate } from "./useVaultInitGate";

type Origin = "ref" | "vault";

export function NewCredentialModal({
  onClose,
  onSaved,
}: {
  onClose: () => void;
  onSaved: (id: string) => void;
}) {
  const { pushToast } = useUi();
  const { ensureVaultReady, vaultInitGate } = useVaultInitGate();
  const [name, setName] = useState("");
  const [kind, setKind] = useState("password");
  const [value, setValue] = useState("");
  const [origin, setOrigin] = useState<Origin>("ref");
  const [keyFilePath, setKeyFilePath] = useState("");
  const [inlineKeyContent, setInlineKeyContent] = useState<string | null>(null);
  const [contentMode, setContentMode] = useState<"file" | "paste">("file");
  const [pastedKey, setPastedKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [saving, setSaving] = useState(false);

  const modalRef = useRef<HTMLDivElement>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const nameId = useId();
  const valueId = useId();
  const passphraseId = useId();
  const layer = useOverlayFocus(true, modalRef, {
    initialFocus: () => nameInputRef.current,
  });

  const isKey = kind === "private_key";
  const inlinePicker = useInlineKeyPicker();
  const pick = (set: (v: string) => void) =>
    void pickKeyFile().then((p) => {
      if (p) set(p);
    });
  const pickInline = async () => {
    try {
      const selection = await inlinePicker.pick();
      if (!selection) return;
      setKeyFilePath(selection.path);
      setInlineKeyContent(selection.content);
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };
  const changeOrigin = (next: Origin) => {
    inlinePicker.invalidate();
    if (next === "ref" && inlineKeyContent !== null) {
      setKeyFilePath("");
      setInlineKeyContent(null);
    }
    setOrigin(next);
  };
  const changeContentMode = (next: "file" | "paste") => {
    inlinePicker.invalidate();
    setContentMode(next);
  };
  const changeInlinePath = (next: string) => {
    inlinePicker.invalidate();
    setKeyFilePath(next);
    setInlineKeyContent(null);
  };

  const fileReady = keyFilePath.trim().length > 0;
  const contentReady =
    contentMode === "paste" ? pastedKey.trim().length > 0 : fileReady && !inlinePicker.pending;
  const keyReady = origin === "ref" ? fileReady : contentReady;
  const ready = name.trim().length > 0 && (isKey ? keyReady : value.length > 0);

  const save = async () => {
    setSaving(true);
    try {
      if (!(await ensureVaultReady("保存凭据前需要解锁凭据库"))) return;
      let secret = value;
      let source: CredentialSource | undefined;
      if (isKey) {
        if (origin === "ref") {
          source = "file";
          secret = keyFilePath.trim();
        } else {
          source = "inline";
          secret =
            contentMode === "paste"
              ? pastedKey.trim()
              : await resolveInlineKeyContent(
                  { path: keyFilePath.trim(), content: inlineKeyContent },
                  assetApi.readKeyFile,
                );
        }
      }
      const { id } = await vaultApi.setCredential(name.trim(), kind, secret, {
        source,
        ...(isKey && passphrase ? { passphrase } : {}),
      });
      pushToast("success", `已创建凭据「${name.trim()}」`);
      onSaved(id);
    } catch (e) {
      pushToast("error", describeError(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
    <div className="nx-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="nx-modal flex max-w-[440px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (!layer.isTopmost()) return;
          if (e.key === "Escape" && !e.repeat && !isImeKeyEvent(e)) {
            e.preventDefault();
            onClose();
            return;
          }
          trapOverlayTab(e, modalRef.current);
        }}
      >
        <div className="nx-modal-header shrink-0">
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">新建凭据</span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          <div className="nx-form-row">
            <span className="nx-label">类型</span>
            <div className="grid grid-cols-3 gap-1.5">
              {NEW_KIND_ORDER.map((k) => {
                const meta = KIND_META[k];
                const on = k === kind;
                return (
                  <button
                    key={k}
                    type="button"
                    className={`flex items-center gap-2 rounded-md border px-2.5 py-2 text-left text-[12px] ${
                      on
                        ? "border-blue-500/50 bg-blue-500/10 text-neutral-100"
                        : "border-neutral-800/80 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200"
                    }`}
                    onClick={() => setKind(k)}
                  >
                    <span
                      className={`hidden min-[400px]:flex h-6 w-6 shrink-0 items-center justify-center rounded ${meta.bg} ${meta.tone}`}
                    >
                      <meta.Icon size={13} />
                    </span>
                    <span className="truncate">{meta.label}</span>
                  </button>
                );
              })}
            </div>
          </div>

          <div className="nx-form-row">
            <label className="nx-label" htmlFor={nameId}>名称</label>
            <input
              id={nameId}
              ref={nameInputRef}
              className="nx-input"
              value={name}
              placeholder="例如：db-prod"
              onChange={(e) => setName(e.target.value)}
            />
            <span className="nx-hint mt-1.5 block">名称用来区分这条凭据给哪台主机用，之后选凭据时按名字认。</span>
          </div>

          {isKey ? (
            <>
              <div className="nx-form-row">
                <span className="nx-label">私钥来源</span>
                <div className="nx-segment mb-2">
                  <button
                    type="button"
                    className={`nx-segment-item ${origin === "ref" ? "is-active" : ""}`}
                    onClick={() => changeOrigin("ref")}
                  >
                    引用本地文件
                  </button>
                  <button
                    type="button"
                    className={`nx-segment-item ${origin === "vault" ? "is-active" : ""}`}
                    onClick={() => changeOrigin("vault")}
                  >
                    存入凭据库
                  </button>
                </div>

                {origin === "ref" ? (
                  <>
                    <div className="flex gap-1.5">
                      <input
                        className="nx-input font-mono text-[12px]"
                        value={keyFilePath}
                        onChange={(e) => setKeyFilePath(e.target.value)}
                        placeholder="C:\Users\you\.ssh\id_rsa"
                        aria-label="私钥文件路径"
                        autoComplete="off"
                      />
                      <button
                        type="button"
                        className="nx-btn nx-btn-outline shrink-0"
                        onClick={() => pick(setKeyFilePath)}
                      >
                        浏览…
                      </button>
                    </div>
                    <div className="nx-hint mt-1.5">
                      ↳ 只记路径，私钥正文不复制进库。文件被挪走或删掉后，用它的资产就连不上了。
                    </div>
                  </>
                ) : (
                  <>
                    <div className="nx-segment mb-2">
                      <button
                        type="button"
                        className={`nx-segment-item ${contentMode === "file" ? "is-active" : ""}`}
                        onClick={() => changeContentMode("file")}
                      >
                        私钥文件
                      </button>
                      <button
                        type="button"
                        className={`nx-segment-item ${contentMode === "paste" ? "is-active" : ""}`}
                        onClick={() => changeContentMode("paste")}
                      >
                        粘贴内容
                      </button>
                    </div>
                    {contentMode === "file" ? (
                      <div className="flex gap-1.5">
                        <input
                          className="nx-input font-mono text-[12px]"
                          value={keyFilePath}
                          onChange={(e) => changeInlinePath(e.target.value)}
                          placeholder="选择私钥文件"
                          aria-label="私钥文件路径"
                          autoComplete="off"
                        />
                        <button
                          type="button"
                          className="nx-btn nx-btn-outline shrink-0"
                          onClick={() => void pickInline()}
                          disabled={inlinePicker.pending}
                        >
                          浏览…
                        </button>
                      </div>
                    ) : (
                      <textarea
                        className="nx-textarea font-mono text-[11.5px]"
                        rows={4}
                        value={pastedKey}
                        onChange={(e) => setPastedKey(e.target.value)}
                        placeholder={
                          "-----BEGIN OPENSSH PRIVATE KEY-----\n…\n-----END OPENSSH PRIVATE KEY-----"
                        }
                        aria-label="私钥内容"
                        autoComplete="off"
                      />
                    )}
                    <div className="nx-hint mt-1.5">
                      ↳ 保存时读取内容并加密入库，之后不再依赖原文件。
                    </div>
                  </>
                )}
              </div>

              <div className="nx-form-row">
                <label className="nx-label" htmlFor={passphraseId}>私钥口令</label>
                <input
                  id={passphraseId}
                  type="password"
                  className="nx-input font-mono"
                  value={passphrase}
                  autoComplete="off"
                  onChange={(e) => setPassphrase(e.target.value)}
                  placeholder="没有就留空"
                />
                <div className="nx-hint mt-1.5">
                  ↳ 选填。私钥本身带口令才需要填，连接时自动使用。
                </div>
              </div>
            </>
          ) : (
            <div className="nx-form-row">
              <label className="nx-label" htmlFor={valueId}>值</label>
              <input
                id={valueId}
                type="password"
                className="nx-input"
                value={value}
                autoComplete="off"
                onChange={(e) => setValue(e.target.value)}
              />
              <div className="nx-hint mt-1.5">
                ↳ 把密码或 API 密钥原样填进来，保存后加密存入凭据库。
              </div>
            </div>
          )}
        </div>
        <div className="nx-modal-footer shrink-0">
          <button className="nx-btn nx-btn-ghost" onClick={onClose}>
            取消
          </button>
          <button
            className="nx-btn nx-btn-primary"
            disabled={!ready || saving}
            onClick={() => void save()}
          >
            {saving ? "保存中…" : "保存"}
          </button>
        </div>
      </div>
    </div>
    {vaultInitGate}
    </>
  );
}
