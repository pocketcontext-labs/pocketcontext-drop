import HelpTip from "./HelpTip";
import { useCallback, useEffect, useRef, useState } from "react";
import { op } from "./dropApi";
import { message } from "./errors";

interface Account { id: string; email: string; balance: number; purchases: number }
interface Ledger {
  account: string;
  state: { balance: number; reserved?: number; pending_compute_seconds?: number; paid: boolean; trial_seconds: number };
  settings: { pack: number; euro_micros_per_credit: number };
  entries: { id: number; kind: string; delta: number; app: string; created: number }[];
  apps: { id: string; name: string; rate: number; compute: number; s3: number }[];
}
const credits = (micro: number) => (micro / 1_000_000).toLocaleString(undefined, { maximumFractionDigits: 6 });

export default function Credits({ admin, onPurchase }: { admin: boolean; onPurchase?: () => void }) {
  const [page, setPage] = useState<{ accounts: Account[]; has_more: boolean; offset: number; query: string } | null>(null);
  const [selected, setSelected] = useState<Account | null>(null);
  const account = selected?.id ?? "";
  const [query, setQuery] = useState("");
  const [offset, setOffset] = useState(0);
  const currentPage = page?.offset === offset && page.query === query ? page : null;
  const accounts = currentPage?.accounts ?? [];
  const [data, setData] = useState<Ledger | null>(null);
  const [before, setBefore] = useState(0);
  const [error, setError] = useState("");
  const [delta, setDelta] = useState("");
  const [busy, setBusy] = useState(false);
  const [version, setVersion] = useState(0);
  const pending = useRef<{ intent: string; key: string } | null>(null);
  const refresh = useCallback(() => setVersion(v => v + 1), []);
  const pageSize = 25;
  useEffect(() => {
    if (!admin) return;
    let active = true;
    const timer = window.setTimeout(() => {
      void op<{ accounts: Account[]; has_more: boolean }>("credits.accounts", { query, offset, limit: pageSize })
        .then(result => {
          if (active) setPage({ ...result, query, offset });
        })
        .catch(e => { if (active) { setPage({ accounts: [], has_more: false, query, offset }); setError(message(e)); } });
    }, 180);
    return () => { active = false; window.clearTimeout(timer); };
  }, [admin, query, offset, version]);
  useEffect(() => {
    let active = true;
    async function load() {
      const result = await op<Ledger>("credits.get", { account, before });
      if (active) { setData(result); setError(""); }
    }
    void load().catch(e => { if (active) setError(message(e)); });
    const timer = window.setInterval(refresh, 30_000);
    return () => { active = false; window.clearInterval(timer); };
  }, [account, admin, before, refresh, version]);

  async function submit() {
    if (!data || busy) return;
    const amount = Math.round(Number(delta) * 1_000_000);
    const intent = admin ? `adjust:${data.account}:${amount}` : "buy";
    if (pending.current?.intent !== intent) pending.current = { intent, key: crypto.randomUUID() };
    setBusy(true);
    try {
      if (admin) await op("credits.adjust", { account: data.account, microcredits: amount }, pending.current.key);
      else await op("credits.buy", {}, pending.current.key);
      window.dispatchEvent(new Event("drop-credits-changed"));
      pending.current = null; setDelta(""); setBefore(0); refresh();
      if (!admin) onPurchase?.();
    } catch (e) { setError(message(e)); }
    finally { setBusy(false); }
  }

  const rate = data?.apps.reduce((total, a) => total + a.rate, 0) ?? 0;
  return <section className="panel wide credits-page">
    <h2>Credits</h2>
    {error && <p role="alert">{error}</p>}
    {admin && <fieldset>
      <legend>Account</legend>
      <label>Find an account <input type="search" value={query} placeholder="Email or user ID" onChange={e => {
        setQuery(e.target.value); setOffset(0);
      }} /></label>
      <label>View balance <select value={account} onChange={e => {
        setData(null); setBefore(0); setSelected(accounts.find(a => a.id === e.target.value) ?? null);
      }}>
        <option value="">My account</option>
        {selected && !accounts.some(a => a.id === selected.id) && <option value={selected.id}>{selected.email}</option>}
        {accounts.map(a => <option key={a.id} value={a.id}>{a.email} · {credits(a.balance)} credits · purchased {credits(a.purchases)}</option>)}
      </select></label>
      <div className="row">
        <button className="secondary" disabled={offset === 0} onClick={() => setOffset(n => Math.max(0, n - pageSize))}>Previous</button>
        <button className="secondary" disabled={!currentPage?.has_more} onClick={() => setOffset(n => n + pageSize)}>Next</button>
        <span className="quiet">{!currentPage ? "Loading accounts…" : accounts.length ? `${offset + 1}–${offset + accounts.length}` : "No accounts found"}</span>
      </div>
    </fieldset>}
    {data && <>
      <p className="credit-balance"><strong>{credits(data.state.balance)} credits</strong>  {rate} credits per awake hour <HelpTip topic="awake hours">An awake hour is an hour a workspace is running, even if you close your browser. Sleeping or stopped workspaces use no running-time credits. Storage can have separate charges.</HelpTip></p>
      {(data.state.reserved ?? 0) > 0 && <p>{credits(data.state.reserved ?? 0)} credits reserved for storage requests.</p>}
      {(data.state.pending_compute_seconds ?? 0) > 0 && <p>{data.state.pending_compute_seconds} awake seconds await storage settlement.</p>}
      {rate > 0 && data.state.balance > 0 && <p>About {(data.state.balance / 1_000_000 / rate).toFixed(1)} hours left at this compute rate.</p>}
      {data.state.balance === 0 && <p>{Math.ceil((3600 - data.state.trial_seconds) / 60)} shared awake minutes left. Workspaces run locally until the buffer runs out, then stop with their data retained. A top-up resets the buffer to 60 minutes.</p>}
      {admin ?
        <label>Adjustment in credits <input type="number" step="0.000001" value={delta} onChange={e => setDelta(e.target.value)} /></label> :
        <p>Simulated purchase. No payment is collected.</p>}
      <button className="primary" disabled={busy || (admin && !Number(delta))} onClick={() => void submit()}>{admin ? "Apply adjustment" :
        `Buy ${data.settings.pack} credits (€${(data.settings.pack * data.settings.euro_micros_per_credit / 1_000_000).toFixed(2)})`}</button>
      {data.apps.length > 0 && <><h3>Workspace usage</h3><div className="credit-table"><table><thead><tr><th>Workspace</th><th>Credits/hour</th><th>Running costs (credits)</th><th>Storage costs (credits)</th></tr></thead><tbody>
        {data.apps.map(a => <tr key={a.id}><td>{a.name}</td><td>{a.rate}</td><td>{credits(a.compute)}</td><td>{credits(a.s3)}</td></tr>)}
      </tbody></table></div></>}
      <details className="credit-history"><summary>Transaction history</summary>
      {data.entries.length ? <div className="credit-table"><table><thead><tr><th>Time</th><th>Entry</th><th>Credits</th></tr></thead><tbody>{data.entries.map(e => <tr key={e.id}>
        <td>{new Date(e.created * 1000).toLocaleString()}</td><td>{e.kind === "s3" ? "Storage" : e.kind === "compute" ? "Workspace runtime" : e.kind}</td><td>{credits(e.delta)}</td></tr>)}</tbody></table></div> : <p>No entries.</p>}
      {data.entries.length === 100 && <button onClick={() => setBefore(data.entries[data.entries.length-1].id)}>Older entries</button>}
      {before > 0 && <button onClick={() => setBefore(0)}>Latest entries</button>}
      </details>
    </>}
  </section>;
}
