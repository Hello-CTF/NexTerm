// 新建凭据弹窗。
//
// 两条产品决定写在这里：
// · **没有「私钥口令」这个类型** —— 口令是私钥的一个属性，在私钥表单里作为选填项出现，
//   跟私钥存进同一条凭据（旧版把它做成独立凭据，列表里多出一条看不出属于哪把钥匙）；
// · **私钥来源二选一**：「引用本地文件」（只记路径，不复制正文）或「存入凭据库」
//   （读内容加密保存）。两种模式下口令都能填，也都跟着这条凭据走。
import { useState } from "react";
import { assetApi, vaultApi, type CredentialSource } from "../../ipc/commands";
import { pickKeyFile } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { KIND_META, NEW_KIND_ORDER } from "./meta";

type Origin = "ref" | "vault";

export function NewCredentialModal({
  onClose,
  onSaved,
}: {
  onClose: () => void;
  onSaved: (id: string) => void;
}) {
  const { pushToast } = useUi();
  const [name, setName] = useState("");
  const [kind, setKind] = useState("password");
  const [value, setValue] = useState("");
  const [origin, setOrigin] = useState<Origin>("ref");
  /** 私钥文件路径：引用模式 = 要引用的文件；入库模式 = 要读取的文件。 */
  const [keyFilePath, setKeyFilePath] = useState("");
  const [contentMode, setContentMode] = useState<"file" | "paste">("file");
  const [pastedKey, setPastedKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [saving, setSaving] = useState(false);

  const isKey = kind === "private_key";
  const pick = (set: (v: string) => void) =>
    void pickKeyFile().then((p) => {
      if (p) set(p);
    });

  const fileReady = keyFilePath.trim().length > 0;
  const contentReady = contentMode === "paste" ? pastedKey.trim().length > 0 : fileReady;
  const keyReady = origin === "ref" ? fileReady : contentReady;
  const ready = name.trim().length > 0 && (isKey ? keyReady : value.length > 0);

  const save = async () => {
    setSaving(true);
    try {
      let secret = value;
      let source: CredentialSource | undefined;
      if (isKey) {
        if (origin === "ref") {
          // 引用：库里只落路径（不读内容），口令可选
          source = "file";
          secret = keyFilePath.trim();
        } else {
          // 入库：文件模式在保存时才读内容 —— 选完到保存之间文件被改动的窗口最小
          source = "inline";
          secret =
            contentMode === "paste" ? pastedKey.trim() : await assetApi.readKeyFile(keyFilePath.trim());
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
    <div className="nx-overlay" onClick={onClose}>
      <div className="nx-modal max-w-[440px]" onClick={(e) => e.stopPropagation()}>
        <div className="nx-modal-header">
          <span className="text-[13px] font-semibold text-neutral-100">新建凭据</span>
        </div>
        <div className="nx-modal-body">
          {/* 类型：卡片比下拉框更直观 —— 选中项一眼可见，不用点开才知道有几个选项 */}
          <div className="nx-form-row">
            <label className="nx-label">类型</label>
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
                      className={`flex h-6 w-6 shrink-0 items-center justify-center rounded ${meta.bg} ${meta.tone}`}
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
            <label className="nx-label">名称</label>
            <input
              className="nx-input"
              value={name}
              placeholder="例如：db-prod"
              autoFocus
              onChange={(e) => setName(e.target.value)}
            />
            <span className="nx-hint mt-1.5 block">名称只给你自己看，建议写成「用途-环境」。</span>
          </div>

          {isKey ? (
            <>
              <div className="nx-form-row">
                <label className="nx-label">私钥来源</label>
                <div className="nx-segment mb-2">
                  <button
                    type="button"
                    className={`nx-segment-item ${origin === "ref" ? "is-active" : ""}`}
                    onClick={() => setOrigin("ref")}
                  >
                    引用本地文件
                  </button>
                  <button
                    type="button"
                    className={`nx-segment-item ${origin === "vault" ? "is-active" : ""}`}
                    onClick={() => setOrigin("vault")}
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
                        onClick={() => setContentMode("file")}
                      >
                        私钥文件
                      </button>
                      <button
                        type="button"
                        className={`nx-segment-item ${contentMode === "paste" ? "is-active" : ""}`}
                        onClick={() => setContentMode("paste")}
                      >
                        粘贴内容
                      </button>
                    </div>
                    {contentMode === "file" ? (
                      <div className="flex gap-1.5">
                        <input
                          className="nx-input font-mono text-[12px]"
                          value={keyFilePath}
                          onChange={(e) => setKeyFilePath(e.target.value)}
                          placeholder="选择私钥文件"
                        />
                        <button
                          type="button"
                          className="nx-btn nx-btn-outline shrink-0"
                          onClick={() => pick(setKeyFilePath)}
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
                      />
                    )}
                    <div className="nx-hint mt-1.5">
                      ↳ 保存时读取内容并加密入库，之后不再依赖原文件。
                    </div>
                  </>
                )}
              </div>

              <div className="nx-form-row">
                <label className="nx-label">私钥口令</label>
                <input
                  type="password"
                  className="nx-input font-mono"
                  value={passphrase}
                  autoComplete="off"
                  onChange={(e) => setPassphrase(e.target.value)}
                  placeholder="没有就留空"
                />
                <div className="nx-hint mt-1.5">
                  ↳ 选填。口令跟私钥存在同一条凭据里，不会单独占一条。
                </div>
              </div>
            </>
          ) : (
            <div className="nx-form-row">
              <label className="nx-label">值</label>
              <input
                type="password"
                className="nx-input"
                value={value}
                autoComplete="off"
                onChange={(e) => setValue(e.target.value)}
              />
            </div>
          )}
        </div>
        <div className="nx-modal-footer">
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
  );
}
