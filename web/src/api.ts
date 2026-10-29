export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

export async function api<T>(path: string, init?: { method?: string; body?: unknown }): Promise<T> {
  const headers: Record<string, string> = {};
  if (init?.body !== undefined) headers["Content-Type"] = "application/json";
  let resp: Response;
  try {
    resp = await fetch(`/api${path}`, { method: init?.method ?? "GET", headers, body: init?.body === undefined ? undefined : JSON.stringify(init.body) });
  } catch {
    throw new ApiError(0, "network");
  }
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new ApiError(resp.status, (data as { error?: string }).error ?? `HTTP ${resp.status}`);
  return data as T;
}

export const contentUrl = (artifactId: number) => `/api/artifacts/${artifactId}/content`;

export type Job = {
  id: number; stage: string; state: string; attempt: number; progress_done: number; progress_total: number;
  provider: string | null; model: string | null; error_class: string | null; error_message: string | null;
  started_at: string | null; finished_at: string | null; updated_at: string;
};
export type Run = {
  id: number; kind: "full" | "rerender" | "rewrite"; parent_run_id: number | null; feedback: string | null; state: string; created_at: string;
  finished_at: string | null; stages: string[]; jobs: Job[];
};
export type EpisodeSummary = { id: number; number: number; title: string; target_duration_s: number; created_at: string; latest_run: Run | null };
export type VideoVersion = {
  id: number; run_id: number; version: number; sha256: string; size_bytes: number; validation_status: string;
  created_at: string; decision: string | null; exported: boolean;
};
export type ArtifactRow = { id: number; type: string; item_key: string; version: number; sha256: string; validation_status: string; provider: string; model: string; meta: Record<string, unknown> };
export type QA = { rule: string; rule_version: number; severity: "blocking" | "warning" | "advisory"; passed: boolean; reason: string; suggested_fix: string };
export type Review = {
  video: VideoVersion; episode_id: number; qa: QA[];
  decisions: { id: number; sha256: string; decision: string; notes: string; reviewer: string; created_at: string }[];
  exports: { id: number; file_name: string; sha256: string; created_at: string }[];
  script_artifact_id: number | null; subtitles_artifact_id: number | null; versions: VideoVersion[];
};
export type CharacterStatus = "alive" | "deceased" | "moved_away";
export type Character = {
  id: number; slug: string; name: string; appearance: string; personality: string; voice: string;
  arrives_on: string | null; status: CharacterStatus; status_since: string | null; lora_name: string; trigger_token: string;
};
export type EpisodeSummaryText = { summary: string; characters_present: string[]; open_threads: string[]; updated_at: string };
export type System = {
  cpu_pct?: number; ram_used_mib?: number; ram_total_mib?: number; vram_used_mib?: number; vram_total_mib?: number;
  disk_free_gib?: number; disk_total_gib?: number; gpu_slot_stage: string | null; gpu_slot_episode: string | null;
  running_jobs: number; episodes_ready_today: number; cloud_spend: null;
};
