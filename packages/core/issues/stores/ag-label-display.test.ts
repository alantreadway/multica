// @vitest-environment jsdom
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { createStore } from "zustand/vanilla";
import { setCurrentWorkspace } from "../../platform/workspace-storage";
import {
  DEFAULT_LABEL_DISPLAY,
  LABEL_DISPLAY_VALUES,
  normalizeCardPropertiesLabels,
  normalizeLabelDisplay,
} from "./ag-label-display";
import {
  DEFAULT_CARD_PROPERTIES,
  mergeViewStatePersisted,
  useIssueViewStore,
  viewStoreSlice,
  type IssueViewState,
} from "./view-store";
import { myIssuesViewStore } from "./my-issues-view-store";
import {
  ISSUE_SURFACE_VIEW_STORAGE_KEY,
  getIssueSurfaceViewStore,
  pruneIssueSurfaceViewStates,
  seedIssueSurfaceViewState,
} from "./surface-view-store";

const flush = async () => {
  await new Promise((resolve) => queueMicrotask(() => resolve(null)));
  await new Promise((resolve) => queueMicrotask(() => resolve(null)));
};

beforeAll(() => {
  if (typeof globalThis.localStorage?.clear !== "function") {
    const values = new Map<string, string>();
    const storage: Storage = {
      get length() {
        return values.size;
      },
      clear: () => values.clear(),
      getItem: (key) => values.get(key) ?? null,
      key: (index) => Array.from(values.keys())[index] ?? null,
      removeItem: (key) => {
        values.delete(key);
      },
      setItem: (key, value) => {
        values.set(key, value);
      },
    };
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: storage });
    Object.defineProperty(window, "localStorage", { configurable: true, value: storage });
  }
});

const defaults = () =>
  createStore<IssueViewState>()((set) => viewStoreSlice(set)).getState();

describe("AG patch: label display value", () => {
  it("exposes exactly Off, Compact, Full", () => {
    expect([...LABEL_DISPLAY_VALUES]).toEqual(["off", "compact", "full"]);
  });

  it("keeps a fresh view's labels off so nothing changes until someone opts in", () => {
    expect(DEFAULT_LABEL_DISPLAY).toBe("off");
    expect(DEFAULT_CARD_PROPERTIES.labels).toBe("off");
    expect(defaults().cardProperties.labels).toBe("off");
  });

  it.each([
    [true, "full"],
    [false, "off"],
    ["off", "off"],
    ["compact", "compact"],
    ["full", "full"],
  ] as const)("maps persisted %j to %s", (stored, expected) => {
    expect(normalizeLabelDisplay(stored)).toBe(expected);
  });

  it.each([[undefined], [null], ["FULL"], ["on"], ["wide"], [1], [0], [{}], [[]]])(
    "falls back to the default for %j",
    (stored) => {
      expect(normalizeLabelDisplay(stored)).toBe("off");
    },
  );

  it("falls back to a supplied fallback instead of the default", () => {
    expect(normalizeLabelDisplay(undefined, "compact")).toBe("compact");
    expect(normalizeLabelDisplay("garbage", "full")).toBe("full");
    // An explicit stored value always beats the fallback.
    expect(normalizeLabelDisplay(false, "full")).toBe("off");
  });

  it("normalises labels in card properties and leaves every other key alone", () => {
    const input = { priority: false, description: true, labels: true as unknown };
    const out = normalizeCardPropertiesLabels(input);
    expect(out).toEqual({ priority: false, description: true, labels: "full" });
    expect(input.labels).toBe(true); // input not mutated
    const withoutLabels: { priority: boolean; labels?: unknown } = { priority: true };
    expect(normalizeCardPropertiesLabels(withoutLabels)).toEqual({
      priority: true,
      labels: "off",
    });
  });
});

