// @vitest-environment node
import { describe, expect, it } from "vitest";
import { RESOURCES } from "./index";

// The Tree layout's menu, tooltip, row and chart strings must exist in every shipped
// locale; a missing key silently renders English. The generic parity test
// guards key sets, this one guards the Tree strings' content.
const TREE_KEYS = [
  "aria_label",
  "expand",
  "collapse",
  "expand_all",
  "collapse_all",
  "focus",
  "focus_root",
  "breadcrumb_label",
  "load_more",
  "loading_children",
  "load_error",
  "retry",
  "empty",
  "hidden_ancestors",
  "filters_paused",
  "clear_focus",
  "sub_issue_progress",
  "layout_section",
  "layout_outline",
  "layout_chart",
  "chart_hint",
  "chart_aria_label",
  "chart_zoom_in",
  "chart_zoom_out",
  "chart_fit",
  "chart_reset",
  "chart_expand_level",
  "chart_use_outline",
  "chart_too_many_title",
  "chart_too_many_body",
] as const;

type IssuesBundle = {
  view: Record<string, string>;
  tree: Record<string, string>;
};

describe("AG patch: tree view translations", () => {
  const locales = Object.keys(RESOURCES);

  it("ships en plus the five translated locales", () => {
    expect([...locales].sort()).toEqual(["en", "fr", "ja", "ko", "zh-Hans", "zh-Hant"]);
  });

  for (const locale of locales) {
    describe(locale, () => {
      const issues = (RESOURCES as unknown as Record<string, { ag: { issues: IssuesBundle } }>)[
        locale
      ]!.ag.issues;

      it("labels the Tree layout in the view menu, tooltip and saved-view picker", () => {
        for (const key of ["tree", "tooltip_tree"]) {
          expect(typeof issues.view[key], `view.${key}`).toBe("string");
          expect(issues.view[key]!.trim().length, `view.${key}`).toBeGreaterThan(0);
        }
      });

      it("defines every tree row string", () => {
        for (const key of TREE_KEYS) {
          expect(typeof issues.tree[key], `tree.${key}`).toBe("string");
          expect(issues.tree[key]!.trim().length, `tree.${key}`).toBeGreaterThan(0);
        }
      });

      it("keeps the sub-issue progress placeholders", () => {
        expect(issues.tree.sub_issue_progress).toContain("{{done}}");
        expect(issues.tree.sub_issue_progress).toContain("{{total}}");
      });
    });
  }

  it("keeps the node-cap placeholders in the chart's warning", () => {
    for (const locale of locales) {
      const { tree } = (RESOURCES as unknown as Record<string, { ag: { issues: IssuesBundle } }>)[
        locale
      ]!.ag.issues;
      expect(tree.chart_too_many_body, locale).toContain("{{count}}");
      expect(tree.chart_too_many_body, locale).toContain("{{max}}");
    }
  });

  it("uses the spec's English wording for the layout", () => {
    const { view } = (RESOURCES.en as unknown as { ag: { issues: IssuesBundle } }).ag.issues;
    expect(view.tree).toBe("Tree");
    const { tree } = (RESOURCES.en as unknown as { ag: { issues: IssuesBundle } }).ag.issues;
    expect(tree.layout_outline).toBe("Outline");
    expect(tree.layout_chart).toBe("Chart");
  });
});
