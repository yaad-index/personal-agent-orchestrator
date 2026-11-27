// How a profile's status reads to the operator.
export function statusLabel(status: string): string {
  switch (status) {
    case "loaded":
      return "Loaded";
    case "restart_to_apply":
      return "Restart to apply";
    case "not_applied":
      return "Not applied";
    case "invalid":
      return "Invalid";
  }
  return status;
}

export function statusHelp(status: string): string {
  switch (status) {
    case "restart_to_apply":
      return "Saved while the agent runs: it keeps the profile it started with until it is restarted.";
    case "not_applied":
      return "The file differs from what the node runs, as when it was edited outside pao. Save it to apply it.";
    case "invalid":
      return "The node would not load this file. Fix it and save.";
  }
  return "";
}
