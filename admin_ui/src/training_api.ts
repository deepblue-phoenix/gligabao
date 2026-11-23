// How the admin UI talks to the training endpoints (../training/..., which are part of the API, not of the admin API): like every route
// of users, they need to know the user the request acts as, in the gb-user-id header. The admin UI is for gligabao run on a local
// machine, so it is always one fixed user, ADMIN_USER_ID. (If the server has an auth hook the admin UI has no API key to send, so it
// does not work with one yet.)

export const ADMIN_USER_ID = "admin";
const USER_ID_HEADER = "gb-user-id";

// fetch() for a training endpoint: the same, plus the user header.
export function trainingFetch(url: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set(USER_ID_HEADER, ADMIN_USER_ID);
  return fetch(url, { ...init, headers });
}

type Listener = (e: Event) => void;

// The part of EventSource the admin UI uses (onopen, onerror, addEventListener, close), for an SSE endpoint. The real EventSource cannot
// send a header, so this reads the stream with fetch and, like EventSource, reconnects by itself (3 s after the connection is lost)
// with Last-Event-ID of the last event it got.
export class TrainingEventSource {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  private listeners = new Map<string, Listener[]>();
  private lastID = "";
  private closed = false;
  private abort: AbortController | undefined;

  constructor(private readonly url: string) {
    void this.run();
  }

  addEventListener(type: string, fn: Listener): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]);
  }

  close(): void {
    this.closed = true;
    this.abort?.abort();
  }

  private async run(): Promise<void> {
    while (!this.closed) {
      try {
        this.abort = new AbortController();
        const headers: Record<string, string> = { Accept: "text/event-stream" };
        if (this.lastID) headers["Last-Event-ID"] = this.lastID;
        const res = await trainingFetch(this.url, { headers, signal: this.abort.signal });
        if (!res.ok || !res.body) throw new Error(`${res.status}`);
        this.onopen?.();
        await this.read(res.body);
      } catch {
        // falls through to the error handling below
      }
      if (this.closed) return;
      this.onerror?.();
      await new Promise((r) => setTimeout(r, 3000));
    }
  }

  // Reads the stream until it ends: events are blocks of "field: value" lines separated by a blank line.
  private async read(body: ReadableStream<Uint8Array>): Promise<void> {
    const reader = body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    let type = "message";
    let data: string[] = [];
    for (;;) {
      const { done, value } = await reader.read();
      if (done) return;
      buf += decoder.decode(value, { stream: true });
      let nl: number;
      while ((nl = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, nl).replace(/\r$/, "");
        buf = buf.slice(nl + 1);
        if (line === "") {
          if (data.length > 0) this.dispatch(type, data.join("\n"));
          type = "message";
          data = [];
        } else if (!line.startsWith(":")) {
          const i = line.indexOf(":");
          const field = i < 0 ? line : line.slice(0, i);
          const v = i < 0 ? "" : line.slice(i + 1).replace(/^ /, "");
          if (field === "event") type = v;
          else if (field === "data") data.push(v);
          else if (field === "id") this.lastID = v;
        }
      }
    }
  }

  private dispatch(type: string, data: string): void {
    const ev = new MessageEvent(type, { data, lastEventId: this.lastID });
    for (const fn of this.listeners.get(type) ?? []) fn(ev);
  }
}
