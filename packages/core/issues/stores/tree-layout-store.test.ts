// @vitest-environment node
import { beforeEach, describe, expect, it } from "vitest";
import { selectTreeLayout, useTreeLayoutStore } from "./tree-layout-store";

describe("AG patch: tree layout store", () => {
  beforeEach(() => {
    useTreeLayoutStore.setState({ layouts: {} });
  });

  it("defaults every surface to the outline", () => {
    expect(selectTreeLayout("project:p1")(useTreeLayoutStore.getState())).toBe("outline");
  });

  it("remembers the chart per surface and drops the entry when outline returns", () => {
    const { setLayout } = useTreeLayoutStore.getState();
    setLayout("project:p1", "chart");
    expect(selectTreeLayout("project:p1")(useTreeLayoutStore.getState())).toBe("chart");
    expect(selectTreeLayout("workspace:all")(useTreeLayoutStore.getState())).toBe("outline");

    setLayout("project:p1", "outline");
    expect(useTreeLayoutStore.getState().layouts).toEqual({});
  });

  it("reads a stored value it does not know as the outline", () => {
    useTreeLayoutStore.setState({ layouts: { s: "radial" as never } });
    expect(selectTreeLayout("s")(useTreeLayoutStore.getState())).toBe("outline");
  });
});
