import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, validSearch, type SearchHit } from "./api";
import { message } from "./errors";
import { formatDate } from "./Apps";

interface Results {
  term: string;
  generation: string;
  hits: SearchHit[];
  hasMore: boolean;
}

/** Full-text search over the project's apps. */
export default function Search() {
  const [term, setTerm] = useState("");
  const [results, setResults] = useState<Results | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const searches = useRef(0);
  const mounted = useRef(true);
  useEffect(() => {
    // Strict mode and Fast Refresh run effects again on the same instance.
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const run = async (next: string, previous?: Results) => {
    const search = ++searches.current;
    setBusy(true);
    setError("");
    try {
      const page = await api.searchApps(
        next,
        previous ? previous.hits.length : 0,
        20,
        previous?.generation,
      );
      if (!mounted.current || search !== searches.current) return;
      setResults({
        term: next,
        generation: page.generation,
        hits: previous ? [...previous.hits, ...page.hits] : page.hits,
        hasMore: page.hasMore,
      });
    } catch (e) {
      if (!mounted.current || search !== searches.current) return;
      setError(message(e));
      if (!previous) setResults(null);
    } finally {
      if (mounted.current && search === searches.current) setBusy(false);
    }
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    try {
      void run(validSearch(term));
    } catch (e) {
      setError(message(e));
    }
  };
  return (
    <section className="panel" aria-labelledby="search-title">
      <h2 id="search-title">Search</h2>
      <form role="search" className="row" onSubmit={submit}>
        <label className="visually-hidden" htmlFor="search-term">
          Search apps
        </label>
        <input
          id="search-term"
          type="search"
          value={term}
          maxLength={500}
          onChange={(event) => setTerm(event.target.value)}
          placeholder="Search apps"
        />
        <button className="secondary" disabled={busy || !term.trim()}>
          Search
        </button>
        {results && (
          <button
            type="button"
            className="link"
            onClick={() => {
              searches.current++;
              setBusy(false);
              setTerm("");
              setResults(null);
              setError("");
            }}
          >
            Clear
          </button>
        )}
      </form>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <div aria-live="polite">
        {results &&
          (results.hits.length === 0 ? (
            <p className="empty">No apps match “{results.term}”.</p>
          ) : (
            <>
              <p className="quiet">
                {results.hits.length}
                {results.hasMore ? "+" : ""}{" "}
                {results.hits.length === 1 ? "match" : "matches"} for “
                {results.term}”
              </p>
              <ol className="apps" aria-label="Search results">
                {results.hits.map((hit) => (
                  <li key={hit.id}>
                    <p>
                      <span>{hit.name}</span> <code className="slug">{hit.slug}</code>
                    </p>
                    {hit.excerpt && hit.excerpt !== hit.name && (
                      <p className="quiet excerpt">{hit.excerpt}</p>
                    )}
                    <time dateTime={hit.created.replace(" ", "T")}>
                      {formatDate(hit.created)}
                    </time>
                  </li>
                ))}
              </ol>
            </>
          ))}
      </div>
      {results?.hasMore && (
        <button
          type="button"
          className="secondary"
          disabled={busy}
          onClick={() => void run(results.term, results)}
        >
          More results
        </button>
      )}
    </section>
  );
}
