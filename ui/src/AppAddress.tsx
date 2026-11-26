import CopyButton from "./CopyButton";

export function appURL(hostname?: string): string {
  if (!hostname || !/^[a-z0-9-]+(?:\.[a-z0-9-]+)+$/i.test(hostname)) return "";
  return "https://" + hostname;
}

/** A user can follow or copy the assigned address before their app finishes starting. */
export default function AppAddress({ hostname }: { hostname?: string }) {
  const url = appURL(hostname);
  if (!url) return null;
  return (
    <span className="app-address">
      <a href={url} target="_blank" rel="noreferrer">{url}</a>
      <CopyButton key={url} text={url} label="Copy app URL" />
    </span>
  );
}
