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
  // An answer with no body, such as 204 or 202, has nothing to read.
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
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

// The shapes below are the node's own JSON (internal/channel/local): a field
// named here must be one the node sends.

export interface BudgetWindow {
  // left is the share of the window left, in percent.
  left: number;
  resets?: string;
}

export interface Budget {
  account: string;
  // state is "unmetered", "readable" or "fault".
  state: string;
  week?: BudgetWindow;
  five_hour?: BudgetWindow;
  fault?: string;
  retrying?: string;
  headroom: boolean;
}

export interface Profile {
  file: string;
  // name is the agent the profile defines; empty for an invalid file.
  name: string;
  backend?: string;
  tags: string[];
  // status is "loaded", "restart_to_apply", "not_applied" or "invalid".
  status: string;
  running: boolean;
  error?: string;
}

export interface ProfileFile extends Profile {
  text: string;
}

export interface ProfileDraft {
  name: string;
  workdir: string;
  model: string;
  tags: string[];
}

export const login = (token: string) => call<void>("POST", "/ui/api/login", { token });
export const logout = () => call<void>("POST", "/ui/api/logout");
export const session = () => call<void>("GET", "/ui/api/session");
export const node = () => call<NodeInfo>("GET", "/v1/node");
export const budget = () => call<Budget[]>("GET", "/v1/budget");
export const profiles = () => call<Profile[]>("GET", "/v1/profiles");
export const profile = (file: string) => call<ProfileFile>("GET", `/v1/profiles/${encodeURIComponent(file)}`);
export const createProfile = (text: string) => call<Profile>("POST", "/v1/profiles", { text });
export const saveProfile = (file: string, text: string) => call<Profile>("PUT", `/v1/profiles/${encodeURIComponent(file)}`, { text });
export const draftProfile = (d: ProfileDraft) => call<{ text: string }>("POST", "/v1/profiles/draft", d);
export const restartNode = () => call<void>("POST", "/v1/node/restart");
export const restartAgent = (name: string) => call<void>("POST", `/v1/agents/${encodeURIComponent(name)}/restart`);

export interface Question {
  id: string;
  agent: string;
  tool: string;
  text: string;
  asked: string;
}

export interface Recipe {
  name: string;
  description: string;
  tags: string[];
}

export interface Spawned {
  name: string;
}

export interface KillResult {
  harvest: { id: string; complete: boolean };
  workdir_kept: boolean;
}

export const questions = () => call<Question[]>("GET", "/v1/questions");
export const decide = (id: string, decision: "allow" | "always" | "deny", reason = "") =>
  call<void>("POST", "/v1/decisions", { id, decision, reason });
export const recipes = () => call<Recipe[]>("GET", "/v1/recipes");
export const agentAction = (name: string, action: "start" | "stop" | "restart") =>
  call<void>("POST", `/v1/agents/${encodeURIComponent(name)}/${action}`);
export const spawn = (recipe: string, name: string) => call<Spawned>("POST", "/v1/temp", { recipe, name, channel: "" });
export const kill = (name: string) => call<KillResult>("DELETE", `/v1/temp/${encodeURIComponent(name)}`);
