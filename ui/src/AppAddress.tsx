import CopyButton from "./CopyButton";

export function appURL(hostname?: string): string {
  if (!hostname || !/^[a-z0-9-]+(?:\.[a-z0-9-]+)+$/i.test(hostname)) return "";
  return "https://" + hostname;
}

/** A user can follow or copy the assigned address before their app finishes starting. */
export default function AppAddress({ hostname, copyOnly = false }: { hostname?: string; copyOnly?: boolean }) {
  const url = appURL(hostname);
  if (!url) return null;
  return (
    <span className="app-address">
      {copyOnly ? <code>{url}</code> : <a href={url} target="_blank" rel="noreferrer">{url}</a>}
      <CopyButton icon key={url} text={url} label="Copy app URL" />
    </span>
  );
}
