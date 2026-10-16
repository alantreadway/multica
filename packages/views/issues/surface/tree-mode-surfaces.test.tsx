/**
 * @vitest-environment jsdom
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@testing-library/react";
import type { ReactNode } from "react";
import { createStore } from "zustand/vanilla";
import {
  type IssueViewState,
  viewStoreSlice,
} from "@multica/core/issues/stores/view-store";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import enMyIssues from "../../locales/en/my-issues.json";

// AG patch: which surfaces offer the Tree layout. IssueSurface is replaced by a
// recorder so only the wiring is under test: the modes each page asks for, and
// that the page hands the controller's allowTree flag on to its header.

const surface = vi.hoisted(() => ({
  modes: [] as string[][],
  allowTree: true,
}));
const headerProps = vi.hoisted(() => ({
  issues: [] as Array<{ allowTree?: boolean }>,
  myIssues: [] as Array<{ allowTree?: boolean }>,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector?: (s: unknown) => unknown) => {
    const state = { user: { id: "user-1" } };
    return selector ? selector(state) : state;
  },
}));
vi.mock("../surface/issue-surface", () => ({
  IssueSurface: ({
    modes,
    renderHeader,
  }: {
    modes: string[];
    renderHeader?: (ctx: { controller: unknown }) => ReactNode;
  }) => {
    surface.modes.push(modes);
    return (
      <ViewStoreProvider store={createStore<IssueViewState>()(viewStoreSlice)}>
        {renderHeader?.({
          controller: {
            surfaceIssues: [],
            workingAgents: undefined,
            isRefreshing: false,
            allowTree: surface.allowTree,
            facetCountsExact: true,
            tableFacetCounts: undefined,
            setActiveTableFacet: vi.fn(),
          },
        })}
      </ViewStoreProvider>
    );
  },
}));
vi.mock("../components/issues-header", () => ({
  IssuesHeader: (props: { allowTree?: boolean }) => {
    headerProps.issues.push(props);
    return null;
  },
}));
vi.mock("../../my-issues/components/my-issues-header", () => ({
  MyIssuesHeader: (props: { allowTree?: boolean }) => {
    headerProps.myIssues.push(props);
    return null;
  },
}));

import { IssuesPage } from "../components/issues-page";
import { MyIssuesPage } from "../../my-issues/components/my-issues-page";

const RESOURCES = {
  en: { common: enCommon, issues: enIssues, "my-issues": enMyIssues },
};

function renderPage(ui: ReactNode) {
  return render(
    <I18nProvider locale="en" resources={RESOURCES}>
      {ui}
    </I18nProvider>,
  );
}

describe("AG patch: Tree is offered on the issue pages", () => {
  afterEach(() => {
    cleanup();
    surface.modes = [];
    surface.allowTree = true;
    headerProps.issues = [];
    headerProps.myIssues = [];
  });

  it("offers Tree on /issues and passes the flag to the view menu", () => {
    renderPage(<IssuesPage />);
    expect(surface.modes.at(-1)).toContain("tree");
    expect(headerProps.issues.at(-1)?.allowTree).toBe(true);
  });

  it("offers Tree on /my-issues and passes the flag to the view menu", () => {
    renderPage(<MyIssuesPage />);
    expect(surface.modes.at(-1)).toContain("tree");
    expect(headerProps.myIssues.at(-1)?.allowTree).toBe(true);
  });

  it("does not turn the menu entry on when the surface does not allow Tree", () => {
    surface.allowTree = false;
    renderPage(<IssuesPage />);
    renderPage(<MyIssuesPage />);
    expect(headerProps.issues.at(-1)?.allowTree).toBe(false);
    expect(headerProps.myIssues.at(-1)?.allowTree).toBe(false);
  });

  it("keeps the existing layouts alongside Tree", () => {
    renderPage(<IssuesPage />);
    renderPage(<MyIssuesPage />);
    for (const modes of surface.modes) {
      expect(modes).toEqual(expect.arrayContaining(["board", "list", "table", "swimlane"]));
    }
  });
});
