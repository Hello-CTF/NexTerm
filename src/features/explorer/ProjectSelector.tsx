import { useUi } from "../../app/store";
import {
  ALL_PROJECTS,
  UNGROUPED_PROJECT,
  projectFocusForGroup,
  projectGroupId,
  topLevelProjects,
} from "../../app/projectFocus";
import type { AssetGroup } from "../../ipc/commands";
import { ProjectTemplatePanel } from "./ProjectTemplatePanel";

export function ProjectSelector({ groups }: { groups: readonly AssetGroup[] }) {
  const activeProject = useUi((state) => state.activeProject);
  const setActiveProject = useUi((state) => state.setActiveProject);
  const projects = topLevelProjects(groups);
  const selectedGroup = projectGroupId(activeProject);
  const selectedMissing = selectedGroup !== null && !projects.some((group) => group.id === selectedGroup);

  return (
    <>
      <div className="px-2 pb-2">
        <select
          className="nx-select h-[30px] w-full text-xs"
          aria-label="项目筛选"
          title="仅过滤当前设备的显示，不会断开其他项目"
          value={activeProject}
          onChange={(event) => setActiveProject(event.target.value as typeof activeProject)}
        >
          <option value={ALL_PROJECTS}>全部</option>
          <option value={UNGROUPED_PROJECT}>未分组</option>
          {projects.map((group) => (
            <option key={group.id} value={projectFocusForGroup(group.id)}>{group.name}</option>
          ))}
          {selectedMissing && <option value={activeProject}>当前项目（不可用）</option>}
        </select>
      </div>
      <ProjectTemplatePanel groups={groups} />
    </>
  );
}
