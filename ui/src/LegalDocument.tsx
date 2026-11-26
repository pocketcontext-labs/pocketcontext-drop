import Brand from "./Brand";

declare const __DROP_LEGAL_DOCUMENTS__: Record<string, string>;

export function publishedLegalDocument(hash: string): string | undefined {
  return Object.hasOwn(__DROP_LEGAL_DOCUMENTS__, hash)
    ? __DROP_LEGAL_DOCUMENTS__[hash]
    : undefined;
}

export function LegalDocument({ html }: { html: string }) {
  const waitlist = window.location.hash.startsWith("#/legal/2026-11-26-waitlist/");
  const base = waitlist ? "#/legal/2026-11-26-waitlist" : "#/legal/2026-11-26";
  return (
    <div className="beta-legal-page">
      <header>
        <a href="#/">
          <Brand />
        </a>
      </header>
      <main dangerouslySetInnerHTML={{ __html: html }} />
      <footer>
        <a href={base + "/terms"}>{waitlist ? "Waitlist Terms" : "Terms of Service"}</a>
        <a href={base + "/privacy"}>{waitlist ? "Waitlist Privacy Notice" : "Privacy Policy"}</a>
      </footer>
    </div>
  );
}