describe("AG patch: label display on the view store", () => {
  it("setLabelDisplay changes only labels", () => {
    const store = createStore<IssueViewState>()((set) => viewStoreSlice(set));
    store.getState().setLabelDisplay("compact");
    expect(store.getState().cardProperties).toEqual({
      ...DEFAULT_CARD_PROPERTIES,
      labels: "compact",
    });
    store.getState().setLabelDisplay("full");
    expect(store.getState().cardProperties.labels).toBe("full");
    store.getState().setLabelDisplay("off");
    expect(store.getState().cardProperties.labels).toBe("off");
  });

  it("toggling another card property does not touch labels", () => {
    const store = createStore<IssueViewState>()((set) => viewStoreSlice(set));
    store.getState().setLabelDisplay("compact");
    store.getState().toggleCardProperty("priority");
    expect(store.getState().cardProperties.priority).toBe(false);
    expect(store.getState().cardProperties.labels).toBe("compact");
  });
});

describe("AG patch: mergeViewStatePersisted migrates persisted labels", () => {
  it.each([
    [true, "full"],
    [false, "off"],
    ["off", "off"],
    ["compact", "compact"],
    ["full", "full"],
  ] as const)("cardProperties.labels %j becomes %s", (stored, expected) => {
    const merged = mergeViewStatePersisted({ cardProperties: { labels: stored } }, defaults());
    expect(merged.cardProperties.labels).toBe(expected);
  });

  it("uses the default when labels is missing, and keeps the other toggles", () => {
    const merged = mergeViewStatePersisted(
      { cardProperties: { priority: false } },
      defaults(),
    );
    expect(merged.cardProperties.labels).toBe("off");
    expect(merged.cardProperties.priority).toBe(false);
    expect(merged.cardProperties.assignee).toBe(true);
  });

  it("uses the default when there are no persisted cardProperties at all", () => {
    expect(mergeViewStatePersisted({}, defaults()).cardProperties).toEqual(
      DEFAULT_CARD_PROPERTIES,
    );
    expect(mergeViewStatePersisted(undefined, defaults()).cardProperties.labels).toBe("off");
  });

  it("degrades malformed or unknown values to the default", () => {
    for (const bad of ["on", "wide", 1, null, {}]) {
      const merged = mergeViewStatePersisted({ cardProperties: { labels: bad } }, defaults());
      expect(merged.cardProperties.labels).toBe("off");
    }
  });

  it("inherits the current labels value when the persisted one is missing", () => {
    const current = defaults();
    current.cardProperties = { ...current.cardProperties, labels: "compact" };
    const merged = mergeViewStatePersisted({ cardProperties: { priority: false } }, current);
    expect(merged.cardProperties.labels).toBe("compact");
  });
});

