export type State = "green" | "amber" | "red" | "unknown" | "partial" | "stale" | string;
export type Coverage = "known" | "partial" | "unmeasured" | "unknown" | string;

export interface Envelope<T> {
  data: T;
  next_cursor?: string;
  at?: string;
}

export interface SessionStats {
  runs?: number;
  successes?: number;
  failures?: number;
  success_rate?: number;
  success?: number;
  retries?: number;
  duration_ms?: number;
  tokens?: number;
  spent_usd?: number;
  spent_usd_known_runs?: number;
  peak_rss?: number;
  rss_known_steps?: number;
  coverage?: number;
  measured_steps?: number;
  steps?: number;
}

export interface Session {
  id: string;
  name?: string;
  name_basis?: string;
  active?: boolean;
  state?: State;
  primary_project?: string;
  projects?: string[];
  origin?: { client?: string; surface?: string; transport?: string };
  started_at?: string;
  updated_at?: string;
  closed_at?: string;
  stats?: SessionStats;
  capabilities?: string[];
  implementations?: string[];
  providers?: string[];
  tools?: string[];
  runs?: Run[];
  timeline?: TimelineEntry[];
  graph?: { nodes?: GraphNode[]; edges?: GraphEdge[] };
}

export interface TimelineEntry {
  id: string;
  at?: string;
  kind?: string;
  run_id?: string;
  step_id?: string;
  capability?: string;
  implementation?: string;
  provider?: string;
  repository?: string;
  state?: State;
  reason?: string;
  duration_ms?: number;
  tokens?: number;
}

export interface GraphNode {
  id: string;
  kind?: string;
  label?: string;
  state?: State;
  run_id?: string;
  step_id?: string;
  provider?: string;
  tool?: string;
  duration_ms?: number;
  tokens?: number;
  tokens_known?: boolean;
}

export interface GraphEdge {
  from?: string;
  to?: string;
  source?: string;
  target?: string;
  kind?: string;
}

export interface Run {
  id: string;
  session?: string;
  session_id?: string;
  state?: State;
  verdict?: string;
  task?: string;
  project?: string;
  repositories?: string[];
  projects?: string[];
  started_at?: string;
  finished_at?: string;
  duration_ms?: number;
  steps?: Array<{ id?: string; state?: State; capability?: string; implementation?: string; provider?: string; tool?: string; duration_ms?: number; ended_at?: string; tokens?: number; tokens_known?: boolean }>;
}

export interface WorkflowPoint {
  id: string;
  title: string;
  state: State;
  evidence?: string[];
  retired?: boolean;
}

export interface WorkflowTelemetry {
  point_id: string;
  agent_runs?: number;
  tool_uses?: number;
  agent_duration?: number;
  tool_duration?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  measured?: number;
  estimated?: number;
  partial?: number;
  unknown?: number;
}

export interface WorkflowActivity {
  cursor?: number;
  point_id?: string;
  agent_run_id?: string;
  invocation_id?: string;
  tool?: string;
  requested_model?: string;
  observed_model?: string;
  requested_reasoning_effort?: string;
  observed_reasoning_effort?: string;
  markdown?: string;
  at?: string;
}

export interface WorkflowDetail {
  id: string;
  task?: string;
  repository?: string;
  state?: State;
  started_at?: string;
  finished_at?: string;
  active_duration_ms?: number;
  progress?: { revision?: number; points?: WorkflowPoint[] };
  telemetry?: WorkflowTelemetry[];
  activity?: WorkflowActivity[];
  activity_cursor?: number;
  activity_has_more?: boolean;
}

export interface Overview {
  at?: string;
  range?: string;
  snapshot?: { light?: State; uptime_ms?: number; active_sessions?: number; active_runs?: number };
  sessions?: number | Session[] | { total?: number; active?: number };
  runs?: number | { total?: number; active?: number };
  stats?: Record<string, unknown> & { success_rate?: number; duration_ms?: number; tokens?: number; cost?: number };
  trends?: Record<string, unknown>;
}

export interface EventRecord {
  seq: number;
  ts?: string;
  kind?: string;
  type?: string;
  session_id?: string;
  run_id?: string;
  step_id?: string;
  state?: State;
  [key: string]: unknown;
}
