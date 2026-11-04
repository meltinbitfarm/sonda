---
name: project-team
description: Proposes, creates and changes the project's subagents (.claude/agents/) from the agent catalog, area by area. Use it for a team or agent request, a new area or milestone that needs agents, a change of an agent's model or tools, and whenever PROJECT.md's Agent limits change.
---

# Project team

The catalog is in `agents/`, next to this file. Each agent declares its model, its tools and `omitClaudeMd: true`.

| Agent | Model | For |
|---|---|---|
| developer | opus | code: features, fixes, refactors, tests |
| writer | opus | text that stays: specifications, content, copy |
| researcher | sonnet | research on the web and in the project |
| analyst | sonnet | analysis of code, data or documents, reviews |
| clerk | haiku | mechanical work: counts, extractions, formatting |
| documentarist | haiku | brings `.jerome/docs-queue.md` into `docs/`, with `/docs` |

The model follows the kind of work, not a hierarchy: opus for code and writing that stays, sonnet for research and analysis, haiku for mechanical work.

Every agent must also work in a heartbeat, where nobody answers: never give one a tool that waits for the user, such as AskUserQuestion, nor write in its instructions that it asks, runs something in the background or waits for an approval. An agent that needs a decision says so in its reply, and the lead agent opens the request.

## Proposing the team

1. Read `.jerome/areas.md`, `.jerome/scope.md` and `.jerome/decisions.md`, and list the files in `.claude/agents/`.
2. Propose, area by area, the smallest team that covers the work of its current milestone, reusing agents that already exist. For each agent: name, its area (or areas), what it will do there, model, tools, and why it is needed. Start from the catalog; adapt an agent (name, description, tools) or write a new one only when no catalog agent fits, saying why. Include the documentarist whenever other agents will change things.
3. Keep the catalog's model unless the project gives a reason; say so when you propose a different one.
4. End with a decision request. Create nothing before the user explicitly confirms.

## Creating the agents (after confirmation)

1. Copy each confirmed agent from `agents/` to `.claude/agents/<name>.md`, with the confirmed changes. Keep `model` declared and `omitClaudeMd: true`. Agent files stay in `.claude/agents/` whatever their area.
2. List each agent in the `Agents` line of its areas in `.jerome/areas.md`. An agent in several areas is listed in each. Without `areas.md`, the project has a single implicit area: skip this step.
3. In its `## Project limits` section, copy word for word, with their IDs, the Agent limits from PROJECT.md that apply to that agent; leave `- none` if none applies. Agents do not read PROJECT.md: this section is the only way they know the project's limits.
4. Record one decision in `.jerome/decisions.md`: the team, each agent with its area and model, and the reason.
5. Tell the user in one line that the team is ready. If the Agent tool does not list the new agents yet, ask them to restart the session.

## Changing the team

- An agent's model, tools or role change only when the user asks or confirms: edit its file and record the decision. Never change them on your own initiative. If you see a reason (an agent keeps failing on its model, or a cheaper one would do), propose it with a decision request.
- When PROJECT.md's Agent limits change, update the `## Project limits` section of every agent in the same turn, as part of recording that change.
- Remove an agent only after confirmation: delete its file, take it out of `areas.md` and record the decision.
- `jerome update` never touches `.claude/agents/`: changes to the catalog reach existing agents only if the user asks you to apply them.
