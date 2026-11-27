import { closeOnBackdropClick } from "./dialog";
import DeleteSlider from "./DeleteSlider";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { dropApi, type App, type DeletionReceipt } from "./dropApi";
import { message } from "./errors";
import { viewHashes } from "./routes";

export function DeleteApp({ app, slider = !!(app.official_product || app.developer_private) }: { app: App; slider?:boolean }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [slug, setSlug] = useState("");
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<Awaited<ReturnType<typeof dropApi.getApp>>>();
  useEffect(() => {
    if (!open) return;
    let active = true;
    dialog.current?.showModal();
    dropApi.getApp(app.id, true).then(result => {
      if (active) setDetail(result);
    }).catch(e => { if (active) setError(message(e)); });
    return () => { active = false; };
  }, [open, app.id]);
  const cancel = () => dialog.current?.close();
  async function remove(event?: FormEvent) {
    event?.preventDefault();
    if (busy || (!slider && slug !== app.slug) || !detail?.deletion_inventory) return;
    setBusy(true);
    setError("");
    try {
      await dropApi.deleteApp(app.id, app.slug);
      cancel();
      window.location.hash = viewHashes.apps;
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }
  const inventory = detail?.deletion_inventory;
  return <>
    <button type="button" onClick={() => {
      setSlug(""); setError(""); setDetail(undefined); setOpen(true);
    }}>Delete app permanently</button>
    {open && <dialog ref={dialog} className="delete-app-dialog" aria-labelledby="delete-app-title"
      onClick={busy ? undefined : closeOnBackdropClick}
      onCancel={e => { if (busy) e.preventDefault(); }} onClose={() => setOpen(false)}>
      <h2 id="delete-app-title">Permanently delete {app.name}?</h2>
      <dl className="delete-identifiers">
        <dt>Slug</dt><dd><code>{app.slug}</code></dd>
        {app.hostname && <><dt>Hostname</dt><dd><code>{app.hostname}</code></dd></>}
      </dl>
      {inventory && detail && <ul>
        <li>{detail.app.disk_locked ? `Its persistent disk (${detail.app.disk_mib} MiB) and files will be erased.` : "This app has never been placed on a node. Its disk allocation will be released."}</li>
        {inventory.replica_mode === "enabled" && <li>All versions of this app’s S3 replica objects will be erased.</li>}
        {inventory.replica_mode === "local" && <li>This deployment uses local storage. Any S3 versions retained from earlier deployments will also be erased.</li>}
        {detail.secrets.length > 0 && <li>All versions of {detail.secrets.length} {detail.secrets.length === 1 ? "secret" : "secrets"} will be erased.</li>}
        {inventory.release_count > 0 && <li>{inventory.release_count} release {inventory.release_count === 1 ? "record" : "records"} will be removed. Only unshared release objects will be erased.</li>}
      </ul>}
      {!inventory && !error && <p role="status">Loading affected resources…</p>}
      <form onSubmit={e=>{e.preventDefault();if(!slider)void remove()}}>
        {slider?<DeleteSlider disabled={busy||!inventory} onConfirm={()=>void remove()}/>:<label>Type {app.slug} to confirm
          <input value={slug} onChange={e => setSlug(e.target.value)} autoComplete="off" spellCheck={false} disabled={busy} />
        </label>}
        {error && <p role="alert">{error}</p>}
        <div className="row">
          <button autoFocus type="button" disabled={busy} onClick={cancel}>Keep the app</button>
          {!slider&&<button type="submit" className="delete-confirm" disabled={busy || !inventory || slug !== app.slug}>Delete {app.name}</button>}
        </div>
      </form>
    </dialog>}
  </>;
}

export function DeletionStatus({ id }: { id: string }) {
  const [receipt, setReceipt] = useState<DeletionReceipt>();
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    let timer: number;
    async function read() {
      try {
        const result = await dropApi.listDeletions(id);
        if (!active) return;
        const item = result.deletions[0];
        setReceipt(item);
        setError(item ? "" : "Deletion request not found.");
        if (!item || item.completed) return;
      } catch (e) {
        if (!active) return;
        setError(message(e));
      }
      timer = window.setTimeout(read, 5000);
    }
    void read();
    return () => { active = false; window.clearTimeout(timer); };
  }, [id]);
  return <section className="panel wide" aria-live="polite">
    <p><a href={viewHashes.apps}>← Your apps</a></p>
    {receipt ? <>
      <h2>{receipt.completed ? "Cleanup complete" : "Deletion requested"}</h2>
      <p>App slug <code>{receipt.slug}</code></p>
      <p>{receipt.completed ? "The app’s resources have been removed and its slug released." :
        receipt.waiting_nodes ? "Cleanup pending. Waiting for volume removal confirmation from former nodes." :
        "Cleanup pending. Drop retries unfinished cleanup automatically."}</p>
    </> : !error && <p>Loading deletion status…</p>}
    {error && <p role="alert">{error}</p>}
  </section>;
}
