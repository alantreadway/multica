import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Label } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";
import { AgLabelDisplayControl } from "./ag-label-display-control";
import { AgLabelRow } from "./ag-label-row";

const NAMES = ["Bug", "Frontend", "Needs design", "Customer-reported", "Regression"];
const COLORS = ["#ef4444", "#3b82f6", "#22c55e", "#a855f7", "#f97316"];

function makeLabels(count: number): Label[] {
  return NAMES.slice(0, count).map((name, i) => ({
    id: `label-${i}`,
    workspace_id: "ws-1",
    name,
    color: COLORS[i]!,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  }));
}

const row = (container: HTMLElement) =>
  container.querySelector<HTMLElement>('[data-slot="ag-label-row"]');

describe("AG patch: label row", () => {
  it.each(["compact", "full"] as const)(
    "shows every label with no +N overflow in %s mode",
    (display) => {
      const { container } = renderWithI18n(
        <AgLabelRow labels={makeLabels(5)} display={display} />,
      );
      for (const name of NAMES) {
        expect(within(container).getByText(name)).toBeVisible();
      }
      expect(container.textContent).not.toMatch(/\+\d/);
    },
  );

  it.each(["compact", "full"] as const)(
    "wraps instead of clipping in %s mode",
    (display) => {
      const { container } = renderWithI18n(
        <AgLabelRow labels={makeLabels(4)} display={display} />,
      );
      const el = row(container)!;
      expect(el.className).toContain("flex-wrap");
      expect(el.className).not.toContain("overflow-hidden");
    },
  );

  it.each(["compact", "full"] as const)(
    "keeps the order the server returned in %s mode",
    (display) => {
      const reversed = [...makeLabels(4)].reverse();
      const { container } = renderWithI18n(
        <AgLabelRow labels={reversed} display={display} />,
      );
      const shown = Array.from(row(container)!.children).map((c) =>
        c.getAttribute("aria-label"),
      );
      expect(shown).toEqual(reversed.map((l) => l.name));
    },
  );

  it("renders nothing when display is off", () => {
    const { container } = renderWithI18n(
      <AgLabelRow labels={makeLabels(4)} display="off" />,
    );
    expect(row(container)).toBeNull();
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing for an empty label list", () => {
    const { container } = renderWithI18n(<AgLabelRow labels={[]} display="full" />);
    expect(container).toBeEmptyDOMElement();
  });

  it("draws compact labels as outlined chips with a colour dot and visible text", () => {
    const labels = makeLabels(4);
    const { container } = renderWithI18n(
      <AgLabelRow labels={labels} display="compact" />,
    );
    const chips = Array.from(row(container)!.children) as HTMLElement[];
    expect(chips).toHaveLength(4);
    chips.forEach((chip, i) => {
      // Outlined, not filled.
      expect(chip.className).toContain("border");
      expect(chip.style.backgroundColor).toBe("");
      // Micro type.
      expect(chip.className).toContain("text-micro");
      // Colour dot carries the label colour.
      const dot = chip.querySelector<HTMLElement>('[aria-hidden="true"]')!;
      expect(dot.style.backgroundColor).toBe(
        // jsdom normalises hex to rgb()
        hexToRgb(labels[i]!.color),
      );
      // Text always shows.
      expect(within(chip).getByText(labels[i]!.name)).toBeVisible();
    });
  });

  it("draws full labels as the filled chip in the label colour", () => {
    const labels = makeLabels(4);
    const { container } = renderWithI18n(<AgLabelRow labels={labels} display="full" />);
    const chips = Array.from(row(container)!.children) as HTMLElement[];
    expect(chips).toHaveLength(4);
    chips.forEach((chip, i) => {
      expect(chip.style.backgroundColor).toBe(hexToRgb(labels[i]!.color));
      expect(chip.className).toContain("text-caption");
      expect(chip.querySelector('[aria-hidden="true"]')).toBeNull();
    });
  });

  it("keeps the compact chip's fixed width overhead under the full chip's 16px", () => {
    const labels = makeLabels(1);
    const compact = renderWithI18n(<AgLabelRow labels={labels} display="compact" />);
    const full = renderWithI18n(<AgLabelRow labels={labels} display="full" />);
    const compactChip = row(compact.container)!.firstElementChild as HTMLElement;
    const fullChip = row(full.container)!.firstElementChild as HTMLElement;
    const px = (cls: string, prefix: string) => {
      const m = new RegExp(`(?:^|\\s)${prefix}-(\\d+(?:\\.\\d+)?)(?:\\s|$)`).exec(cls);
      return m ? Number(m[1]) * 4 : 0;
    };
    const dot = compactChip.querySelector<HTMLElement>('[aria-hidden="true"]')!;
    const overhead =
      2 * px(compactChip.className, "px") +
      px(compactChip.className, "gap") +
      px(dot.className, "size");
    const fullOverhead = 2 * px(fullChip.className, "px");
    expect(fullOverhead).toBe(16);
    // The outline must be an inset ring: a real border would add width.
    expect(compactChip.className).not.toMatch(/(?:^|\s)border(?:-|\s|$)/);
    expect(overhead).toBeLessThan(fullOverhead);
  });

  it("forwards className to the row", () => {
    const { container } = renderWithI18n(
      <AgLabelRow labels={makeLabels(2)} display="full" className="ml-1.5 max-w-[260px]" />,
    );
    expect(row(container)!.className).toContain("ml-1.5");
    expect(row(container)!.className).toContain("max-w-[260px]");
  });
});

describe("AG patch: label display control", () => {
  it("offers Off, Compact and Full and marks the current value", () => {
    renderWithI18n(<AgLabelDisplayControl value="compact" onChange={vi.fn()} />);
    const radios = screen.getAllByRole("radio");
    expect(radios.map((r) => r.textContent)).toEqual(["Off", "Compact", "Full"]);
    expect(screen.getByRole("radio", { name: "Compact" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(screen.getByRole("radio", { name: "Off" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(screen.getByRole("radio", { name: "Full" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it.each([
    ["off", "Full", "full"],
    ["full", "Compact", "compact"],
    ["compact", "Off", "off"],
  ] as const)("from %s, choosing %s reports %s", async (value, name, expected) => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    renderWithI18n(<AgLabelDisplayControl value={value} onChange={onChange} />);
    await user.click(screen.getByRole("radio", { name }));
    expect(onChange).toHaveBeenCalledWith(expected);
  });

  it("is labelled Labels and shows the title unless hidden", () => {
    const { rerender } = renderWithI18n(
      <AgLabelDisplayControl value="off" onChange={vi.fn()} />,
    );
    expect(screen.getByRole("radiogroup", { name: "Labels" })).toBeInTheDocument();
    expect(screen.getByText("Labels")).toBeInTheDocument();
    rerender(<AgLabelDisplayControl value="off" onChange={vi.fn()} hideTitle />);
    expect(screen.getByRole("radiogroup", { name: "Labels" })).toBeInTheDocument();
    expect(screen.queryByText("Labels")).toBeNull();
  });
});

function hexToRgb(hex: string): string {
  const n = parseInt(hex.slice(1), 16);
  return `rgb(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255})`;
}
