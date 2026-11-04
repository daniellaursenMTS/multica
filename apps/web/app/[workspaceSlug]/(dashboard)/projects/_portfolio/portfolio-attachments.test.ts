// @vitest-environment node

import { describe, expect, it, vi } from "vitest";
import {
  addProjectDocuments,
  MAX_DOCUMENT_BYTES,
  uploadProjectFiles,
  validateProjectDocument,
} from "./portfolio-attachments";

const file = (name: string, size = 10): File => ({ name, size } as File);

describe("project document attachments", () => {
  it.each(["budget.xlsx", "legacy.XLS", "brief.docx", "notes.DOC", "report.pdf"])(
    "accepts %s",
    (name) => {
      expect(validateProjectDocument(file(name))).toBeNull();
    },
  );

  it("rejects other formats without weakening the allowlist", () => {
    expect(validateProjectDocument(file("slides.pptx"))).toContain("ikke et understøttet dokument");
    expect(validateProjectDocument(file("sheet.xlsx.exe"))).toContain("ikke et understøttet dokument");
  });

  it("rejects documents over the ordinary 100 MB attachment limit", () => {
    expect(validateProjectDocument(file("large.pdf", MAX_DOCUMENT_BYTES + 1))).toBe(
      "large.pdf er over 100 MB.",
    );
  });

  it("keeps valid selections while reporting an invalid file", () => {
    const existing = file("existing.docx");
    const accepted = file("budget.xlsx");
    const result = addProjectDocuments(
      [existing],
      [accepted, file("script.js")],
    );

    expect(result.documents).toEqual([existing, accepted]);
    expect(result.error).toContain("script.js");
  });

  it("names the failed file and stops before a caller can save attachment ids", async () => {
    const upload = vi
      .fn<(candidate: File) => Promise<string>>()
      .mockResolvedValueOnce("attachment-1")
      .mockRejectedValueOnce(new Error("upload failed"));

    await expect(
      uploadProjectFiles([file("one.docx"), file("two.xlsx"), file("three.pdf")], upload),
    ).rejects.toThrow("Kunne ikke uploade two.xlsx: upload failed");
    expect(upload).toHaveBeenCalledTimes(2);
  });
});
