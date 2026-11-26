// Set both URLs when the owner has verified and published the documents.
function publicLink(value?: string): string | undefined {
  try {
    const url = new URL(value ?? "");
    if (url.protocol === "https:" && !url.username && !url.password)
      return url.href;
  } catch {
    /* Unconfigured. */
  }
}

export const betaTerms = publicLink(import.meta.env.VITE_DROP_TERMS_URL);
export const betaPrivacy = publicLink(import.meta.env.VITE_DROP_PRIVACY_URL);

export const betaTermsVersion =
  typeof import.meta.env.VITE_DROP_TERMS_VERSION === "string"
    ? import.meta.env.VITE_DROP_TERMS_VERSION.trim()
    : "";
export const betaEnrollmentOpen = !!(
  betaTerms &&
  betaPrivacy &&
  betaTermsVersion
);

export function BetaLegalLinks() {
  return betaTerms && betaPrivacy ? (
    <footer className="beta-legal">
      <a href={betaTerms} target="_blank" rel="noreferrer">
        Terms of Service
      </a>
      <a href={betaPrivacy} target="_blank" rel="noreferrer">
        Privacy Policy
      </a>
    </footer>
  ) : null;
}
