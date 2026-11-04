export type SseFrame = { kind: 'event'; id?: string; event: string; data: string; retryMs?: number } | { kind: 'comment' };

// Decode incrementally: both UTF-8 sequences and CRLF pairs can cross pushes.
export class SseParser {
  private decoder = new TextDecoder();
  private line = '';
  private afterCR = false;
  private data: string[] = [];
  private id: string | undefined;
  private event = 'message';
  private retryMs: number | undefined;

  push(chunk: Uint8Array): SseFrame[] {
    const frames: SseFrame[] = [];
    for (const c of this.decoder.decode(chunk, { stream: true })) {
      if (this.afterCR) {
        this.afterCR = false;
        if (c === '\n') continue;
      }
      if (c === '\r' || c === '\n') {
        this.consumeLine(frames);
        this.afterCR = c === '\r';
      } else this.line += c;
    }
    return frames;
  }

  end(): SseFrame[] {
    // SSE dispatch requires a terminating blank line; EOF is not a delimiter.
    this.decoder = new TextDecoder();
    this.line = '';
    this.afterCR = false;
    this.resetBlock();
    return [];
  }

  private resetBlock(): void {
    this.data = [];
    this.id = undefined;
    this.event = 'message';
    this.retryMs = undefined;
  }

  private consumeLine(frames: SseFrame[]): void {
    const line = this.line;
    this.line = '';
    if (line === '') {
      if (this.data.length || this.id !== undefined || this.retryMs !== undefined) {
        frames.push({ kind: 'event', event: this.event, data: this.data.join('\n'),
          ...(this.id !== undefined ? {id:this.id} : {}),
          ...(this.retryMs !== undefined ? {retryMs:this.retryMs} : {}) });
      }
      this.resetBlock();
      return;
    }
    if (line.startsWith(':')) { frames.push({kind:'comment'}); return; }
    const colon = line.indexOf(':');
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? '' : line.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    switch (field) {
      case 'data': this.data.push(value); break;
      case 'id': if (!value.includes('\0')) this.id = value; break;
      case 'event': this.event = value || 'message'; break;
      case 'retry': if (/^\d+$/.test(value) && Number.isSafeInteger(Number(value))) this.retryMs = Number(value); break;
    }
  }
}
