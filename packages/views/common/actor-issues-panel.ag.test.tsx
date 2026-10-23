// @vitest-environment jsdom

import { describe, it, expect, vi } from "vitest";
import { renderWithI18n } from "../test/i18n";
import { ActorIssuesPanel } from "./actor-issues-panel";

const surfaceModes = vi.hoisted(() => ({ seen: [] as string[][] }));
vi.mock("../issues/surface/issue-surface", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../issues/surface/issue-surface")>()),
  IssueSurface: ({ modes }: { modes: string[] }) => {
    surfaceModes.seen.push(modes);
    return null;
  },
}));

describe("AG patch: ActorIssuesPanel layouts", () => {
  it("stays list-only, so the actor panel has no Tree entry", () => {
    surfaceModes.seen = [];
    renderWithI18n(<ActorIssuesPanel actorType="agent" actorId="agent-1" />);
    expect(surfaceModes.seen.at(-1)).toEqual(["list"]);
  });
});
