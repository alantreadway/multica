import { beforeEach, describe, expect, it } from "vitest";
import { RAIL_SPACES } from "../paths/spaces";
import { resolveRailOrder, useRailOrderStore } from "./rail-order-store";

// AG patch: the fork's Overview and Decisions spaces head the default order,
// so an order saved without them gets them back at the front.
const ag = (ids: readonly string[]) => ["overview", "decisions", ...ids];

describe("resolveRailOrder", () => {
  it("uses the default order until the user reorders", () => {
    expect(resolveRailOrder(null)).toEqual(RAIL_SPACES);
  });

  it("keeps a saved order", () => {
    const saved = ["chat", "work", "team", "autopilots"];
    expect(resolveRailOrder(saved)).toEqual(ag(saved));
  });

  it("drops unknown and repeated ids", () => {
    expect(resolveRailOrder(["chat", "settings", 3, "chat", "work", "team", "autopilots"])).toEqual(ag([
      "chat",
      "work",
      "team",
      "autopilots",
    ]));
  });

  it("drops the retired Inbox space from an order saved before it moved into Work", () => {
    expect(resolveRailOrder(["chat", "inbox", "work", "team", "autopilots"])).toEqual(ag([
      "chat",
      "work",
      "team",
      "autopilots",
    ]));
  });

  it("puts a space missing from the saved order after its default neighbour", () => {
    expect(resolveRailOrder(["chat", "autopilots", "work"])).toEqual(ag([
      "chat",
      "autopilots",
      "work",
      "team",
    ]));
    expect(resolveRailOrder(["chat", "team", "autopilots"])).toEqual(ag([
      "work",
      "chat",
      "team",
      "autopilots",
    ]));
  });
});

describe("useRailOrderStore", () => {
  beforeEach(() => {
    useRailOrderStore.setState({ order: null });
  });

  it("saves a custom order", () => {
    const order = ["chat", "work", "team", "autopilots"] as const;
    useRailOrderStore.getState().setOrder(order);
    expect(useRailOrderStore.getState().order).toEqual(ag(order));
  });

  it("forgets the order once it is back to the default", () => {
    useRailOrderStore.getState().setOrder(["team", "work", "chat", "autopilots"]);
    useRailOrderStore.getState().setOrder(RAIL_SPACES);
    expect(useRailOrderStore.getState().order).toBeNull();
  });
});
