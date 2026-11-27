import HelpTip from "./HelpTip";
import { useEffect, useState } from "react";
import { api, type ConnectedApp, type Connection } from "./api";
import CopyButton from "./CopyButton";

const dateFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });

function ConnectedApps() {
  const [apps, setApps] = useState<ConnectedApp[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const load = () =>
    api
      .connectedApps()
      .then((list) => {
        setApps(list);
        setError("");
      })
      .catch(() =>
        setError("Could not load connected apps. Refresh to try again."),
      );
  useEffect(() => {
    void load();
  }, []);
  const revoke = async (app: ConnectedApp) => {
    if (!window.confirm(`Disconnect ${app.name}? It loses access right away.`))
      return;
    setBusy(app.id);
    try {
      await api.revokeConnectedApp(app.id);
      await load();
    } catch {
      setError(`Could not disconnect ${app.name}. Try again.`);
    } finally {
      setBusy("");
    }
  };
  return (
    <section aria-labelledby="connected-title">
      <h3 id="connected-title">Connected apps</h3>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {apps && !apps.length && !error && (
        <p className="empty">No apps connected yet.</p>
      )}
      {apps && apps.length > 0 && (
        <ul className="apps" aria-label="Connected apps">
          {apps.map((app) => (
            <li key={app.id} className="row">
              <p>
                <span>{app.name}</span>{" "}
                <span className="quiet">
                  connected {dateFormat.format(new Date(app.created * 1000))}
                </span>
              </p>
              <button
                type="button"
                className="secondary"
                disabled={busy === app.id}
                onClick={() => void revoke(app)}
              >
                Disconnect
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** How to connect an assistant to Drop through remote MCP. */
export default function Connect() {
  const [connection, setConnection] = useState<Connection | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let active = true;
    api.connection().then(
      (value) => {
        if (active) setConnection(value);
      },
      () => {
        if (active) setFailed(true);
      },
    );
    return () => {
      active = false;
    };
  }, []);
  return (
    <section className="panel connect-guide" aria-labelledby="connect-title">
      <h2 id="connect-title">Connect an assistant</h2>
      {!connection && !failed && (
        <p role="status" className="quiet">
          Checking how this Drop is set up…
        </p>
      )}
      {failed && (
        <p role="alert" className="error">
          Could not check how this Drop is set up. Refresh to try again.
        </p>
      )}
      {connection?.kind === "local" && (
        <p>
          Remote MCP is available on the hosted Drop only. This local server is
          for developing the reader.
        </p>
      )}
      {connection?.kind === "mcp" && (
        <div className="connect-setup">
          <ol>
            <li>
              <strong>Add this address to your assistant</strong>{" "}
              <HelpTip topic="the MCP address">Open your assistant’s settings and look for connectors or MCP servers. Choose the option to add a server, then paste this address.</HelpTip>
              <p>Use it wherever your assistant asks for an MCP server.</p>
              <div className="connect-address">
                <code>{connection.url}</code>
                <CopyButton text={connection.url} />
              </div>
            </li>
            <li>
              <strong>Approve the connection</strong>
              <p>Your assistant will open Drop. Sign in with the account you want to use, then approve access.</p>
            </li>
            <li>
              <strong>Try it in your assistant</strong>
              <p>Go back to your chat and ask, “Which Drop workspaces do I have?”</p>
            </li>
          </ol>
          <ConnectedApps />
        </div>
      )}
    </section>
  );
}
