---
name: project-brief
description: Runs the initial briefing for a new project and saves it in the brief/v3 format, and the area briefing that adds an area or opens an area's next milestone. Use it whenever the user wants to start a new project, product, tool or feature ("I have an idea", "I was thinking of building", "let's do the briefing", "new project", or the same in any language), to add a front of work such as marketing or support, or to open the next milestone, when they say "save the brief", "close the brief", "the brief is ready", or when they ask to import or convert a brief in a project folder. Works on claude.ai (including from a phone, often with dictated messages) and in Claude Code, inside or outside a Jérôme project.
---

# Project brief

Read `references/briefing.md` for how to run the briefing and close it, and `references/brief-v3.md` for the format, before writing or importing a brief.

## Where the brief goes

**Inside a Jérôme project** (the folder contains `.jerome/project.json`; Jérôme's files live in `.jerome/`):
- Write the project files directly, as described under "Import" in `references/brief-v3.md`.
- Also save a copy of the full brief as `.jerome/brief.md`.

**Anywhere else** (claude.ai, phone, Claude Code outside a Jérôme project): show the brief in a single markdown block, so the user can copy it. They import it on their computer with `jerome new <name> --from-file <file>`, or `--from-file -` to read it from the clipboard through standard input.

Then reply in 2–3 lines: where the brief is, how many decisions and open questions it contains, and how to pick it up again.

## Import and conversion (Claude Code only)

`jerome new <name> --from-file` imports a valid brief/v3, brief/v2 or brief/v1 by itself, without you. You are needed in two cases.

**The user gives you a brief** (a file or pasted text) inside a Jérôme project:
1. Check the `format` field. `brief/v3` is imported as is, and `brief/v2` with one area `main`. `brief/v1` is accepted: map its section headings with the table in `references/brief-v3.md`. With any other declared format, stop and report the version.
2. Sort the sections into the files as described under "Import" in `references/brief-v3.md`, and confirm in one line.

**PROJECT.md is provisional** (the CLI could not read `.jerome/brief.md` as a valid brief and left an instruction to convert it):
1. Read `.jerome/brief.md`, the original text, and extract goal, context, constraints, decisions, open questions and the rest, following the format.
2. The text may come from another chat and contain proposals the user never confirmed. List every decision you found and ask the user to confirm them in one block, or to say which ones to drop. Unconfirmed ones go under Open questions.
3. Write PROJECT.md, scope.md, areas.md, decisions.md and open.md in `.jerome/` as described under "Import", replacing the provisional PROJECT.md. Leave `.jerome/brief.md` as it is.

**Adopting an existing project** (`jerome init` on a folder that already had code, so `.jerome/PROJECT.md` does not exist yet):
1. Before the briefing, read the project's own instructions, if any: `CLAUDE.md`, `.claude/CLAUDE.md`, `CLAUDE.local.md` and `.claude/rules/` other than `jerome.md`. They are loaded in every session and heartbeat too.
2. List the instructions that conflict with Jérôme's rules (`.claude/rules/jerome.md`), for example on branches and merges, pushing, chained commands, who edits the state files, or delegation, and ask the user how to settle each one in one block. Change none of those files yourself.
3. Run the briefing on the project as it is: read its README and a few key files rather than asking what they already say, and keep to what the user wants Jérôme to do from now on.

In every case, propose the team of agents only afterwards, as a separate step for the user to approve.

## Area briefing (Claude Code, inside a Jérôme project)

To add an area, or to open an area's next milestone when the current one is reached (often from a decision request). Read `.jerome/areas.md`, `.jerome/scope.md` and `.jerome/decisions.md` first, then follow "Area briefing" in `references/briefing.md`. Once the user confirms:
1. Write the area's section in `.jerome/areas.md` (format under "Areas" in `references/brief-v3.md`, plus its `Agents` line), and its context and out of scope under `## Area: <name>` in `.jerome/scope.md`. For a new milestone, only `Milestone` and `Criteria` change.
2. Record the confirmed decisions; a closed milestone is one line: `D…: <area>: <milestone> done → <next> — reason`. Create the area's folder if it is new.
3. Use the project-team skill to propose the team changes for the area. Nothing is created before the user confirms.
