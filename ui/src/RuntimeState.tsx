import { useEffect, useState } from "react";
import { dropApi, type AppStatus } from "./dropApi";

function label(status: AppStatus) {
  switch (status.phase) {
    case "running": return "Running";
    case "sleeping": return status.last_hook?.event === "terminate" ? "Terminated" : "Suspended";
    case "stopped": return status.last_hook?.event === "terminate" ? "Terminated" : "Stopped";
    case "archived": return "Archived";
    case "starting": return "Starting";
    case "pending": case "unplaced": return "Pending";
    case "deploying": case "staging": return "Deploying";
    case "failed": case "unhealthy": case "staging_failed": case "secret_failed": return "Needs attention";
    default: return "Status unavailable";
  }
}

export default function RuntimeState({ id }: { id: string }) {
  const [state, setState] = useState("Checking status…");
  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    async function read() {
      try {
        const status = await dropApi.appStatus(id);
        if (active) setState(label(status));
      } catch {
        if (active) setState("Status unavailable");
      }
      if (active) timer = setTimeout(read, 30000);
    }
    void read();
    return () => { active = false; clearTimeout(timer); };
  }, [id]);
  return <span className="dashboard-state" data-running={state === "Running" || undefined}>{state}</span>;
}
