import { ask } from "../../ui/dialogs";
import { assetApi, type Asset } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { withoutInventoryMetadata } from "./inventory";
import { t } from "../../i18n";

export async function cloneAsset(a: Asset): Promise<Asset | null> {
  // The confirmation is account-scoped: a late resolve after an account switch must not
  // create the clone under the new account.
  const epoch = useUi.getState().accountEpoch;
  const ok = await ask(
    t("克隆「{name}」？\n\n会新建一条独立资产「{name2} 副本」：共享原资产的凭据引用与私钥路径，不复制凭据内容。", { name: a.name, name2: a.name }),
    { title: t("克隆资产"), kind: "info" },
  );
  if (!ok || useUi.getState().accountEpoch !== epoch) return null;
  return assetApi.create({
    kind: a.kind,
    name: t("{name} 副本", { name: a.name }),
    groupId: a.groupId,
    host: a.host,
    port: a.port,
    username: a.username,
    authKind: a.authKind,
    keyPath: a.keyPath,
    credId: a.credId,
    options: withoutInventoryMetadata(a.options),
    tags: a.tags,
    note: a.note,
  });
}
