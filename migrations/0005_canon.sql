-- M1b minimal story bible (canon.md): characters, an in-story date per episode, one owner-written
-- summary per episode. Agents never write these tables; only the owner does, through the API.
CREATE TABLE characters (
    id             bigserial PRIMARY KEY,
    project_id     bigint NOT NULL REFERENCES projects(id),
    slug           text NOT NULL CHECK (slug ~ '^[a-z][a-z0-9]{0,15}$'),
    name           text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    appearance     text NOT NULL CHECK (char_length(appearance) BETWEEN 5 AND 600),
    personality    text NOT NULL DEFAULT '' CHECK (char_length(personality) <= 600),
    voice          text NOT NULL CHECK (char_length(voice) BETWEEN 5 AND 200),
    arrives_on     date,
    status         text NOT NULL DEFAULT 'alive' CHECK (status IN ('alive', 'deceased', 'moved_away')),
    status_since   date,
    lora_name      text NOT NULL DEFAULT '' CHECK (lora_name ~ '^[A-Za-z0-9_.-]{0,100}$'),
    trigger_token  text NOT NULL DEFAULT '' CHECK (char_length(trigger_token) <= 60),
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug),
    CHECK (status = 'alive' OR status_since IS NOT NULL)
);

ALTER TABLE episodes ADD COLUMN in_story_date date;

CREATE TABLE episode_summaries (
    episode_id          bigint PRIMARY KEY REFERENCES episodes(id),
    summary             text NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 2000),
    characters_present  text[] NOT NULL DEFAULT '{}',
    open_threads        text[] NOT NULL DEFAULT '{}',
    updated_at          timestamptz NOT NULL DEFAULT now()
);
