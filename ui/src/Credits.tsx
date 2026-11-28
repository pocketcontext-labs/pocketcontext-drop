import HelpTip from "./HelpTip";
import { useCallback, useEffect, useRef, useState } from "react";
import { op } from "./dropApi";
import { message } from "./errors";

interface Account { id: string; email: string; balance: number; purchases: number }
interface Ledger {
  account: string;
  state: { balance: number; reserved?: number; pending_compute_seconds?: number; paid: boolean; trial_seconds: number };
  settings: { pack: number; euro_micros_per_credit: number };
  has_more: boolean;
  entries: { id: number; kind: string; delta: number; app: string; created: number }[];
  apps: { id: string; name: string; rate: number; compute: number; s3: number; backup_storage?: number }[];
}
const entryLabel = (kind: string) => ({ compute: "App runtime", s3: "Storage", backup_storage: "Backup storage" }[kind] ?? kind);
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
  const [cursors, setCursors] = useState<number[]>([0]);
  const before = cursors[cursors.length - 1];
  const [historyQuery, setHistoryQuery] = useState("");
  const [kind, setKind] = useState("");
  const [historyLoading, setHistoryLoading] = useState(true);
  const [error, setError] = useState("");
  const [delta, setDelta] = useState("");
  const [busy, setBusy] = useState(false);
  const [version, setVersion] = useState(0);
  const pending = useRef<{ intent: string; key: string } | null>(null);
  const refresh = useCallback(() => setVersion(v => v + 1), []);
  const supportsHistoryFilters = useRef<boolean | undefined>(undefined);
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
    if (admin && !account) return;
    async function load() {
      let result: Ledger;
      if (supportsHistoryFilters.current === true) {
        result = await op<Ledger>("credits.get", { account, before, limit: pageSize, query: historyQuery, kind });
      } else {
        // Both server versions accept these fields. Newer responses include has_more.
        result = await op<Ledger>("credits.get", { account, before });
        if (!active) return;
        supportsHistoryFilters.current = typeof result.has_more === "boolean";
        const search = historyQuery.trim().toLowerCase();
        const names = new Map(result.apps.map(app => [app.id, app.name]));
        const matches: Ledger["entries"] = [];
        let chunk = result;
        while (active) {
          for (const entry of chunk.entries) {
            const label = entryLabel(entry.kind);
            if ((!kind || entry.kind === kind) && [entry.app, names.get(entry.app), entry.kind, label]
              .some(value => (value || "").toLowerCase().includes(search))) matches.push(entry);
          }
          if (matches.length > pageSize || chunk.entries.length < 100 || chunk.has_more === false) break;
          chunk = await op<Ledger>("credits.get", { account, before: chunk.entries[chunk.entries.length - 1].id });
        }
        result = { ...result, entries: matches.slice(0, pageSize), has_more: matches.length > pageSize || (result.has_more === true && !search && !kind) };
      }
      if (active) { setData(result); setHistoryLoading(false); setError(""); }
    }
    const loadTimer = window.setTimeout(() => void load().catch(e => { if (active) { setHistoryLoading(false); setError(message(e)); } }), 150);
    const timer = window.setInterval(refresh, 30_000);
    return () => { active = false; window.clearTimeout(loadTimer); window.clearInterval(timer); };
  }, [admin, account, before, historyQuery, kind, refresh, version]);

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
      pending.current = null; setDelta(""); setCursors([0]); refresh();
      if (!admin) onPurchase?.();
    } catch (e) { setError(message(e)); }
    finally { setBusy(false); }
  }

  const euroFormat = new Intl.NumberFormat(undefined, {style: "currency", currency: "EUR"});
  function euro(microcredits: number) {
    const amount = microcredits / 1e6 * (data?.settings.euro_micros_per_credit ?? 0) / 1e6;
    if (amount !== 0 && Math.abs(amount) < 0.01) return `${amount < 0 ? "−" : ""}< ${euroFormat.format(0.01)}`;
    return euroFormat.format(amount);
  }
  const rate = data?.apps.reduce((total, a) => total + a.rate, 0) ?? 0;
  return <section className="panel wide credits-page">
    <h2>Credits</h2>
    {error && <p role="alert">{error}</p>}
    {admin && <fieldset className="credit-account-picker">
      <legend>Account</legend>
      <label>Find an account <input type="search" value={query} placeholder="Email or user ID" onChange={e => {
        setQuery(e.target.value); setOffset(0);
      }} /></label>
      <label>View balance <select value={account} onChange={e => {
        setData(null); setCursors([0]); setSelected(accounts.find(a => a.id === e.target.value) ?? null);
      }}>
        <option value="">Select a customer account</option>
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
      <p className="credit-balance"><strong>{credits(data.state.balance)} credits</strong><span className="balance-euro">≈ {euro(data.state.balance)}</span> Up to {rate} credits per hour for currently awake apps (≈ {euro(rate * 1e6)}/h) <HelpTip topic="awake hours">This is the compute cost if the apps that are awake now run for a full hour. Other apps waking up can raise this rate. An app that stays suspended overnight uses zero compute credits overnight. S3 storage is billed separately.</HelpTip></p>
      {(data.state.reserved ?? 0) > 0 && <p>{credits(data.state.reserved ?? 0)} credits reserved for storage requests.</p>}
      {(data.state.pending_compute_seconds ?? 0) > 0 && <p>{data.state.pending_compute_seconds} awake seconds await storage settlement.</p>}
      {rate > 0 && data.state.balance > 0 && <p>About {(data.state.balance / 1_000_000 / rate).toFixed(1)} hours at this compute rate. <HelpTip topic="remaining hours">This estimate divides your balance by the current hourly compute rate. Your balance can last longer when apps sleep. The estimate excludes storage charges.</HelpTip></p>}
      {data.state.balance === 0 && <p>{Math.ceil((3600 - data.state.trial_seconds) / 60)} shared awake minutes left. Apps run locally until the buffer runs out, then stop with their data retained. A top-up resets the buffer to 60 minutes.</p>}
      {admin ?
        <label className="credit-adjustment">Adjustment in credits <input type="number" step="0.000001" value={delta} onChange={e => setDelta(e.target.value)} /></label> :
        <p>Simulated purchase. No payment is collected.</p>}
      <button className="primary" disabled={busy || (admin && !Number(delta))} onClick={() => void submit()}>{admin ? "Apply adjustment" :
        `Buy ${data.settings.pack} credits (€${(data.settings.pack * data.settings.euro_micros_per_credit / 1_000_000).toFixed(2)})`}</button>
      {data.apps.length > 0 && <><h3>App usage <HelpTip topic="app usage">Credits/hour shows each app’s current compute rate. Running costs are credits used for time already spent awake. Storage costs cover S3 usage and can continue while the app is suspended.</HelpTip></h3><div className="credit-table credit-usage"><table><thead><tr><th>App</th><th>Credits/hour</th><th>Running costs (credits)</th><th>Storage costs (credits)</th><th>Backup storage (credits)</th></tr></thead><tbody>
        {data.apps.map(a => <tr key={a.id}><td>{a.name}</td><td data-label="Credits/hour">{a.rate} <small>({euro(a.rate * 1e6)}/h)</small></td><td data-label="Running costs (credits)">{credits(a.compute)} <small>({euro(a.compute)})</small></td><td data-label="Storage costs (credits)">{credits(a.s3)} <small>({euro(a.s3)})</small></td><td data-label="Backup storage (credits)">{credits(a.backup_storage ?? 0)} <small>({euro(a.backup_storage ?? 0)})</small></td></tr>)}
      </tbody></table></div></>}
      <details className="credit-history"><summary>Transaction history</summary>
      <div className="row">
        <label>Find transactions <input type="search" placeholder="App name or entry type" value={historyQuery} onChange={e => {
          setHistoryLoading(true); setHistoryQuery(e.target.value); setCursors([0]);
        }} /></label>
        <label>Entry type <select value={kind} onChange={e => {
          setHistoryLoading(true); setKind(e.target.value); setCursors([0]);
        }}><option value="">All types</option><option value="compute">App runtime</option><option value="s3">Storage</option><option value="backup_storage">Backup storage</option><option value="purchase">Purchase</option><option value="adjustment">Adjustment</option></select></label>
      </div>
      {historyLoading ? <p role="status">Loading transactions…</p> : data.entries.length ? <div className="credit-table"><table><thead><tr><th>Time</th><th>App</th><th>Entry</th><th>Credits</th></tr></thead><tbody>{data.entries.map(e => <tr key={e.id}>
        <td>{new Date(e.created * 1000).toLocaleString()}</td><td>{data.apps.find(a => a.id === e.app)?.name || e.app || "—"}</td><td>{entryLabel(e.kind)}</td><td>{credits(e.delta)} <small>({euro(e.delta)})</small></td></tr>)}</tbody></table></div> : <p>No matching transactions.</p>}
      {data.has_more && <button className="secondary" disabled={historyLoading} onClick={() => {
        setHistoryLoading(true); setCursors([...cursors, data.entries[data.entries.length - 1].id]);
      }}>Older transactions</button>}
      {before > 0 && <button className="secondary" disabled={historyLoading} onClick={() => {
        setHistoryLoading(true); setCursors(cursors.slice(0, -1));
      }}>Newer transactions</button>}
      </details>
    </>}
  </section>;
}
