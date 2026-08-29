export interface ModelInfoACP {
  description?: string;
  modelId: string;
  name: string;
}

export interface SessionModelState {
  availableModels: ModelInfoACP[];
  currentModelId: string;
}

export interface ACPSessionMode {
  description?: string;
  id: string;
  name: string;
}

export interface SessionModeState {
  availableModes: ACPSessionMode[];
  currentModeId: string;
}

export interface ACPThoughtLevel {
  description?: string;
  id: string;
  name: string;
}

export interface SessionThoughtLevelState {
  availableThoughtLevels: ACPThoughtLevel[];
  currentThoughtLevelId: string;
  configId?: string;
}

export interface AgentInfo {
  agent_id: string;
  revision?: string;
  type: string;
  model_id: string;
  display_name: string;
  icon_url: string;
  availability?: string;
  available: boolean;
  refreshing?: boolean;
  error?: string;
  capabilities?: string[];
  models?: SessionModelState;
  modes?: SessionModeState;
  thoughtLevels?: SessionThoughtLevelState;
}
