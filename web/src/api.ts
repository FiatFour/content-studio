export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

// Called when any request other than a sign-in attempt gets 401, so the app can show the login screen
// instead of a broken page. The server gives no reason; the UI does not guess one.
let onUnauthorized: (() => void) | null = null;
export const setUnauthorizedHandler = (fn: (() => void) | null) => { onUnauthorized = fn; };

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
  if (resp.status === 401 && !path.startsWith("/auth/login") && !path.startsWith("/auth/enroll")) onUnauthorized?.();
  if (!resp.ok) throw new ApiError(resp.status, (data as { error?: string }).error ?? `HTTP ${resp.status}`);
  return data as T;
}

export type AuthStatus = { enrolled: boolean; authenticated: boolean };
export type Passkey = { id: number; label: string; transports: string[]; created_at: string; last_used_at: string | null };
export type PasskeyList = { credentials: Passkey[]; current_credential_id: number | null };

export const contentUrl = (artifactId: number) => `/api/artifacts/${artifactId}/content`;

export type Job = {
  id: number; stage: string; state: string; attempt: number; progress_done: number; progress_total: number;
  provider: string | null; model: string | null; error_class: string | null; error_message: string | null; wait_reason: string | null;
  started_at: string | null; finished_at: string | null; updated_at: string;
};
export type JobInspection = Job & {
  run_id: number; max_attempts: number; run_after: string | null; lease_expires_at: string | null; heartbeat_at: string | null;
  lease_expired: boolean | null; execution: { input_hash: string; attempt: number; at: string } | null;
  resume: { verdict: "safe" | "unsafe" | "unknown" | "not_applicable"; reason: string };
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
export type QA = { artifact_id: number; created_at: string; rule: string; rule_version: number; severity: "blocking" | "warning" | "advisory"; passed: boolean; reason: string; suggested_fix: string };
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
export type NotifEvent = { event: string; enabled: boolean; default: boolean; implemented: boolean };
export type NotifDelivery = { event: string; ok: boolean; http_status: number | null; error: string; at: string };
export type Notifications = {
  configured: boolean; suffix?: string; events: NotifEvent[]; recent_deliveries: NotifDelivery[]; last_delivery?: NotifDelivery;
};
export type NotifTestResult = { ok: boolean; http_status: number | null; error: string };
export type Resource = { name: string; max_concurrent: number; running: number; waiting: number; safe_max: number; max: number };
export type ProviderHealth = {
  name: string; capability: string; endpoint: string; state: "up" | "down" | "unavailable"; latency_ms?: number; http_status?: number; reason?: string; checked_at: string;
};
export type Monitor = {
  gpu: { available: boolean; name?: string; util_pct?: number; temp_c?: number; vram_used_mib?: number; vram_total_mib?: number };
  disks: { name: string; free_gib: number | null; total_gib: number | null }[];
  queue: { queued: number; running: number; waiting_for_resource: number; retry_scheduled: number };
  gpu_slot: { max_concurrent: number; running: number; waiting: number; stage: string | null; episode: string | null };
};
export type Policies = { tasks: { task: string; capability: string; policy: string }[]; cloud_enabled: boolean; changeable: boolean };
export type System = {
  cpu_pct?: number; ram_used_mib?: number; ram_total_mib?: number; vram_used_mib?: number; vram_total_mib?: number;
  disk_free_gib?: number; disk_total_gib?: number; gpu_slot_stage: string | null; gpu_slot_episode: string | null;
  running_jobs: number; episodes_ready_today: number; cloud_spend: null;
};
export type CanonProposal = {
  id: number; kind: string; payload: unknown; rationale: string; proposed_by: string; episode_id: number | null; created_at: string;
  status: "proposed" | "approved" | "rejected"; decided_by: string | null; reason: string | null; decided_at: string | null;
};
export type CanonImpact = {
  proposal_id: number; kind: string; status: string; advisory: boolean;
  impact: {
    characters: { slug: string; name: string; exists: boolean }[];
    relationships: { id: number; from: string; to: string; type: string }[];
    episodes: { id: number; number: number; title: string; in_story_date: string | null; reason: string }[];
  };
  findings: { code: string; severity: "conflict" | "warning" | "info"; message: string }[];
};
