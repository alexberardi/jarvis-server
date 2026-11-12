-- A taught workflow's skill (docs/agent/WORKFLOW-SKILLS.md): its goal, when to use it, its
-- prerequisites, guardrails and notes, as JSON (computer.Skill). Each step's goal and
-- expectation are in steps. '{}' is a workflow taught before skills: it runs as it did.
-- +goose Up
ALTER TABLE cc_taught_workflows ADD COLUMN skill TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE cc_taught_workflows DROP COLUMN skill;
