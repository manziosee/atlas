// Incremental Server-Sent Events parser (WHATWG HTML §9.2.6), fed with text
// chunks as they arrive from a streaming fetch.

export interface SSEFrame {
  event?: string;
  data: string;
  id?: string;
}

export class SSEParser {
  private buffer = "";
  private data: string[] = [];
  private event?: string;
  private id?: string;

  feed(chunk: string): SSEFrame[] {
    this.buffer += chunk;
    const frames: SSEFrame[] = [];
    for (;;) {
      const match = /\r\n|\r|\n/.exec(this.buffer);
      if (!match) break;
      // A lone \r at the end may be the first half of \r\n; wait for more.
      if (match[0] === "\r" && match.index === this.buffer.length - 1) break;
      const line = this.buffer.slice(0, match.index);
      this.buffer = this.buffer.slice(match.index + match[0].length);
      if (line === "") {
        if (this.data.length) frames.push({ event: this.event, data: this.data.join("\n"), id: this.id });
        this.data = [];
        this.event = undefined;
        continue;
      }
      if (line.startsWith(":")) continue;
      const colon = line.indexOf(":");
      const field = colon === -1 ? line : line.slice(0, colon);
      let value = colon === -1 ? "" : line.slice(colon + 1);
      if (value.startsWith(" ")) value = value.slice(1);
      if (field === "data") this.data.push(value);
      else if (field === "event") this.event = value;
      else if (field === "id" && !value.includes("\0")) this.id = value;
    }
    return frames;
  }
}
