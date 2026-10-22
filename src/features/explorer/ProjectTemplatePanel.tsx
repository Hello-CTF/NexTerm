import { useEffect, useState } from "react";
import { useUi } from "../../app/store";
import {
  ALL_PROJECTS,
  projectFocusLabel,
} from "../../app/projectFocus";
import {
  captureProjectTemplateDraft,
  createProjectTemplate,
  templateId,
  templateTabLabel,
  type ProjectTemplate,
  type ProjectTemplateDraft,
  type TemplateProjectFocus,
} from "../../app/projectTemplates";
import { createProjectTemplateRepository } from "../../app/projectTemplateStore";
import { closeProject, openProjectTemplate } from "../../app/projectTemplateActions";
import type { AssetGroup } from "../../ipc/commands";
import type { BroadcastProductionChallenge } from "../production/productionChallenge";
import { ask, promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";

interface ProductionPrompt {
  challenge: BroadcastProductionChallenge;
  command: string;
  // Resolves the typed per-target environment names on confirm, or null on cancel.
  resolve(environmentNames: string[] | null): void;
}

export function ProjectTemplatePanel({ groups }: { groups: readonly AssetGroup[] }) {
  const activeProject = useUi((state) => state.activeProject);
  const workspaces = useUi((state) => state.workspaces);
  const inventory = useUi((state) => state.projectInventory);
  const setActiveProject = useUi((state) => state.setActiveProject);
  const pushToast = useUi((state) => state.pushToast);
  const accountEpoch = useUi((state) => state.accountEpoch);
  const [expanded, setExpanded] = useState(false);
  const [templates, setTemplates] = useState<ProjectTemplate[]>([]);
  const [draft, setDraft] = useState<ProjectTemplateDraft | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [productionPrompt, setProductionPrompt] = useState<ProductionPrompt | null>(null);

  useEffect(() => {
    if (!expanded) return;
    let current = true;
    setError(null);
    void Promise.resolve().then(() => createProjectTemplateRepository().list()).then((items) => {
      if (current) setTemplates(items);
    }).catch((cause) => {
      if (current) setError(describeError(cause));
    });
    return () => {
      current = false;
    };
  }, [expanded]);

  const reload = async () => {
    const items = await createProjectTemplateRepository().list();
    setTemplates(items);
  };

  // Mutations are account-scoped: every await re-checks the captured epoch, so a late
  // completion after an account switch never reaches the new owner's repository or UI.
  const isStale = (epoch: number) => useUi.getState().accountEpoch !== epoch;

  const run = (operation: (epoch: number) => Promise<void>): void => {
    if (busy) return;
    const epoch = accountEpoch;
    setBusy(true);
    setError(null);
    void operation(epoch).catch((cause) => {
      if (!isStale(epoch)) setError(describeError(cause));
    }).finally(() => {
      if (!isStale(epoch)) setBusy(false);
    });
  };

  const beginSave = () => {
    if (activeProject === ALL_PROJECTS || !inventory) {
      pushToast("info", "请先选择一个项目或未分组，再保存模板");
      return;
    }
    const captured = captureProjectTemplateDraft(
      `${projectFocusLabel(activeProject, groups)} 模板`,
      activeProject,
      workspaces,
      inventory,
    );
    if (captured.workspaces.length === 0) {
      pushToast("info", "当前项目没有可保存的主机或数据库工作区");
      return;
    }
    setDraft(captured);
  };

  const save = () => run(async (epoch) => {
    if (!draft) return;
    const template = createProjectTemplate(draft, { id: templateId() });
    await createProjectTemplateRepository().create(template);
    if (isStale(epoch)) return;
    setDraft(null);
    await reload();
    if (isStale(epoch)) return;
    pushToast("success", `项目模板「${template.name}」已保存`);
  });

  const rename = (template: ProjectTemplate) => run(async (epoch) => {
    const name = await promptText("重命名项目模板", template.name);
    if (name === null || name.trim() === template.name) return;
    if (isStale(epoch)) return;
    await createProjectTemplateRepository().rename(template.id, name);
    if (isStale(epoch)) return;
    await reload();
  });

  const remove = (template: ProjectTemplate) => run(async (epoch) => {
    if (!(await ask(`删除项目模板「${template.name}」？\n已打开的工作区和后台终端不受影响。`, { kind: "warning" }))) return;
    if (isStale(epoch)) return;
    await createProjectTemplateRepository().delete(template.id);
    if (isStale(epoch)) return;
    await reload();
    if (isStale(epoch)) return;
    pushToast("success", "项目模板已删除");
  });

  const confirmProductionEnvironmentNames = (challenge: BroadcastProductionChallenge, command: string) =>
    new Promise<string[] | null>((resolve) => setProductionPrompt({ challenge, command, resolve }));

  const finishProductionPrompt = (environmentNames: string[] | null) => {
    productionPrompt?.resolve(environmentNames);
    setProductionPrompt(null);
  };

  const open = (template: ProjectTemplate) => run(async (epoch) => {
    const result = await openProjectTemplate(template, { commandDependencies: { confirmProductionEnvironmentNames } });
    if (isStale(epoch)) return;
    const failedWorkspaces = Object.values(result.workspaces).filter((item) => item.status === "failed").length;
    const failedForwards = result.forwards.filter((item) => item.status === "failed").length;
    const failedCommands = result.commands.filter((item) => item.status === "failed").length;
    const canceledCommands = result.commands.filter((item) => item.status === "canceled").length;
    if (result.forwardError) setError(`转发规则读取失败：${result.forwardError}`);
    const failures = failedWorkspaces + failedForwards + failedCommands;
    pushToast(
      failures > 0 ? "error" : canceledCommands > 0 ? "info" : "success",
      `已打开「${template.name}」：${failedWorkspaces} 个连接失败，${failedForwards} 条转发失败，${failedCommands} 条命令失败${canceledCommands > 0 ? `，${canceledCommands} 条命令已取消` : ""}。项目筛选未切换`,
    );
  });

  const closeCurrent = () => run(async (epoch) => {
    if (activeProject === ALL_PROJECTS || !inventory) return;
    const results = await closeProject(activeProject as TemplateProjectFocus, inventory);
    if (isStale(epoch)) return;
    const failed = results.filter((item) => !item.closed);
    pushToast(
      failed.length > 0 ? "error" : "success",
      failed.length > 0
        ? `${failed.length} 个工作区未关闭；不支持后台保留的终端仍在运行`
        : `已关闭 ${results.length} 个项目工作区，可后台保留的终端均已 detach`,
    );
  });

  return (
    <section className="px-2 pb-2" aria-label="项目模板">
      <div className="flex items-center gap-1">
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm flex-1 justify-start"
          aria-expanded={expanded}
          onClick={() => setExpanded((value) => !value)}
        >
          项目模板（{templates.length}）
        </button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={busy} onClick={beginSave}>
          保存当前
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm"
          disabled={busy || activeProject === ALL_PROJECTS}
          title="关闭当前项目工作区；可保留的终端只 detach，不 kill"
          onClick={closeCurrent}
        >
          关闭项目
        </button>
      </div>
      {error && <div role="alert" className="mt-1 break-words text-[11px] text-red-300">{error}</div>}
      {draft && (
        <div className="mt-2 flex flex-col gap-2 rounded border border-neutral-800/70 p-2">
          <label className="text-[11px] text-neutral-400">
            模板名称
            <input
              className="nx-input nx-input-sm mt-1 w-full"
              aria-label="模板名称"
              value={draft.name}
              disabled={busy}
              onChange={(event) => setDraft({ ...draft, name: event.target.value })}
            />
          </label>
          <div className="text-[11px] text-neutral-500">为需要的终端填写启动命令；留空则不执行。凭据只保留引用，请勿在命令中填写密码或令牌。</div>
          {draft.workspaces.flatMap((workspace) => workspace.panes.flatMap((pane) => pane.tabs
            .filter((tab) => tab.kind === "terminal")
            .map((tab) => (
              <label key={tab.id} className="text-[11px] text-neutral-400">
                {templateTabLabel(workspace, tab)}
                <input
                  className="nx-input nx-input-sm mt-1 w-full font-mono"
                  aria-label={`启动命令：${templateTabLabel(workspace, tab)}`}
                  placeholder={tab.containerId ? "容器 exec 不自动恢复" : "例如 cd /srv/app && docker compose logs -f"}
                  value={draft.startupCommands[tab.id] ?? ""}
                  disabled={busy || Boolean(tab.containerId)}
                  onChange={(event) => setDraft({
                    ...draft,
                    startupCommands: { ...draft.startupCommands, [tab.id]: event.target.value },
                  })}
                />
              </label>
            ))))}
          <div className="flex justify-end gap-1">
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={busy} onClick={() => setDraft(null)}>取消</button>
            <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" disabled={busy || !draft.name.trim()} onClick={save}>保存模板</button>
          </div>
        </div>
      )}
      {expanded && !draft && (
        <div className="mt-1 flex flex-col gap-1">
          {templates.length === 0 ? (
            <span className="py-1 text-[11px] text-neutral-500">还没有项目模板</span>
          ) : templates.map((template) => (
            <div key={template.id} className="rounded border border-neutral-800/60 px-2 py-1.5 text-[11px]">
              <div className="truncate font-medium text-neutral-200" title={template.name}>{template.name}</div>
              <div className="mt-0.5 text-neutral-500">
                {projectFocusLabel(template.project, groups)} · {template.workspaces.length} 个工作区 · {Object.keys(template.startupCommands).length} 条命令
              </div>
              <div className="mt-1 flex flex-wrap gap-1">
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={busy} onClick={() => open(template)}>打开</button>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={busy} onClick={() => setActiveProject(template.project)}>聚焦</button>
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={busy} onClick={() => rename(template)}>重命名</button>
                <button type="button" className="nx-btn nx-btn-danger nx-btn-xs" disabled={busy} onClick={() => remove(template)}>删除</button>
              </div>
            </div>
          ))}
        </div>
      )}
      {productionPrompt && (
        <ProductionConfirmationDialog
          challenge={productionPrompt.challenge}
          contentHash={productionPrompt.challenge.inputHash}
          command={productionPrompt.command}
          onCancel={() => finishProductionPrompt(null)}
          onConfirm={finishProductionPrompt}
        />
      )}
    </section>
  );
}
