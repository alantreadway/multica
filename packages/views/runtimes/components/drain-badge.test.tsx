// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";
import { DrainBadge } from "./drain-badge";

function renderBadge(
  metadata: Record<string, unknown>,
  status: "online" | "offline" = "online",
) {
  return renderWithI18n(<DrainBadge runtime={{ status, metadata }} />);
}

afterEach(cleanup);

describe("AG patch: DrainBadge", () => {
  it("AG patch: shows Draining with the running count", () => {
    renderBadge({ ag_drain: { draining: true, idle: false, active_tasks: 2 } });
    expect(screen.getByText("Draining · 2 running")).toBeInTheDocument();
  });

  it("AG patch: shows Idle once drained and empty", () => {
    renderBadge({ ag_drain: { draining: true, idle: true, active_tasks: 0 } });
    expect(screen.getByText("Idle")).toBeInTheDocument();
    expect(screen.queryByText(/Draining/)).not.toBeInTheDocument();
  });

  it("AG patch: renders nothing when the runtime is not draining", () => {
    const { container } = renderBadge({});
    expect(container).toBeEmptyDOMElement();
  });

  it("AG patch: renders nothing for an offline runtime with a stale report", () => {
    const { container } = renderBadge(
      { ag_drain: { draining: true, idle: true, active_tasks: 0 } },
      "offline",
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("AG patch: a malformed report renders nothing and does not throw", () => {
    const { container } = renderBadge({ ag_drain: "draining" });
    expect(container).toBeEmptyDOMElement();
  });

  it("AG patch: a malformed count shows Draining without claiming zero running", () => {
    renderBadge({ ag_drain: { draining: true, active_tasks: "many" } });
    expect(screen.getByText("Draining")).toBeInTheDocument();
  });
});
