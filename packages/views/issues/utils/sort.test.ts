import { describe, expect, it } from "vitest";
import type { Issue } from "@multica/core/types";
import { sortIssues } from "./sort";

const propertyId = "prop-effort";

function issueWith(id: string, value?: number | string, position = 0): Issue {
  return {
    id,
    position,
    properties: value === undefined ? {} : { [propertyId]: value },
  } as unknown as Issue;
}

describe("sortIssues property sorts", () => {
  it("sorts number values numerically, missing values last", () => {
    const sorted = sortIssues(
      [issueWith("big", 10), issueWith("none"), issueWith("small", 2)],
      `property:${propertyId}`,
      "asc",
    );
    expect(sorted.map((i) => i.id)).toEqual(["small", "big", "none"]);
  });

  it("desc reverses values but keeps missing values last", () => {
    const sorted = sortIssues(
      [issueWith("none"), issueWith("small", 2), issueWith("big", 10)],
      `property:${propertyId}`,
      "desc",
    );
    expect(sorted.map((i) => i.id)).toEqual(["big", "small", "none"]);
  });

  it("sorts date-only strings chronologically via lexical compare", () => {
    const sorted = sortIssues(
      [issueWith("later", "2026-08-01"), issueWith("earlier", "2026-07-13")],
      `property:${propertyId}`,
      "asc",
    );
    expect(sorted.map((i) => i.id)).toEqual(["earlier", "later"]);
  });

  it("falls back to position order for the static fields", () => {
    const sorted = sortIssues(
      [issueWith("b", undefined, 2), issueWith("a", undefined, 1)],
      "position",
      "asc",
    );
    expect(sorted.map((i) => i.id)).toEqual(["a", "b"]);
  });
});

describe("sortIssues last_activity", () => {
  function issueActive(id: string, lastActivityAt: string | null, updatedAt: string): Issue {
    return {
      id,
      position: 0,
      last_activity_at: lastActivityAt,
      updated_at: updatedAt,
    } as unknown as Issue;
  }

  it("puts the most recently worked-on issue first on desc", () => {
    const sorted = sortIssues(
      [
        issueActive("old", "2026-11-01T10:00:00Z", "2026-11-01T10:00:00Z"),
        issueActive("fresh", "2026-11-12T09:00:00Z", "2026-11-02T10:00:00Z"),
        issueActive("legacy", null, "2026-11-05T10:00:00Z"),
      ],
      "last_activity",
      "desc",
    );
    expect(sorted.map((i) => i.id)).toEqual(["fresh", "legacy", "old"]);
  });
});
