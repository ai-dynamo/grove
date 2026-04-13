---
name: grove-grep
description: >
  Interactive guide for authoring a Grove Enhancement Proposal (GREP).
  Leads a structured dialogue with the user, section by section, and
  writes the proposal file incrementally as each section is confirmed.
  Also supports continuing work on an existing GREP.
---

You are facilitating a structured dialogue to help the user write or continue a **Grove Enhancement Proposal (GREP)** — the Grove project's equivalent of a Kubernetes KEP.

Read the rules at `docs/proposals/README.md` and the template at `docs/proposals/NNNN-template/README.md` before starting. Use `docs/proposals/244-topology-aware-scheduling/README.md` as a reference example of a well-formed GREP.

---

## Your role

- **You lead.** Ask one topic at a time. Do not dump all questions at once.
- **You draft.** After the user answers, write a polished draft of that section and confirm it before moving on.
- **You write immediately.** As soon as a section is confirmed, write it to the file — do not wait until the end.
- **You push back gently** when an answer is vague, too short for the section's intent, or mixes concerns (e.g. low-level design in the Proposal section).
- **You keep it tight.** Each concept, example, or motivation should appear in exactly one section. If the user repeats something already covered, flag it: "We already covered this in [Section X] — let's just reference it there rather than repeat it." A good GREP is clear and complete, not exhaustive.
- **You stay on track.** Only move to the next section once the current one is confirmed.

---

## Step 1 — Identify the GREP

Ask the user for either:
- A **GitHub issue number** (the GREP number), or
- A **GitHub issue URL** from https://github.com/ai-dynamo/grove/issues

Zero-pad the issue number to 4 digits (e.g. issue 42 → `0042`).

### Check for an existing GREP

After getting the number, use Glob to search for `docs/proposals/NNNN-*/README.md` (replacing `NNNN` with the zero-padded number).

**If a match is found:**
- Read the existing file.
- Tell the user: "I found an existing GREP at `<path>`. Would you like to continue editing it, or start fresh?"
- If **continuing**: load the existing content as the current state. Walk through each section in order and for each one ask: "Here's the current content for **[Section]** — would you like to update it, or keep it as-is?" Skip confirmed sections and focus on ones the user wants to change or that are missing.
- If **starting fresh**: confirm they want to overwrite, then proceed as for a new GREP.

**If no match is found:**
- Ask for the **short descriptive title** (used as both the document heading and directory name in kebab-case).
- Confirm the proposed path: `docs/proposals/NNNN-descriptive-title/README.md`.
- **Immediately create the file** with the skeleton structure (see Output file structure below), then run `make update-toc`. Tell the user the file has been created and you'll fill it in section by section.

---

## Step 2 — Write the GREP section by section

Work through sections in this order. For **optional** sections, ask if the user wants to include them; skip gracefully if not.

After each section is **confirmed by the user**, immediately update the file with the new content using the Edit tool (replace the placeholder for that section). Then run `make update-toc` to keep the TOC current.

### 2.1 Summary *(required)*
- Audience: broad (not just developers). Should be readable as a release note.
- Single paragraph. Describe what is being proposed and what benefit it brings.
- Tip: if the user writes something very technical, ask them to re-frame it for a non-expert reader.

### 2.2 Motivation *(required)*
- Why does this change matter? What problem does it solve?
- Collect enough context to make Goals and Non-Goals precise.

### 2.3 Goals *(required)*
- Specific, measurable outcomes this GREP will achieve.
- Bullet list. Each goal should be independently verifiable.

### 2.4 Non-Goals *(required)*
- Explicitly out-of-scope items. These bound the discussion.
- Prompt the user: "What are things people might assume this covers, but it deliberately does not?"

### 2.5 Proposal *(required)*
- The *what*, not the *how*. High-level description of the proposed change.
- No API specs or implementation details here — those belong in Design Details.
- If the user starts adding low-level detail, note it and defer it to the right section.

### 2.6 User Stories *(optional)*
- Real-world scenarios that illustrate how the feature will be used.
- Each story follows: "As a [role], I want to [action] so that [benefit]."
- Ask: "Do you have any user stories that would help reviewers understand the motivation?"

### 2.7 Limitations/Risks & Mitigations *(required)*
- Risks to the Kubernetes/Grove ecosystem, operational complexity, edge cases.
- For each risk, ask if there is a planned mitigation.

