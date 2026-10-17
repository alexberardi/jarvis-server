-- Each household's active prompt (docs/cc/prompt-contract.md §8, "Per household"): a built-in or an
-- imported prompt, the household's edit of it, and the prompt's own LLM values. No row: the
-- install's default built-in (llm.prompt_provider). original is the imported text (NULL for a
-- built-in, whose embedded file is the original); text NULL means unedited; tool_calling and
-- thinking_switch NULL mean "from the model".
-- +goose Up
CREATE TABLE cc_household_prompts (
    household_id    TEXT PRIMARY KEY,
    source          TEXT NOT NULL CHECK (source IN ('builtin', 'import')),
    name            TEXT NOT NULL,
    original        TEXT,
    text            TEXT,
    tool_calling    TEXT CHECK (tool_calling IN ('native', 'text')),
    thinking_switch TEXT CHECK (thinking_switch IN ('suffix', 'none')),
    updated_at      INTEGER NOT NULL
);

-- +goose Down
DROP TABLE cc_household_prompts;
