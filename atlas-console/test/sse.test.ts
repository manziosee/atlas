import { describe, expect, it } from "vitest";
import { SSEParser } from "@/lib/sse";

describe("SSEParser", () => {
  it("parses frames split across chunks, comments and CRLF", () => {
    const p = new SSEParser();
    expect(p.feed(": connected\nretry: 3000\n\nid: 1\nda")).toEqual([]);
    expect(p.feed('ta: {"a":1}\r\n\r\nevent: reset\ndata: x\n')).toEqual([{ id: "1", data: '{"a":1}', event: undefined }]);
    expect(p.feed("\n")).toEqual([{ id: "1", data: "x", event: "reset" }]);
  });

  it("joins multi-line data and keeps the last event id", () => {
    const p = new SSEParser();
    expect(p.feed("id: 7\ndata: a\ndata: b\n\ndata: c\n\n")).toEqual([
      { id: "7", data: "a\nb", event: undefined },
      { id: "7", data: "c", event: undefined },
    ]);
  });

  it("waits when a chunk ends in a lone carriage return", () => {
    const p = new SSEParser();
    expect(p.feed("data: x\r")).toEqual([]);
    expect(p.feed("\n\n")).toEqual([{ data: "x", event: undefined, id: undefined }]);
  });
});
