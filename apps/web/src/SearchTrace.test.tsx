import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { SearchTrace } from "./SearchTrace";

test("shows stage ranks, negative rerank scores and explicit degradation", () => {
  render(
    <SearchTrace
      trace={{
        candidates: [
          {
            ChunkID: 1,
            DocumentID: 2,
            VersionID: 3,
            SectionID: 4,
            Title: "住宿标准",
            Content: "",
            HeadingPath: [],
            PageStart: null,
            PageEnd: null,
            RRFRank: 1,
            Score: 0.03,
            LexicalRank: 1,
            LexicalScore: 2,
            SemanticRank: 0,
            SemanticDistance: 0,
            RerankRank: 1,
            RerankScore: -2,
          },
        ],
        timings_ms: { recall_rrf: 12.5 },
        degraded: ["rerank"],
        filters: { DocumentTags: [], MIMEs: [], UpdatedAfter: null },
      }}
    />,
  );
  fireEvent.click(screen.getByText("检索调试 · 1 个可见候选"));
  expect(screen.getByText("1 / -2.0000")).toBeTruthy();
  expect(screen.getByText("住宿标准 / 1")).toBeTruthy();
  expect(screen.getByText(/recall_rrf: 12.5 ms/)).toBeTruthy();
});
