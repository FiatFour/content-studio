CREATE TABLE projects (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL,
    profile     text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE episodes (
    id                 bigserial PRIMARY KEY,
    project_id         bigint NOT NULL REFERENCES projects(id),
    number             int NOT NULL,
    title              text NOT NULL,
    idea               text NOT NULL,
    target_duration_s  int NOT NULL CHECK (target_duration_s BETWEEN 60 AND 120),
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, number)
);

CREATE TABLE workflow_runs (
    id             bigserial PRIMARY KEY,
    episode_id     bigint NOT NULL REFERENCES episodes(id),
    profile        text NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('full', 'rerender')),
    source_run_id  bigint REFERENCES workflow_runs(id),
    state          text NOT NULL,
    cancel_requested boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz
);

CREATE TABLE jobs (
    id              bigserial PRIMARY KEY,
    run_id          bigint NOT NULL REFERENCES workflow_runs(id),
    stage           text NOT NULL,
    seq             int NOT NULL,
    state           text NOT NULL,
    attempt_count   int NOT NULL DEFAULT 0,
    error_class     text,
    error_message   text,
    run_after       timestamptz,
    progress_done   int NOT NULL DEFAULT 0,
    progress_total  int NOT NULL DEFAULT 0,
    provider        text,
    model           text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz,
    UNIQUE (run_id, stage)
);
CREATE INDEX jobs_claim_idx ON jobs (state, run_after, id);

CREATE TABLE job_attempts (
    id             bigserial PRIMARY KEY,
    job_id         bigint NOT NULL REFERENCES jobs(id),
    n              int NOT NULL,
    started_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz,
    outcome        text,
    error_class    text,
    error_message  text,
    UNIQUE (job_id, n)
);

CREATE TABLE artifacts (
    id                 bigserial PRIMARY KEY,
    project_id         bigint NOT NULL REFERENCES projects(id),
    episode_id         bigint NOT NULL REFERENCES episodes(id),
    run_id             bigint NOT NULL REFERENCES workflow_runs(id),
    job_id             bigint NOT NULL REFERENCES jobs(id),
    type               text NOT NULL,
    item_key           text NOT NULL DEFAULT '',
    version            int NOT NULL,
    storage_path       text NOT NULL,
    sha256             text NOT NULL,
    size_bytes         bigint NOT NULL,
    validation_status  text NOT NULL,
    provider           text NOT NULL DEFAULT '',
    model              text NOT NULL DEFAULT '',
    meta               jsonb NOT NULL DEFAULT '{}',
    license            jsonb NOT NULL DEFAULT '{}',
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (episode_id, type, item_key, version)
);
CREATE INDEX artifacts_run_idx ON artifacts (run_id, type);

CREATE TABLE artifact_parents (
    artifact_id  bigint NOT NULL REFERENCES artifacts(id),
    parent_id    bigint NOT NULL REFERENCES artifacts(id),
    PRIMARY KEY (artifact_id, parent_id)
);

CREATE TABLE qa_results (
    id             bigserial PRIMARY KEY,
    artifact_id    bigint NOT NULL REFERENCES artifacts(id),
    rule           text NOT NULL,
    rule_version   int NOT NULL,
    severity       text NOT NULL CHECK (severity IN ('blocking', 'warning', 'advisory')),
    passed         boolean NOT NULL,
    reason         text NOT NULL,
    suggested_fix  text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE approvals (
    id           bigserial PRIMARY KEY,
    artifact_id  bigint NOT NULL REFERENCES artifacts(id),
    sha256       text NOT NULL,
    decision     text NOT NULL CHECK (decision IN ('approved', 'rejected', 'changes_requested')),
    notes        text NOT NULL DEFAULT '',
    reviewer     text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX approvals_artifact_idx ON approvals (artifact_id, id DESC);

CREATE TABLE exports (
    id           bigserial PRIMARY KEY,
    approval_id  bigint NOT NULL REFERENCES approvals(id),
    artifact_id  bigint NOT NULL REFERENCES artifacts(id),
    export_path  text NOT NULL,
    sha256       text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_settings (
    user_id  text PRIMARY KEY,
    ui_lang  text NOT NULL CHECK (ui_lang IN ('th', 'en'))
);
