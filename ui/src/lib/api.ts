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
// An empty channel leaves it to pao, which picks the node's only channel.
export const spawn = (recipe: string, name: string, channel = "") => call<Spawned>("POST", "/v1/temp", { recipe, name, channel });
export const kill = (name: string) => call<KillResult>("DELETE", `/v1/temp/${encodeURIComponent(name)}`);

export type TaskStatus = "open" | "assigned" | "running" | "blocked" | "done" | "failed" | "canceled";

export interface TaskAttachments {
  tools?: string[];
  scripts?: string[];
  // the name of a secret, never its value
  secret?: string;
}

export interface TaskLogEntry {
  at: string;
  by: string;
  kind: string;
  status?: TaskStatus;
  text?: string;
}

export interface Task {
  id: string;
  title: string;
  description: string;
  done: string;
  how?: string;
  tags: string[];
  owner: string;
  parent?: string;
  attachments?: TaskAttachments;
  status: TaskStatus;
  assignee?: string;
  one_off?: boolean;
  removed?: boolean;
  created: string;
  updated: string;
  log?: TaskLogEntry[];
  dropped?: number;
  folder: string;
}

export interface NewTask {
  title: string;
  description: string;
  done: string;
  how?: string;
  tags: string[];
  parent?: string;
  attachments?: TaskAttachments;
}

export const tasks = () => call<Task[]>("GET", "/v1/tasks");
export const getTask = (id: string) => call<Task>("GET", `/v1/tasks/${encodeURIComponent(id)}`);
export const createTask = (t: NewTask) => call<Task>("POST", "/v1/tasks", t);
export const cancelTask = (id: string, reason: string) =>
  call<Task>("POST", `/v1/tasks/${encodeURIComponent(id)}/cancel`, { reason });

export interface Channel {
  name: string;
  kind: string;
  groups: string[];
}

export interface Binding {
  agent: string;
  stream?: string;
  to: string;
  level?: string;
  prefix?: string;
  // "config" or "runtime"; a config binding is changed in the configuration
  from?: string;
}

export interface BindingList {
  bindings: Binding[];
}

export interface ChannelDefault {
  channel?: string;
  agent: string;
}

export interface ChannelDefaults {
  version: number;
  defaults: ChannelDefault[];
}

export interface DefaultChange {
  channel: string;
  agent?: string;
  previous?: string;
  left?: string;
}

export const channels = () => call<Channel[]>("GET", "/v1/channels");
export const bindings = () => call<BindingList>("GET", "/v1/bindings");
export const bind = (b: Binding) => call<Binding>("POST", "/v1/bindings", b);
export const unbind = (b: Binding) => {
  const q = new URLSearchParams({ agent: b.agent, to: b.to });
  if (b.stream) q.set("stream", b.stream);
  return call<Binding>("DELETE", `/v1/bindings?${q}`);
};
export const channelDefaults = () => call<ChannelDefaults>("GET", "/v1/channels/defaults");
export const setDefault = (channel: string, agent: string) =>
  call<DefaultChange>("PUT", `/v1/channels/${encodeURIComponent(channel)}/default`, { agent });
export const clearDefault = (channel: string) =>
  call<DefaultChange>("DELETE", `/v1/channels/${encodeURIComponent(channel)}/default`);

export interface HarvestFile {
  path: string;
  size: number;
}

export interface HarvestSkip {
  path: string;
  reason: string;
}

export interface Harvest {
  id: string;
  agent: string;
  tags: string[];
  requester: string;
  time: string;
  complete: boolean;
  files: HarvestFile[];
  bytes: number;
  skipped?: HarvestSkip[];
  skipped_total?: number;
}

export interface Rejected {
  channel: string;
  sender: string;
  kind: string;
  reason: string;
  first: string;
  last: string;
  count: number;
  // a stranger's content: shown as text, never acted on
  payload: string;
}

export const harvests = () => call<Harvest[]>("GET", "/v1/harvests");
export const harvest = (id: string) => call<Harvest>("GET", `/v1/harvests/${encodeURIComponent(id)}`);
export const deleteHarvest = (id: string) => call<void>("DELETE", `/v1/harvests/${encodeURIComponent(id)}`);
// A harvested file downloads; the node serves it as an attachment, never as a page.
export const harvestFileURL = (id: string, path: string) =>
  `/v1/harvests/${encodeURIComponent(id)}/file?${new URLSearchParams({ path })}`;
export const rejected = () => call<Rejected[]>("GET", "/v1/inbound/rejected");

export interface WatchLine {
  // RFC 3339, empty when the entry has none
  time?: string;
  text: string;
  // the recent past, sent before the output that arrives after the watch began
  history?: boolean;
}

