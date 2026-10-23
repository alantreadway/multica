// @vitest-environment node
import { describe, expect, it } from "vitest";
import { laneBand, laneBandClass, laneGapClass } from "./swimlane-band";
import type { SwimlaneTreeInfo } from "./swimlane-tree";

// AG patch: alternating band per root tree in parent swimlanes.

const tree = (
  over: Partial<SwimlaneTreeInfo> = {},
): SwimlaneTreeInfo => ({
  depth: 0,
  treeParentRawId: null,
  path: [],
  descendantCount: 0,
  rootRawId: "r",
  rootOrdinal: 0,
  ...over,
});

describe("laneBand", () => {
  it("AG patch: alternates even / odd by root ordinal", () => {
    expect(
      [0, 1, 2, 3, 4].map((rootOrdinal) => laneBand(tree({ rootOrdinal }))),
    ).toEqual(["even", "odd", "even", "odd", "even"]);
  });

  it("AG patch: sub-lanes take the band of their root ordinal, whatever their depth", () => {
    expect(laneBand(tree({ depth: 3, rootOrdinal: 1 }))).toBe("odd");
    expect(laneBand(tree({ depth: 2, rootOrdinal: 2 }))).toBe("even");
  });

  it("AG patch: pinned lanes and lanes without tree info have no band", () => {
    expect(laneBand(tree({ rootRawId: null, rootOrdinal: null }))).toBeNull();
    expect(laneBand(undefined)).toBeNull();
  });
});

describe("laneBandClass", () => {
  it("AG patch: only the odd band is tinted, with a neutral token", () => {
    expect(laneBandClass("odd")).toMatch(/\bbg-muted\/\d+\b/);
    expect(laneBandClass("even")).not.toContain("bg-");
  });

  it("AG patch: both bands pad and pull back by the same amount so lanes never shift", () => {
    for (const band of ["odd", "even"] as const) {
      const cls = laneBandClass(band).split(" ");
      expect(cls).toContain("-mx-2");
      expect(cls).toContain("px-2");
    }
  });
});

describe("laneGapClass", () => {
  it("AG patch: the first lane has no gap", () => {
    expect(laneGapClass(0, tree())).toBeUndefined();
    expect(laneGapClass(0, undefined)).toBeUndefined();
  });

  it("AG patch: a lane inside a tree sits close, a new root keeps the wide gap", () => {
    expect(laneGapClass(3, tree({ depth: 1 }))).toBe("pt-2");
    expect(laneGapClass(4, tree({ depth: 4 }))).toBe("pt-2");
    expect(laneGapClass(5, tree({ depth: 0 }))).toBe("pt-4");
  });

  it("AG patch: lanes without tree info (other groupings, pinned) keep today's wide gap", () => {
    expect(laneGapClass(2, undefined)).toBe("pt-4");
    expect(laneGapClass(1, tree({ rootRawId: null, rootOrdinal: null }))).toBe(
      "pt-4",
    );
  });
});
