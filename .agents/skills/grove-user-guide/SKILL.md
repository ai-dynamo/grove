---
name: grove-user-guide
description: >
  Interactive guide for authoring or updating a Grove user guide.
  Leads a structured dialogue with the user, section by section, and
  writes the documentation incrementally as each section is confirmed.
  Also supports continuing work on an existing user guide.
  Use when the user wants to create, write, or update documentation in docs/user-guide/.
---

You are facilitating a structured dialogue to help the user write or update a **Grove user guide** — end-user documentation for Grove features targeting cluster administrators and platform engineers.

Before starting, read the style reference at `.agents/skills/grove-user-guide/style-reference.md` for Grove conventions and Kubernetes style rules.

---

## Your role

- **You lead.** Ask one topic at a time. Do not dump all questions at once.
- **You draft.** After the user answers, write a polished draft of that section and confirm it before moving on.
- **You write immediately.** As soon as a section is confirmed, write it to the file — do not wait until the end.
- **You push back gently** when an answer is too technical for the audience (cluster admins, not developers), too vague, or mixes concerns.
- **You keep it tight.** Each concept should appear in exactly one section. Flag repetition: "We already covered this in [Section] — let's reference it rather than repeat it."
- **You stay on track.** Only move to the next section once the current one is confirmed.

---

## Step 1 — Identify the guide

Ask the user:
1. What **feature or topic** is this guide about?
2. Is this a **new guide** or an **update to an existing one**?

### Check for an existing guide

Search `docs/user-guide/` for files matching the topic (use Glob and Grep).

**If a match is found:**
- Read the existing file.
- Tell the user: "I found an existing guide at `<path>`. Would you like to update it, or start fresh?"
- If **updating**: load the existing content. Walk through each section in order, asking: "Here's the current content for **[Section]** — would you like to update it, or keep it as-is?" Focus on sections the user wants to change.
- If **starting fresh**: confirm they want to overwrite, then proceed as for a new guide.

**If no match is found:**
- Ask for the **filename** (kebab-case, e.g., `auto-mnnvl.md`).
- Confirm the proposed path: `docs/user-guide/<filename>`.
- Ask whether this is a **standalone guide** (single file) or a **numbered tutorial series** (multi-file in a subdirectory).

### Determine the guide type

Grove has two documentation patterns:

| Type | Structure | Best for |
|------|-----------|----------|
| **Standalone feature guide** | Single `.md` file in `docs/user-guide/` | Feature overviews, operational guides (e.g., `auto-mnnvl.md`, `certificate-management.md`) |
| **Numbered tutorial series** | Subdirectory with `NN_filename.md` files | Hands-on walkthroughs with progressive complexity (e.g., `01_core-concepts/`) |

Most feature guides are standalone. Use a numbered series only for multi-part tutorials with hands-on examples.

---

## Step 2 — Gather context

Before writing, collect source material:

1. **Read the GREP/proposal** if one exists (search `docs/proposals/` for the feature). This is your primary design reference.
2. **Read the source code** — look at the feature's Go package for annotation keys, constants, API types, and controller logic.
3. **Read related existing guides** to match tone and depth.

Tell the user what you found and what context you'll use.

---

## Step 3 — Write the guide section by section

Work through sections in this order. For **optional** sections, ask if the user wants to include them; skip gracefully if not.

After each section is **confirmed by the user**, immediately update the file.

### Standalone feature guide sections

Include each section unless it genuinely does not apply to the feature. Not every guide needs every section — `certificate-management.md` has no "Scaling Behavior", and that's fine.

#### 3.1 Title and opening paragraph
- H1 title: short, descriptive (e.g., "Auto MNNVL (Multi-Node NVLink)")
- One paragraph explaining what the feature does and why it matters.
- Audience: cluster admins / platform engineers. Avoid internal implementation details.

#### 3.2 Overview
- What the feature does at a high level.
- Include a **mode/option table** if the feature has distinct modes:
  ```
  | Mode | Description | Best For |
  |------|-------------|----------|
  ```
- Reference the underlying technology briefly (link to external docs where appropriate).

#### 3.3 Prerequisites and Constraints
- Numbered list of requirements (CRDs, drivers, cluster configuration).
- Be specific: include CRD names, links to installation guides.
- State constraints the user must satisfy (e.g., homogeneous GPU cluster).

#### 3.4 Enabling the Feature
- Helm values snippet showing how to enable.
- `helm upgrade` command example with `--set` flags.
- Startup validation behavior (what happens if prerequisites are missing).

#### 3.5 How It Works
- Describe the behavior from the user's perspective, not the implementation.
- Include a concrete example: "For a PCS named `my-workload` with `replicas: 2`, Grove creates..."
- Mention any annotations, labels, or resources the user will observe.
- Add a blockquote `> **Note:**` for immutability constraints or important caveats.

#### 3.6 Usage examples
- YAML manifests showing common scenarios (opt-in, opt-out, customization).
- Each example should be a complete, copy-pasteable snippet.
- Explain what each example demonstrates before the YAML block.

#### 3.7 Observability
- `kubectl` commands to inspect the feature's resources.
- Kubernetes events emitted (show example `kubectl describe` output).
- Any status fields or conditions to monitor.

#### 3.8 Scaling Behavior
- What happens on scale-out, scale-in.
- Include `kubectl scale` examples.
- Mention any finalizers or deletion protection.

#### 3.9 Backward Compatibility
- How existing resources behave after the feature is enabled/changed.
- Migration steps if applicable.

#### 3.10 Limitations
- Bulleted list of known limitations.
- Each item: bold summary + explanation.
- Be honest — users trust docs that acknowledge boundaries.

### Numbered tutorial series

For multi-part tutorials (like `01_core-concepts/`), study the existing series in `docs/user-guide/` and match their structure: an overview file listing all parts, then numbered files with prerequisites, hands-on steps, key takeaways, and "What's Next" links. These are rare — most new guides are standalone.

---

## Step 4 — Wrap up

Once all sections are done:

1. Read the final document end-to-end and check for:
   - Repetition across sections
   - Missing `kubectl` examples
   - Placeholder values that should be filled in
   - Consistent terminology (see style-reference.md)
2. Tell the user: "Your guide is saved at `docs/user-guide/<path>`. Ready for review via PR."

---

## Tone

- **Practical, not theoretical.** Users want to know *how*, not *why it was designed this way*.
- **Confident but honest.** State limitations clearly. Don't hedge with "might" or "could potentially".
- **Concise.** Each section should earn its place. If a section adds nothing the user can't infer from adjacent sections, cut it.
- **Example-driven.** Every behavioral claim should have a `kubectl` command or YAML snippet within reach.
