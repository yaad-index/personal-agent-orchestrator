// The node's management API, called from the page pao serves. The session
// cookie pao sets at login carries the identity; every call also sends the
// header that marks it as the UI's own, which a page on another site cannot
// add, so a mutating call cannot be forged from elsewhere.

export class Unauthorized extends Error {
  constructor() {
    super("not signed in");
  }
}

export class APIError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "X-Pao-UI": "1" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 401) throw new Unauthorized();
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const j = await res.json();
      if (j && typeof j.error === "string") msg = j.error;
    } catch {
      // not JSON: keep the status text
    }
    throw new APIError(res.status, msg);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export interface Agent {
  name: string;
  tags: string[];
  backend: string;
  state: string;
  temporary: boolean;
}

export interface NodeInfo {
  node: string;
  build: string;
  agents: Agent[];
}

export interface BudgetWindow {
  used_percent: number;
  resets_at?: string;
}

export interface Budget {
  account: string;
  state: string;
  week?: BudgetWindow;
  five_hour?: BudgetWindow;
  fault?: string;
  retrying?: string;
}

export const login = (token: string) => call<void>("POST", "/ui/api/login", { token });
export const logout = () => call<void>("POST", "/ui/api/logout");
export const session = () => call<void>("GET", "/ui/api/session");
export const node = () => call<NodeInfo>("GET", "/v1/node");
export const budget = () => call<Budget[]>("GET", "/v1/budget");
