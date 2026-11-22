# CCx-06 sandbox modes

## Baseline

The card records zero sandbox policy variants in Beacon types. `AutoMergePolicy` covers merges only, and `git clone --reference` gives repository isolation without an access mode.

## Scenario

A startup config sets `sandbox.mode` to one of the four Codex variants. The orchestrator loads the config, resolves the policy for each Watch, and checks a write to the merge ref plus a network request. A ready Watch reaches the merger only when both checks pass.

## Expected behavior

| Mode | Config value | Write to merge ref | Network request |
| --- | --- | --- | --- |
| Full access | `danger-full-access` | 1 allow | 1 allow |
| Read only | `read-only` | 1 deny | 1 deny |
| External sandbox | `external-sandbox` | 1 deny | 1 deny |
| Workspace write | `workspace-write` | 1 allow | 1 deny |

`workspace-write` allows writes inside `writableRoots`. The merge ref uses the path `git/refs/heads`, which falls inside the whole workspace when no roots are listed. `networkAccess` overrides the network default only when the config sets it.

## Checks

- Loading a config with each of the four mode strings succeeds, and `SandboxModes()` returns the same four values.
- An unknown mode string fails while the config is parsed.
- `CheckWrite("git/refs/heads")` returns one allow for full access and workspace write, and one deny for read only and external sandbox.
- `CheckNetwork()` denies network for the three restricted modes. A `networkAccess` override flips the decision.
- A ready Watch with `read-only` produces one `sandbox-denied` action and no merge call.
- A ready Watch with `workspace-write` reaches the merger and produces no `sandbox-denied` action.
- An absolute path or a path with `..` fails the write check in every mode.

## Negative control

Setting an unknown mode such as `write-only` must fail config loading. Allowing that value would turn a typo into a full access run. A second control feeds an absolute path to `CheckWrite` under `danger-full-access`; the check must deny it because Beacon records repo-relative paths only.
