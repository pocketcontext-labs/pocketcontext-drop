import Brand from "./Brand";

declare const __DROP_LEGAL_DOCUMENTS__: Record<string, string>;

export function publishedLegalDocument(hash: string): string | undefined {
  return Object.hasOwn(__DROP_LEGAL_DOCUMENTS__, hash)
    ? __DROP_LEGAL_DOCUMENTS__[hash]
    : undefined;
}

export function LegalDocument({ html }: { html: string }) {
  return (
    <div className="beta-legal-page">
      <header>
        <a href="#/">
          <Brand />
        </a>
      </header>
      <main dangerouslySetInnerHTML={{ __html: html }} />
      <footer>
        <a href="#/legal/2026-11-26/terms">Terms of Service</a>
        <a href="#/legal/2026-11-26/privacy">Privacy Policy</a>
      </footer>
    </div>
  );
}
