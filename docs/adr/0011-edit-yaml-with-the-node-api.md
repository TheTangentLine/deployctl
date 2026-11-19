# 0011. Edit YAML with the node API

- Status: Accepted
- Date: 2026-11-19

## Context

`git_commit` changes one value (an image tag or a replica count) in a values file that humans also read, review and edit. Those files have comments ("# managed by deployctl", "# do not raise above 10 without talking to DBAs"), a deliberate key order, and sometimes anchors.

The obvious Go approach, unmarshal into `map[string]any`, set the value, and marshal back, drops all comments, sorts or reorders keys, and may change quoting. Every deployctl commit would then be a noisy diff of the whole file, and reviewers would lose information.

## Decision

Edit values through `gopkg.in/yaml.v3`'s **`yaml.Node` tree**: parse, walk the dotted path, change only the target scalar's value (and set its tag and style so tags like `1.10` stay strings), encode with the file's detected indentation. A missing key is an error; deployctl never creates structure.

Shelling out to `yq` is an acceptable fallback if node encoding proves unreliable for some files.

## Consequences

- deployctl commits are one-line diffs that are easy to review and to `git blame`.
- Comments and layout survive, so humans and deployctl can share files.
- Some formatting may still be normalised by the encoder (for example, flow-style maps or unusual indentation). Golden-file tests in `testdata/yaml/` catch regressions and document what is preserved.
- Only plain dotted paths into mappings are supported. Values inside lists, or keys containing dots, are out of scope.

## Alternatives considered

- **Unmarshal/marshal with maps or structs.** Rejected: destroys comments and order.
- **Regex or line-based replacement.** Fast, but fragile with nested keys of the same name (`backend.image.tag` vs `frontend.image.tag`). Rejected.
- **Separate machine-owned file per environment** (for example `values-prod.deployctl.yaml` containing only the managed keys, listed after the human file in `valueFiles`). Removes the problem entirely and is worth recommending as a layout, but deployctl should not require it.
