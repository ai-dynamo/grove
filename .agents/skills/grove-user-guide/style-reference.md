# Grove User Guide — Style Reference

Conventions for writing Grove user documentation. Follows the [Kubernetes documentation style guide](https://kubernetes.io/docs/contribute/style/style-guide/) with Grove-specific additions.

---

## Formatting rules

| Element | Convention | Example |
|---------|-----------|---------|
| API objects | UpperCamelCase (PascalCase) | PodCliqueSet, ComputeDomain, PodClique |
| Annotation keys | Code style | `grove.io/mnnvl-group` |
| Annotation values | Code style with quotes | `"none"`, `"my-group"` |
| Helm values | Code style | `config.network.autoMNNVLEnabled` |
| CLI commands | Code block with `bash` fence | `kubectl get pcs` |
| YAML examples | Code block with `yaml` fence | Full, copy-pasteable manifests |
| Placeholders | Angle brackets | `<namespace>`, `<pcs-name>` |
| Filenames and paths | Code style | `auto-mnnvl.md` |
| New terms | Italics on first use | *ComputeDomain* |
| UI elements | Bold | **Status** |

## Writing conventions

- Use U.S. English spelling.
- Use active voice: "Grove creates a ComputeDomain" not "A ComputeDomain is created by Grove".
- Use present tense: "The operator detects GPU containers" not "The operator will detect GPU containers".
- Address the reader as "you": "When you scale the PCS..." not "When the user scales the PCS..."
- Punctuation goes outside quotation marks (international standard): `values include "none".` not `values include "none."`.

## Grove-specific terminology

Use these terms consistently:

| Term | Usage | Avoid |
|------|-------|-------|
| PodCliqueSet (PCS) | Spell out on first use, abbreviation after | "pod clique set", "PCS resource" |
| PodClique (PCLQ) | Spell out on first use | "pod clique", "clique" |
| PodCliqueScalingGroup (PCSG) | Spell out on first use | "scaling group" |
| ComputeDomain | Always PascalCase | "compute domain", "CD" in user docs |
| replica | Lowercase | "Replica" |
| annotation | Lowercase | "Annotation" |
| MNNVL | All caps, define on first use: "Multi-Node NVLink (MNNVL)" | "mnnvl", "Mnnvl" |
| opt-out | Hyphenated | "opt out" (verb form is fine without hyphen: "to opt out") |
| Grove operator | Lowercase "operator" | "Grove Operator" |

## YAML example conventions

- Always include `apiVersion`, `kind`, `metadata.name`, and `metadata.namespace` (or note that default is assumed).
- Use realistic but generic names: `my-inference`, `my-workload`, not `test-123`.
- Include comments only when a field isn't self-explanatory.
- Show the minimal YAML needed — don't pad with irrelevant fields.
- When showing GPU resources, use `nvidia.com/gpu: "8"` as the standard example.

## Existing guides (for reference)

Read these to match tone, depth, and structure:

- `docs/user-guide/auto-mnnvl.md` — standalone feature guide (MNNVL)
- `docs/user-guide/certificate-management.md` — standalone operational guide
- `docs/user-guide/01_core-concepts/` — numbered tutorial series
- `docs/user-guide/02_pod-and-resource-naming-conventions/` — numbered tutorial series
- `docs/user-guide/03_environment-variables-for-pod-discovery/` — numbered tutorial series
