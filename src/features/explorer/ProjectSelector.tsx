import { useUi } from "../../app/store";
import {
  ALL_PROJECTS,
  UNGROUPED_PROJECT,
  projectFocusForGroup,
  projectGroupId,
  topLevelProjects,
} from "../../app/projectFocus";
import type { AssetGroup } from "../../ipc/commands";
import { Select } from "../../ui/Select";

export function ProjectSelector({ groups }: { groups: readonly AssetGroup[] }) {
  const activeProject = useUi((state) => state.activeProject);
  const setActiveProject = useUi((state) => state.setActiveProject);
  const projects = topLevelProjects(groups);
  const selectedGroup = projectGroupId(activeProject);
  const selectedMissing = selectedGroup !== null && !projects.some((group) => group.id === selectedGroup);

  return (
    <div className="flex min-w-0 items-center px-2 pb-2">
      <Select
        className="min-w-0 flex-1"
        triggerClassName="nx-input nx-select-trigger h-[30px] text-xs"
        ariaLabel="项目筛选"
        title="仅过滤当前设备的显示，不会断开其他项目"
        value={activeProject}
        options={[
          { value: ALL_PROJECTS, label: "全部" },
          { value: UNGROUPED_PROJECT, label: "未分组" },
          ...projects.map((group) => ({ value: projectFocusForGroup(group.id), label: group.name })),
          ...(selectedMissing ? [{ value: activeProject, label: "当前项目（不可用）" }] : []),
        ]}
        onChange={(value) => setActiveProject(value as typeof activeProject)}
      />
    </div>
  );
}
