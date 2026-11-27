import { useCallback, useEffect, useState, type FormEvent } from "react";
import { formatDate } from "./Apps";
import {
  dropApi,
  type App,
  type Usage,
  type UsageCounts,
  type UsageLimit,
} from "./dropApi";
import { message } from "./errors";

const GB = 2 ** 30;

/** Bytes for people: B, KB, MB or GB (binary, as S3 bills). */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`;
  if (n < GB) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / GB).toFixed(2)} GB`;
}

function formatValue(limit: Pick<UsageLimit, "metric">, n: number): string {
  return limit.metric === "bytes_out" || limit.metric === "stored"
    ? formatBytes(n)
    : n.toLocaleString("en-US");
}

const usd = (n: number) => `$${n.toFixed(4)}`;

const levelLabels: Record<UsageLimit["level"], string> = {
  ok: "Within limits",
  soft: "Past the soft limit",
  hard: "Past the hard limit",
};

/** One limit as a bar from 0 to the hard limit, with the soft limit marked. */
function LimitMeter({ limit }: { limit: UsageLimit }) {
  const share = Math.min(1, limit.value / limit.hard);
  const soft = limit.soft / limit.hard;
  return (
    <li className={`limit limit-${limit.level}`}>
      <div className="limit-head">
        <strong>{limit.label}</strong>
        <span>
          {formatValue(limit, limit.value)} of {formatValue(limit, limit.soft)} soft,{" "}
          {formatValue(limit, limit.hard)} hard
        </span>
      </div>
      <div
        className="meter"
        role="meter"
        aria-label={limit.label}
        aria-valuemin={0}
        aria-valuemax={limit.hard}
        aria-valuenow={Math.min(limit.value, limit.hard)}
      >
        <span className="meter-fill" style={{ width: `${share * 100}%` }} />
        <span className="meter-soft" style={{ left: `${soft * 100}%` }} title="Soft limit" />
      </div>
      <span className="quiet">{levelLabels[limit.level]}</span>
    </li>
  );
}

