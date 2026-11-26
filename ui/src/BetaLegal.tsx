import type { PublishedLegal } from "./api";

export function BetaLegalLinks({ documents }: { documents?: PublishedLegal }) {
  return documents ? (
    <footer className="beta-legal">
      <a href={documents.termsURL} target="_blank" rel="noreferrer">
        Terms of Service
      </a>
      <a href={documents.privacyURL} target="_blank" rel="noreferrer">
        Privacy Policy
      </a>
    </footer>
  ) : null;
}
