---
name: docs
description: Brings the entries of .jerome/docs-queue.md into the project documentation with the documentarist agent.
disable-model-invocation: true
---

# Docs

1. If `.jerome/docs-queue.md` is missing or has no entries (lines starting with `- `), say that the documentation is up to date and stop.
2. If `.claude/agents/documentarist.md` is missing, say that the project has no documentarist: offer to add it with the project-team skill, or to run `jerome docs` in a terminal. Stop.
3. Run the documentarist with the Agent tool, passing the project folder and nothing else: it reads the queue and the files itself.
4. Report its reply to the user briefly.