function hourLabel(hour: string): string {
  const d = new Date(hour);
  return Number.isNaN(d.getTime())
    ? hour
    : d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function requests(c: UsageCounts): number {
  return c.class_a + c.class_b + c.deletes;
}

/** Admins override an app's monthly soft limits (hard limits are ten times). */
function LimitsForm({ app, usage, onSaved }: { app: App; usage: Usage; onSaved: () => void }) {
  const soft = (metric: string) => usage.limits.find((l) => l.metric === metric)?.soft ?? 0;
  const [requestsA, setRequestsA] = useState(String(soft("requests_a")));
  const [requestsB, setRequestsB] = useState(String(soft("requests_b")));
  const [bytesOut, setBytesOut] = useState(String(soft("bytes_out") / GB));
  const [stored, setStored] = useState(String(soft("stored") / GB));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const save = async (reset: boolean, event?: FormEvent<HTMLFormElement>) => {
    event?.preventDefault();
    setError("");
    setStatus("");
    const whole = (v: string, scale: number) => Math.round(Number(v) * scale);
    const input = reset
      ? { reset: true }
      : {
          requests_a: whole(requestsA, 1),
          requests_b: whole(requestsB, 1),
          bytes_out: whole(bytesOut, GB),
          stored_bytes: whole(stored, GB),
        };
    if (!reset && Object.values(input).some((v) => !Number.isFinite(v) || (v as number) < 1)) {
      setError("Every limit must be a positive number.");
      return;
    }
    setBusy(true);
    try {
      await dropApi.setUsageLimits(app.id, input);
      setStatus(reset ? "Limits reset to the defaults." : "Limits saved.");
      onSaved();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form className="add" onSubmit={(event) => void save(false, event)}>
      <h3>Soft limits (admins)</h3>
      <fieldset disabled={busy}>
        <div className="row">
          <label>
            Class A + DELETE requests
            <input name="requests_a" inputMode="numeric" value={requestsA} onChange={(e) => setRequestsA(e.target.value)} />
          </label>
          <label>
            Class B requests
            <input name="requests_b" inputMode="numeric" value={requestsB} onChange={(e) => setRequestsB(e.target.value)} />
          </label>
          <label>
            Bytes sent (GB)
            <input name="bytes_out" inputMode="decimal" value={bytesOut} onChange={(e) => setBytesOut(e.target.value)} />
          </label>
          <label>
            Stored (GB)
            <input name="stored_bytes" inputMode="decimal" value={stored} onChange={(e) => setStored(e.target.value)} />
          </label>
        </div>
      </fieldset>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <p role="status" className="quiet">
        {status}
      </p>
      <div className="row">
        <button className="primary" disabled={busy}>
          Save limits
        </button>
        <button type="button" className="secondary" disabled={busy} onClick={() => void save(true)}>
          Reset to defaults
        </button>
      </div>
    </form>
  );
}

/** The app's Usage tab: S3 usage of its replica prefix. */
export default function UsagePanel({ app }: { app: App }) {
  const [usage, setUsage] = useState<Usage | null>(null);
  const [admin, setAdmin] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      setUsage(await dropApi.getUsage(app.id));
      setError("");
    } catch (e) {
      setError(message(e));
    }
  }, [app.id]);
  useEffect(() => {
    void load();
    dropApi
      .me()
      .then((me) => setAdmin(me.admin))
      .catch(() => {});
  }, [load]);
  if (error)
    return (
      <p role="alert" className="error">
        {error}
      </p>
    );
  if (!usage) return <p className="quiet">Loading usage…</p>;
  const mtd = usage.month_to_date;
  return (
    <div className="usage">
      {!usage.metered && (
        <p className="quiet">This server does not meter S3 usage; the numbers below stay at zero.</p>
      )}
      {usage.exceeded.length > 0 && (
        <ul className="exceeded" aria-label="Limits exceeded this month">
          {usage.exceeded.map((e, i) => (
            <li key={i} className={e.level === "hard" ? "error" : ""}>
              <strong>{e.level === "hard" ? "Hard limit" : "Soft limit"}</strong> {e.message}{" "}
              <span className="quiet">{formatDate(e.created)}</span>
            </li>
          ))}
        </ul>
      )}
      {(usage.budget_refusals ?? 0) > 0 && <p role="status">The node has blocked {usage.budget_refusals?.toLocaleString()} storage requests because it could not reserve credits.</p>}
      <h3>This month ({usage.month}, UTC)</h3>
      <ul className="limits">
        {usage.limits.map((l) => (
          <LimitMeter key={l.metric} limit={l} />
        ))}
      </ul>
      <dl className="facts">
        <dt>Requests</dt>
        <dd>
          {mtd.class_a.toLocaleString("en-US")} Class A, {mtd.class_b.toLocaleString("en-US")} Class B,{" "}
          {mtd.deletes.toLocaleString("en-US")} DELETE
        </dd>
        <dt>Transfer</dt>
        <dd>
          {formatBytes(mtd.bytes_in)} in, {formatBytes(mtd.bytes_out)} out
        </dd>
        <dt>Errors</dt>
        <dd>
          {mtd.status_4xx} client (4xx, of which {mtd.denied} denied), {mtd.status_5xx} server (5xx)
        </dd>
        <dt>Stored</dt>
        <dd>
          {usage.stored ? (
            <>
              {formatBytes(usage.stored.current_bytes)} current, {formatBytes(usage.stored.noncurrent_bytes)} older versions,{" "}
              {usage.stored.objects} objects (inventory of {usage.stored.day})
            </>
          ) : (
            <span className="quiet">No inventory yet</span>
          )}
        </dd>
        <dt>Estimated cost</dt>
        <dd>
          <strong>{usd(usage.estimate.total_usd)}</strong> (Class A {usd(usage.estimate.class_a_usd)}, Class B{" "}
          {usd(usage.estimate.class_b_usd)}, egress {usd(usage.estimate.egress_usd)}, storage {usd(usage.estimate.storage_usd)})
        </dd>
      </dl>
      <p className="quiet">{usage.estimate_note}</p>
      <h3>Last 24 hours</h3>
      {usage.hourly.length === 0 ? (
        <p className="quiet">No S3 requests in the last 24 hours.</p>
      ) : (
        <div className="table-scroll">
          <table className="usage-hours">
            <thead>
              <tr>
                <th scope="col">Hour</th>
                <th scope="col">Requests</th>
                <th scope="col">Class A</th>
                <th scope="col">Class B</th>
                <th scope="col">DELETE</th>
                <th scope="col">Out</th>
                <th scope="col">4xx / 5xx</th>
              </tr>
            </thead>
            <tbody>
              {usage.hourly.map((h) => (
                <tr key={h.hour}>
                  <th scope="row">{hourLabel(h.hour)}</th>
                  <td>{requests(h).toLocaleString("en-US")}</td>
                  <td>{h.class_a.toLocaleString("en-US")}</td>
                  <td>{h.class_b.toLocaleString("en-US")}</td>
                  <td>{h.deletes.toLocaleString("en-US")}</td>
                  <td>{formatBytes(h.bytes_out)}</td>
                  <td>
                    {h.status_4xx} / {h.status_5xx}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="quiet">
        Past a soft limit Drop records an event; past a hard limit (ten times the soft one) it also stops the
        app once. Logs arrive from S3 with a delay of up to a few hours.
      </p>
      {admin && <LimitsForm key={JSON.stringify(usage.limits_override)} app={app} usage={usage} onSaved={() => void load()} />}
    </div>
  );
}
