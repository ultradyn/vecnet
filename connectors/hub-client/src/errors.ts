export class HubError extends Error {
  status: number;
  code: string;
  details: Record<string, unknown>;
  retryAfterMs?: number;
  constructor(status: number, code: string, details: Record<string, unknown> = {}, retryAfterMs?: number) {
    // Deliberately exclude server-controlled text, URLs, headers and bodies.
    super(`hub error ${status}`);
    this.name = 'HubError';
    this.status = status;
    this.code = code;
    this.details = details;
    this.retryAfterMs = retryAfterMs;
  }
  get retryable(): boolean { return this.status === 0 || this.status === 408 || this.status === 429 || (this.status >= 500 && this.status <= 599); }
}
