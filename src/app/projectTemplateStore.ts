import { DESKTOP } from "../ipc/env";
import { getAccountPreferenceStore, type AccountPreferenceStore } from "./preferences";
import {
  normalizeProjectTemplates,
  templateId,
  type ProjectTemplate,
} from "./projectTemplates";

export const PROJECT_TEMPLATES_PREFERENCE_KEY = "project.workspaceTemplates";
const DEVICE_STORAGE_KEY = "nexterm.projectTemplates.v1";

export interface ProjectTemplateStorage {
  read(): Promise<unknown>;
  write(templates: ProjectTemplate[]): Promise<void>;
}

export class DeviceProjectTemplateStorage implements ProjectTemplateStorage {
  constructor(private readonly storage: Pick<Storage, "getItem" | "setItem"> = window.localStorage) {}

  async read(): Promise<unknown> {
    const raw = this.storage.getItem(DEVICE_STORAGE_KEY);
    return raw === null ? [] : JSON.parse(raw);
  }

  async write(templates: ProjectTemplate[]): Promise<void> {
    this.storage.setItem(DEVICE_STORAGE_KEY, JSON.stringify(templates));
  }
}

export class OwnerProjectTemplateStorage implements ProjectTemplateStorage {
  constructor(private readonly preferences: AccountPreferenceStore) {}

  async read(): Promise<unknown> {
    const view = await this.preferences.getView();
    return view.overrides[PROJECT_TEMPLATES_PREFERENCE_KEY] ?? [];
  }

  async write(templates: ProjectTemplate[]): Promise<void> {
    await this.preferences.putOverrides({ [PROJECT_TEMPLATES_PREFERENCE_KEY]: templates });
  }
}

export function createProjectTemplateStorage(): ProjectTemplateStorage {
  if (DESKTOP) return new DeviceProjectTemplateStorage();
  const preferences = getAccountPreferenceStore();
  if (!preferences) throw new Error("账号偏好存储尚未就绪，请重新登录后再试");
  return new OwnerProjectTemplateStorage(preferences);
}

export class ProjectTemplateRepository {
  private queue: Promise<void> = Promise.resolve();

  constructor(private readonly storage: ProjectTemplateStorage) {}

  private run<T>(operation: () => Promise<T>): Promise<T> {
    const result = this.queue.then(operation, operation);
    this.queue = result.then(() => undefined, () => undefined);
    return result;
  }

  list(): Promise<ProjectTemplate[]> {
    return this.run(async () => normalizeProjectTemplates(await this.storage.read()));
  }

  create(template: Omit<ProjectTemplate, "id" | "createdAt" | "updatedAt"> & { id?: string }): Promise<ProjectTemplate> {
    return this.run(async () => {
      const templates = normalizeProjectTemplates(await this.storage.read());
      const name = template.name.trim().toLocaleLowerCase();
      if (templates.some((candidate) => candidate.name.trim().toLocaleLowerCase() === name)) {
        throw new Error("同名项目模板已存在");
      }
      const now = Date.now();
      const created: ProjectTemplate = { ...template, id: template.id ?? templateId(), createdAt: now, updatedAt: now };
      if (templates.some((candidate) => candidate.id === created.id)) throw new Error("项目模板标识已存在");
      await this.storage.write([...templates, created]);
      return created;
    });
  }

  saveAs(template: ProjectTemplate): Promise<ProjectTemplate> {
    return this.create({ ...template, id: undefined });
  }

  rename(id: string, name: string): Promise<ProjectTemplate> {
    return this.run(async () => {
      const normalized = name.trim();
      if (!normalized) throw new Error("模板名称不能为空");
      if (normalized.length > 80) throw new Error("模板名称不能超过 80 个字符");
      const templates = normalizeProjectTemplates(await this.storage.read());
      const index = templates.findIndex((template) => template.id === id);
      if (index < 0) throw new Error("项目模板不存在");
      if (templates.some((template, candidateIndex) =>
        candidateIndex !== index && template.name.toLocaleLowerCase() === normalized.toLocaleLowerCase()
      )) {
        throw new Error("同名项目模板已存在");
      }
      const renamed = { ...templates[index], name: normalized, updatedAt: Date.now() };
      templates[index] = renamed;
      await this.storage.write(templates);
      return renamed;
    });
  }

  delete(id: string): Promise<void> {
    return this.run(async () => {
      const templates = normalizeProjectTemplates(await this.storage.read());
      if (!templates.some((template) => template.id === id)) throw new Error("项目模板不存在");
      await this.storage.write(templates.filter((template) => template.id !== id));
    });
  }
}

export function createProjectTemplateRepository(): ProjectTemplateRepository {
  return new ProjectTemplateRepository(createProjectTemplateStorage());
}
