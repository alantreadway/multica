# AG patches

Patch inventory for the `ag/<tag>` branch. Update this file whenever a patch
lands, is dropped or is reworked during a rebase.

## Upstream base

- Tag: `v0.6.2`
- Commit: `7ab92d7622ecd81d179a8d83ae2dea74835a5069`

## Patches

Test names for every patch start with `AG patch:` so they are easy to find and
to confirm survived a rebase (`grep -rn "AG patch:" packages/`).

### Tree-ordered parent lanes (pending)

Swimlane view groups by parent issue and orders lanes as a tree, so sub-issue
lanes sit under their parent lane. Status: pending until the unit's PR merges.

- Test prefix: `AG patch:`
- Files:
  - `packages/views/issues/components/swimlane-tree.ts` (new)
  - `packages/views/issues/components/swimlane-tree.test.ts` (new)
  - `packages/views/issues/components/swimlane-view.tsx`
  - `packages/views/issues/components/swimlane-view.test.tsx`
  - `packages/core/issues/queries.ts` (append only)

### Lane drag rules and jump-to-lane chip (pending)

Drag rules between swimlanes and a chip that jumps to the related lane.
Status: pending until the unit's PR merges. Depends on the tree-ordered lanes
patch.

- Test prefix: `AG patch:`
- Files:
  - `packages/views/issues/components/swimlane-view.tsx`
  - `packages/views/issues/components/swimlane-view.test.tsx`

## Tooling (not a patch)

`ag-patches/` and `.github/workflows/ag-patches.yml` exist only on the patch
branches. They do not touch product code.
