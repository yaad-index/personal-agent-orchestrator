// A watch of an agent's output (ADR-0018, from the UI by ADR-0056): the node
// streams server-sent events. It is read with fetch rather than EventSource,
// which cannot tell why a watch was refused.

import { APIError, Unauthorized, type WatchLine } from "./api";

// parseEvents takes the events complete in buf, returning them and what is left
// over: an event ends at a blank line. A comment line (": …") is a keepalive.
export function parseEvents(buf: string): { events: { event: string; data: string }[]; rest: string } {
  const events: { event: string; data: string }[] = [];
  let at: number;
  while ((at = buf.indexOf("\n\n")) >= 0) {
    const block = buf.slice(0, at);
    buf = buf.slice(at + 2);
    let event = "message";
    const data: string[] = [];
    for (const line of block.split("\n")) {
      if (line.startsWith(":")) continue;
      const i = line.indexOf(":");
      const field = i < 0 ? line : line.slice(0, i);
      const value = i < 0 ? "" : line.slice(i + 1).replace(/^ /, "");
      if (field === "event") event = value;
      else if (field === "data") data.push(value);
    }
    if (data.length) events.push({ event, data: data.join("\n") });
  }
  return { events, rest: buf };
}

// watch follows the agent's output at level, showing first the last entries
// (0 leaves both to the node), and calls onLine for each line until signal
// aborts it (a normal return) or the node ends the watch (an Error with the
// node's words).
export async function watch(
  agent: string,
  level: number,
  entries: number,
  onLine: (l: WatchLine) => void,
  signal: AbortSignal,
): Promise<void> {
  const q = new URLSearchParams();
  if (level > 0) q.set("level", String(level));
  if (entries > 0) q.set("entries", String(entries));
  let res: Response;
  try {
    res = await fetch(`/v1/watch/${encodeURIComponent(agent)}${q.size ? `?${q}` : ""}`, { credentials: "same-origin", signal });
  } catch (err) {
    if (signal.aborted) return;
    throw err;
  }
  if (res.status === 401) throw new Unauthorized();
  if (!res.ok || !res.body) {
    let msg = res.statusText;
    try {
      const j = await res.json();
      if (j && typeof j.error === "string") msg = j.error;
    } catch {
      // not JSON
    }
    throw new APIError(res.status, msg);
  }
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = "";
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      const parsed = parseEvents(buf + value.replace(/\r\n/g, "\n"));
      buf = parsed.rest;
      for (const e of parsed.events) {
        if (e.event === "line") onLine(JSON.parse(e.data) as WatchLine);
        else if (e.event === "end") throw new Error((JSON.parse(e.data) as { error?: string }).error || "the watch ended");
      }
    }
  } catch (err) {
    if (signal.aborted) return;
    throw err;
  }
  if (!signal.aborted) throw new Error("the watch ended: the node closed it");
}
