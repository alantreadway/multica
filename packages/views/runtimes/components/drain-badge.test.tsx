// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";
import { DrainBadge } from "./drain-badge";

const TEST_RESOURCES = { en: { common: enCommon, runtimes: enRuntimes } };

function renderBadge(
  metadata: Record<string, unknown>,
  status: "online" | "offline" = "online",
) {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <DrainBadge runtime={{ status, metadata }} />
    </I18nProvider>,
  );
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
