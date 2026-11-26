import { useCallback, useEffect, useRef, useState } from "react";
import { op } from "./dropApi";
import { message } from "./errors";

interface Account { id: string; email: string; balance: number; purchases: number }
interface Ledger {
  account: string;
  state: { balance: number; paid: boolean; trial_seconds: number };
  settings: { pack: number; euro_micros_per_credit: number };
  entries: { id: number; kind: string; delta: number; app: string; created: number }[];
  apps: { id: string; name: string; rate: number; compute: number; s3: number }[];
}
const credits = (micro: number) => (micro / 1_000_000).toLocaleString(undefined, { maximumFractionDigits: 6 });

export default function Credits({ admin, onPurchase }: { admin: boolean; onPurchase?: () => void }) {
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [account, setAccount] = useState("");
  const [data, setData] = useState<Ledger | null>(null);
  const [before, setBefore] = useState(0);
  const [error, setError] = useState("");
  const [delta, setDelta] = useState("");
  const [busy, setBusy] = useState(false);
  const [version, setVersion] = useState(0);
  const pending = useRef<{ intent: string; key: string } | null>(null);
  const refresh = useCallback(() => setVersion(v => v + 1), []);
  useEffect(() => {
    let active = true;
    async function load() {
      if (admin) {
        const all: Account[] = [];
        for (;;) {
          const res = await op<{ accounts: Account[]; has_more: boolean }>("credits.accounts", { offset: all.length, limit: 100 });
          all.push(...res.accounts);
          if (!res.has_more || !active) break;
        }
        if (active) setAccounts(all);
      }
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
  return <section className="panel wide">
    <h2>Credits</h2>
    {error && <p role="alert">{error}</p>}
    {admin && <label>Account <select value={account} onChange={e => { setData(null); setBefore(0); setAccount(e.target.value); }}>
      <option value="">My account</option>
      {accounts.map(a => <option key={a.id} value={a.id}>{a.email} · {credits(a.balance)} credits · purchased {credits(a.purchases)}</option>)}
    </select></label>}
    {data && <>
      <p><strong>{credits(data.state.balance)} credits</strong> · {rate} credits per awake hour</p>
      {rate > 0 && data.state.balance > 0 && <p>About {(data.state.balance / 1_000_000 / rate).toFixed(1)} hours left at this compute rate.</p>}
      {!data.state.paid && <p>{Math.ceil((3600 - data.state.trial_seconds) / 60)} awake minutes left. Your apps are archived after 60 awake minutes unless you buy credits. During the trial nothing is backed up.</p>}
      {admin ?
        <label>Adjustment in credits <input type="number" step="0.000001" value={delta} onChange={e => setDelta(e.target.value)} /></label> :
        <p>Simulated purchase. No payment is collected.</p>}
      <button disabled={busy || (admin && !Number(delta))} onClick={() => void submit()}>{admin ? "Apply adjustment" :
        `Buy ${data.settings.pack} credits (€${(data.settings.pack * data.settings.euro_micros_per_credit / 1_000_000).toFixed(2)})`}</button>
      {data.apps.length > 0 && <><h3>App usage</h3><table><thead><tr><th>App</th><th>Credits/hour</th><th>Compute credits</th><th>S3 credits</th></tr></thead><tbody>
        {data.apps.map(a => <tr key={a.id}><td>{a.name}</td><td>{a.rate}</td><td>{credits(a.compute)}</td><td>{credits(a.s3)}</td></tr>)}
      </tbody></table></>}
      <h3>Ledger</h3>
      {data.entries.length ? <table><thead><tr><th>Time</th><th>Entry</th><th>Credits</th></tr></thead><tbody>{data.entries.map(e => <tr key={e.id}>
        <td>{new Date(e.created * 1000).toLocaleString()}</td><td>{e.kind}</td><td>{credits(e.delta)}</td></tr>)}</tbody></table> : <p>No entries.</p>}
      {data.entries.length === 100 && <button onClick={() => setBefore(data.entries[data.entries.length-1].id)}>Older entries</button>}
      {before > 0 && <button onClick={() => setBefore(0)}>Latest entries</button>}
    </>}
  </section>;
}