describe("AG patch: persisted labels are migrated by each store that reads them back", () => {
  beforeEach(async () => {
    localStorage.clear();
    pruneIssueSurfaceViewStates([]);
    setCurrentWorkspace(null, null);
    await flush();
  });
  afterEach(async () => {
    pruneIssueSurfaceViewStates([]);
    setCurrentWorkspace(null, null);
    await flush();
  });

  it.each([
    ["multica_issues_view", useIssueViewStore],
    ["multica_my_issues_view", myIssuesViewStore],
  ] as const)("%s: true -> full, false -> off, missing -> off", async (key, store) => {
    const cases: Array<[Record<string, unknown>, string]> = [
      [{ priority: true, labels: true }, "full"],
      [{ priority: true, labels: false }, "off"],
      [{ priority: true }, "off"],
      [{ priority: true, labels: "compact" }, "compact"],
    ];
    for (const [i, [cardProperties, expected]] of cases.entries()) {
      const slug = `acme${i}`;
      localStorage.setItem(
        `${key}:${slug}`,
        JSON.stringify({ version: 0, state: { cardProperties } }),
      );
      setCurrentWorkspace(slug, `ws_${i}`);
      await flush();
      expect(store.getState().cardProperties.labels).toBe(expected);
    }
  });

  it("surface registry: true -> full, false -> off, missing -> off, valid kept", async () => {
    // description:false keeps these out of the legacy all-on rewrite.
    const surfaces = Object.fromEntries(
      (
        [
          ["workspace:was-on", { labels: true }],
          ["workspace:was-off", { labels: false }],
          ["workspace:never-set", {}],
          ["workspace:compact", { labels: "compact" }],
        ] as const
      ).map(([key, extra]) => [
        key,
        {
          state: {
            sortBy: "title",
            sortDirection: "desc",
            cardProperties: { description: false, ...extra },
            hiddenStatuses: ["done"],
          },
          updatedAt: "2026-01-01T00:00:00Z",
        },
      ]),
    );
    localStorage.setItem(
      `${ISSUE_SURFACE_VIEW_STORAGE_KEY}:acme`,
      JSON.stringify({ version: 0, state: { surfaces } }),
    );
    setCurrentWorkspace("acme", "ws_a");
    await flush();

    const labels = (key: string) =>
      getIssueSurfaceViewStore(key).getState().cardProperties.labels;
    expect(labels("workspace:was-on")).toBe("full");
    expect(labels("workspace:was-off")).toBe("off");
    expect(labels("workspace:never-set")).toBe("off");
    expect(labels("workspace:compact")).toBe("compact");
  });

  it("surface registry: an explicit labels 'off' is not mistaken for the all-on legacy default", async () => {
    const make = (labels: unknown) => ({
      state: {
        sortBy: "position",
        sortDirection: "asc",
        cardProperties: {
          priority: true,
          description: true,
          assignee: true,
          startDate: true,
          dueDate: true,
          project: true,
          childProgress: true,
          labels,
        },
        hiddenStatuses: ["done"],
      },
      updatedAt: "2026-01-01T00:00:00Z",
    });
    localStorage.setItem(
      `${ISSUE_SURFACE_VIEW_STORAGE_KEY}:acme`,
      JSON.stringify({
        version: 0,
        state: {
          surfaces: {
            "workspace:off": make("off"),
            "workspace:bool-off": make(false),
            "workspace:full": make("full"),
            "workspace:compact": make("compact"),
          },
        },
      }),
    );
    setCurrentWorkspace("acme", "ws_a");
    await flush();

    const state = (key: string) => getIssueSurfaceViewStore(key).getState();
    // "off" and false both mean a user-chosen state: description (default
    // false) must NOT be reset by the legacy rewrite, so it stays true.
    expect(state("workspace:off").cardProperties.description).toBe(true);
    expect(state("workspace:off").cardProperties.labels).toBe("off");
    expect(state("workspace:bool-off").cardProperties.description).toBe(true);
    expect(state("workspace:bool-off").cardProperties.labels).toBe("off");
    // Compact and Full count as on, so an otherwise all-on state is still
    // recognised as the legacy default and rewritten to the current defaults.
    expect(state("workspace:full").cardProperties.description).toBe(false);
    expect(state("workspace:compact").cardProperties.description).toBe(false);
  });

  it.each([
    [{ labels: true }, "full"],
    [{ labels: false }, "off"],
    [{}, "off"],
    [{ labels: "compact" }, "compact"],
    [{ labels: "full" }, "full"],
  ] as const)(
    "saved view seed with stored display.cardProperties %j gives %s",
    async (stored, expected) => {
      setCurrentWorkspace("acme", "ws_a");
      await flush();
      const key = `view:${JSON.stringify(stored)}`;
      seedIssueSurfaceViewState(key, { cardProperties: stored });
      expect(getIssueSurfaceViewStore(key).getState().cardProperties.labels).toBe(expected);
    },
  );

  it("saved view seed with no cardProperties gives the default", async () => {
    setCurrentWorkspace("acme", "ws_a");
    await flush();
    seedIssueSurfaceViewState("view:bare", { viewMode: "list" });
    expect(getIssueSurfaceViewStore("view:bare").getState().cardProperties.labels).toBe("off");
  });
});
