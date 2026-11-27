import type { TaskStatus } from "./api";

// Terminal statuses end a task; nothing more happens to it.
export const terminal = (s: TaskStatus) => s === "done" || s === "failed" || s === "canceled";

// A status that wants the operator's eye is shown as a warning.
export const warns = (s: TaskStatus) => s === "blocked" || s === "failed";

// list splits a comma- or newline-separated field into its entries.
export const list = (v: string) =>
  v
    .split(/[,\n]/)
    .map((s) => s.trim())
    .filter(Boolean);
