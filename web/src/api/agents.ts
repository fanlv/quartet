import type { AgentInfo } from '../types';
import { readAPIResponse } from './response';

interface AgentListResponse {
  code: number;
  agent_list?: AgentInfo[];
  workdir?: string;
  job_enable?: boolean;
}

export interface AvailableAgentList {
  agents: AgentInfo[];
  workdir: string;
  jobEnable: boolean;
}

export async function fetchAvailableAgentList(): Promise<AvailableAgentList> {
  const response = await fetch('/api/v1/agent/list');
  const data = await readAPIResponse<AgentListResponse>(response);
  if (!Array.isArray(data.agent_list)) {
    throw new Error('GET /api/v1/agent/list: response is missing agent_list');
  }
  return {
    agents: data.agent_list.filter((agent) => agent.available !== false),
    workdir: data.workdir || '',
    jobEnable: !!data.job_enable,
  };
}
