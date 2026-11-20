import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { formatDate } from "./Apps";
import CopyButton from "./CopyButton";
import {
  dropApi,
  type DropNode,
  type NodeSecret,
  type NodeState,
} from "./dropApi";
import { message } from "./errors";

const stateNames: Record<NodeState, string> = {
  active: "Active",
  draining: "Draining",
  disabled: "Disabled",
};

/** A node's agent password, shown once after create or rotate. */
function SecretNotice({ secret }: { secret: NodeSecret }) {
  return (
    <div role="status" className="invite-link">
      <p>
        Agent password for {secret.identity} (shown once; give it to the
        agent as its node-secret credential):
      </p>
      <div className="row">
        <code className="endpoint">{secret.password}</code>
        <CopyButton text={secret.password} />
      </div>
    </div>
  );
}

function NodeForm({ onCreated }: { onCreated: (s: NodeSecret) => void }) {
  const [name, setName] = useState("");
  const [hostId, setHostId] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const created = await dropApi.createNode({
        name: name.trim(),
        host_id: hostId.trim(),
      });
      setName("");
      setHostId("");
      onCreated(created);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-labelledby="node-add-title">
      <h3 id="node-add-title">Add a node</h3>
      <form className="add" onSubmit={submit}>
        <div className="row">
          <label>
            Name
            <input
              required
              maxLength={31}
              autoComplete="off"
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="walter-aws"
            />
          </label>
          <label>
            Host id
            <input
              required
              maxLength={63}
              autoComplete="off"
              value={hostId}
              onChange={(event) => setHostId(event.target.value)}
              placeholder="hostId from reported.json"
            />
          </label>
          <button
            className="primary"
            disabled={busy || !name.trim() || !hostId.trim()}
          >
            Add node
          </button>
        </div>
      </form>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
    </section>
  );
}

function PlaceForm({
  nodes,
  onPlaced,
}: {
  nodes: DropNode[];
  onPlaced: () => void;
}) {
  const [app, setApp] = useState("");
  const [node, setNode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const active = nodes.filter((n) => n.state === "active");
  const run = async (clear: boolean) => {
    setBusy(true);
    setError("");
    setDone("");
    try {
      if (clear) {
        await dropApi.clearPlacement(app.trim());
        setDone(`${app.trim()} is no longer placed.`);
      } else {
        const result = await dropApi.placeApp(app.trim(), node);
        setDone(
          result.moved
            ? `${app.trim()} moved to ${node}.`
            : `${app.trim()} is placed on ${node}.`,
        );
      }
      onPlaced();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <section aria-labelledby="place-title">
      <h3 id="place-title">Place an app</h3>
      <form
        className="add"
        onSubmit={(event) => {
          event.preventDefault();
          void run(false);
        }}
      >
        <div className="row">
          <label>
            App (id or slug)
            <input
              required
              maxLength={32}
              autoComplete="off"
              value={app}
              onChange={(event) => setApp(event.target.value)}
              placeholder="hello-app"
            />
          </label>
          <label>
            Node
            <select
              value={node}
              onChange={(event) => setNode(event.target.value)}
            >
              <option value="">Choose…</option>
              {active.map((n) => (
                <option key={n.id} value={n.name}>
                  {n.name}
                </option>
              ))}
            </select>
          </label>
          <button className="primary" disabled={busy || !app.trim() || !node}>
            Place
          </button>
          <button
            type="button"
            className="secondary"
            disabled={busy || !app.trim()}
            onClick={() => void run(true)}
          >
            Clear placement
          </button>
        </div>
        <p className="quiet">
          A moved app stops on its old node, which keeps its data. The disk
          size is fixed from the first placement.
        </p>
      </form>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {done && <p role="status">{done}</p>}
    </section>
  );
}

function capacityText(node: DropNode): string {
  const a = node.allocatable;
  const vcpu = typeof a.vcpuCount === "number" ? a.vcpuCount : null;
  const memory = typeof a.memoryMib === "number" ? a.memoryMib : null;
  if (vcpu === null && memory === null) return "no report yet";
  return `${vcpu ?? "?"} vCPU · ${memory ?? "?"} MiB allocatable`;
}

function Nodes({
  nodes,
  onChanged,
  onSecret,
}: {
  nodes: DropNode[];
  onChanged: () => void;
  onSecret: (s: NodeSecret) => void;
}) {
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const act = async (node: DropNode, run: () => Promise<unknown>) => {
    setBusy(node.id);
    setError("");
    try {
      await run();
      onChanged();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy("");
    }
  };
  return (
    <section aria-labelledby="nodes-title">
      <h3 id="nodes-title">Nodes</h3>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {nodes.length === 0 ? (
        <p className="empty">No nodes yet.</p>
      ) : (
        <ul className="apps" aria-label="Nodes by name">
          {nodes.map((node) => (
            <li key={node.id} className="row user">
              <p>
                <span>{node.name}</span>{" "}
                <span className="quiet">
                  {node.online ? "online" : "offline"} · {node.apps}{" "}
                  {node.apps === 1 ? "app" : "apps"} · {capacityText(node)} ·
                  seq {node.desired_seq}
                  {node.last_heartbeat
                    ? ` · seen ${formatDate(node.last_heartbeat)}`
                    : ""}
                  {node.agent_version ? ` · agent ${node.agent_version}` : ""}
                </span>
              </p>
              <label>
                State
                <select
                  value={node.state}
                  disabled={busy === node.id}
                  onChange={(event) => {
                    const state = event.target.value as NodeState;
                    if (
                      state === "disabled" &&
                      !window.confirm(
                        `Disable ${node.name}? Its agent is locked out until you make it active again.`,
                      )
                    )
                      return;
                    void act(node, () => dropApi.setNodeState(node.id, state));
                  }}
                >
                  {(Object.keys(stateNames) as NodeState[]).map((s) => (
                    <option key={s} value={s}>
                      {stateNames[s]}
                    </option>
                  ))}
                </select>
              </label>
              <button
                type="button"
                className="secondary"
                disabled={busy === node.id}
                onClick={() => {
                  if (
                    !window.confirm(
                      `Replace the agent password of ${node.name}? The agent stops working until it gets the new one.`,
                    )
                  )
                    return;
                  void act(node, async () =>
                    onSecret(await dropApi.rotateNodeSecret(node.id)),
                  );
                }}
              >
                Rotate password
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** Admins: nodes (hosts) and app placements. */
export default function AdminNodes() {
  const [nodes, setNodes] = useState<DropNode[] | null>(null);
  const [secret, setSecret] = useState<NodeSecret | null>(null);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  const load = useCallback(async () => {
    try {
      const list = await dropApi.listNodes();
      if (!mounted.current) return;
      setNodes(list);
      setError("");
    } catch (e) {
      if (mounted.current) setError(message(e));
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    void load();
    return () => {
      mounted.current = false;
    };
  }, [load]);
  const showSecret = (s: NodeSecret) => {
    setSecret(s);
    void load();
  };
  return (
    <section className="panel" aria-labelledby="admin-nodes-title">
      <h2 id="admin-nodes-title">Nodes</h2>
      <NodeForm onCreated={showSecret} />
      {secret && <SecretNotice secret={secret} />}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {nodes ? (
        <>
          <Nodes
            nodes={nodes}
            onChanged={() => void load()}
            onSecret={showSecret}
          />
          <PlaceForm nodes={nodes} onPlaced={() => void load()} />
        </>
      ) : (
        !error && <p className="quiet">Loading nodes…</p>
      )}
    </section>
  );
}