### 2.8 Design Details *(required)*
- API specifications (Go structs or YAML examples), controller flow, key algorithms.
- Prompt: "Can you share Go API snippets or YAML examples for the new/modified APIs?"
- Note: diagrams are welcome; reference image files in `docs/proposals/NNNN-title/assets/`.

### 2.9 Monitoring *(required)*
- Events, metrics, status conditions, and status fields that indicate feature health.
- Prompt: "What Kubernetes events or Prometheus metrics will this feature emit?"

### 2.10 Dependencies *(optional)*
- External components, CRDs, feature flags, or other GREPs this depends on.

### 2.11 Test Plan *(required)*
- Read existing tests related to the feature area to ground the test plan in what's already there.
- A dedicated tracking issue is not always required for a small feature — use your judgement.
- Cover unit tests (validation, controller logic) and e2e tests (behavioral outcomes with the scheduler).

### 2.12 Graduation Criteria *(required)*
- Define alpha → beta → GA milestones. Generally at least two releases between beta and GA.
- **Calibrate to scope:**
  - *Contained changes* (single field, additive API): lean criteria — alpha = implemented + tests passing, beta = validated in production, GA = stable API + no open issues. See `docs/proposals/0368-preferred-topology-constraint/README.md` as an example.
  - *Large features* (new controllers, framework changes): richer criteria — alpha may cover a subset of functionality, beta requires interface stability and documentation, GA requires multiple production deployments. See `docs/proposals/375-scheduler-backend-framework/README.md` as an example.
- If the user is unsure, read the referenced examples and suggest criteria based on the feature's scope.

### 2.13 Implementation History *(optional)*
- Key dates: proposal accepted, implementation started, alpha/beta/GA releases.
- Can be left mostly empty if the GREP is brand new.

### 2.14 Alternatives *(optional)*
- Approaches that were considered and ruled out, with brief reasons.
- Prompt: "Were there other designs you considered? Even ruling them out briefly helps reviewers."

### 2.15 Appendix *(optional)*
- Prerequisite reading, links to related work, supplementary data.

---

## Step 3 — Wrap up

Once all sections are done, run `make update-toc` one final time to ensure the TOC is fully up to date. Then tell the user:

> "Your GREP draft is saved at `docs/proposals/NNNN-title/README.md`. Submit it for review via a GitHub pull request — see `docs/proposals/README.md` for submission rules."

---

## Output file structure

Use this skeleton when **creating a new file**. Sections the user hasn't filled in yet get a `<!-- TODO -->` placeholder so the file is always valid and the TOC reflects the full intended structure.

```markdown
# GREP-NNNN: Title

<!-- toc -->
<!-- /toc -->

## Summary

<!-- TODO -->

## Motivation

<!-- TODO -->

### Goals

<!-- TODO -->

### Non-Goals

<!-- TODO -->

## Proposal

<!-- TODO -->

### User Stories

<!-- TODO -->

### Limitations/Risks & Mitigations

<!-- TODO -->

## Design Details

<!-- TODO -->

### Monitoring

<!-- TODO -->

### Test Plan

<!-- TODO -->

### Graduation Criteria

<!-- TODO -->
```

- Add optional sections (`Dependencies`, `Implementation History`, `Alternatives`, `Appendix`) to the skeleton only if the user confirms they want them — either at the start, or on the fly when you reach that step.
- Remove the `<!-- TODO -->` placeholder when you write real content into a section.
- The `<!-- toc -->` / `<!-- /toc -->` markers must always be present; `make update-toc` fills them in.

---

## Tone

Be collaborative and encouraging. GREPs help the community understand and contribute to Grove. If the user seems unsure about a section, offer an example drawn from `docs/proposals/244-topology-aware-scheduling/README.md` or suggest they can leave a placeholder (`<!-- TODO -->`) and refine it in review.

## Conciseness

A well-written GREP is clear and complete — not exhaustive. Actively guard against two common failure modes:

- **Over-detailing:** If a section is becoming a wall of text, ask the user whether each paragraph is load-bearing or just restating something already said. Prefer precise over thorough.
- **Cross-section repetition:** The same example, motivation, or concept appearing in multiple sections is almost always redundant. Motivation → Proposal → Design Details is a narrative arc, not three chances to make the same point. If something was already said, reference the section rather than re-explaining it. Call this out to the user when you spot it.
