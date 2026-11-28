import { useState } from "react";
import type { DropNode } from "./dropApi";
import "./memory-history.css";

const hour = 3600;
const stamp = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit",
  second: "2-digit",
});
const clock = new Intl.DateTimeFormat(undefined, {
  hour: "numeric",
  minute: "2-digit",
});
type Sample = NonNullable<DropNode["memory_history"]>[number];
const vmCount = (n: number) => `${n} ${n === 1 ? "VM" : "VMs"} at peak`;
const detail = (s: Sample) =>
  `${stamp.format(s.at * 1000)} · ${s.used_mib.toLocaleString()} MiB · ${vmCount(s.running_vms)}`;

function axisMax(peak: number) {
  const step = 10 ** Math.floor(Math.log10(Math.max(1, peak)));
  return Math.ceil(Math.max(1, peak) / step) * step;
}

export default function MemoryHistory({ node }: { node: DropNode }) {
  const [selected, setSelected] = useState<number | null>(null);
  const samples = node.memory_history ?? [];
  if (!samples.length)
    return (
      <small className="quiet node-memory-history">
        No memory samples in the last 24 hours.
      </small>
    );

  const end = node.memory_history_end ?? Math.floor(Date.now() / 1000);
  const slots: (Sample | undefined)[] = Array.from({ length: 24 });
  for (const sample of samples) {
    const index = 23 - Math.floor((end - sample.at) / hour);
    if (index < 0 || index > 23) continue;
    const old = slots[index];
    if (
      !old ||
      sample.used_mib > old.used_mib ||
      (sample.used_mib === old.used_mib && sample.at > old.at)
    )
      slots[index] = sample;
  }
  const peak = slots.reduce<Sample | undefined>(
    (best, s) => (s && (!best || s.used_mib >= best.used_mib) ? s : best),
    undefined,
  );
  if (!peak)
    return (
      <small className="quiet node-memory-history">
        No memory samples in the last 24 hours.
      </small>
    );
  const max = axisMax(peak.used_mib);
  const start = end - 24 * hour;
  const label = (i: number) =>
    slots[i]
      ? detail(slots[i]!)
      : `${stamp.format((start + i * hour) * 1000)} – ${clock.format((start + (i + 1) * hour) * 1000)} · No samples`;
  return (
    <details className="node-memory-history">
      <summary>
        24-hour memory peak · {peak.used_mib.toLocaleString()} MiB ·{" "}
        {vmCount(peak.running_vms)}
      </summary>
      <figure
        className="memory-figure"
        aria-label="Hourly peaks of combined guest memory"
      >
        <div className="memory-axis">
          <span>{max.toLocaleString()} MiB</span>
          <span>0</span>
        </div>
        <div className="memory-plot">
          {slots.map((s, i) => (
            <button
              type="button"
              className={s ? "memory-hour" : "memory-hour missing"}
              key={i}
              aria-label={label(i)}
              onFocus={() => setSelected(i)}
              onPointerEnter={() => setSelected(i)}
              onClick={() => setSelected(i)}
            >
              {s && (
                <span
                  className={s.used_mib ? "memory-bar" : "memory-zero"}
                  style={
                    s.used_mib
                      ? { height: `${(100 * s.used_mib) / max}%` }
                      : undefined
                  }
                >
                  {s === peak && (
                    <b
                      className={
                        "memory-mark" +
                        (i < 3 ? " left" : i > 20 ? " right" : "")
                      }
                    >
                      {peak.used_mib.toLocaleString()} MiB
                    </b>
                  )}
                </span>
              )}
            </button>
          ))}
        </div>
        <div className="memory-times">
          {[0, 12, 24].map((i) => (
            <span key={i}>{clock.format((start + i * hour) * 1000)}</span>
          ))}
        </div>
        <figcaption>
          <span className="memory-key">No samples</span>
          <output aria-live="polite">
            {selected === null ? detail(peak) : label(selected)}
          </output>
        </figcaption>
      </figure>
      <details>
        <summary>View data</summary>
        <table>
          <caption>
            Hourly peaks of combined guest memory. Hours without samples are
            omitted.
          </caption>
          <thead>
            <tr>
              <th scope="col">Time of peak</th>
              <th scope="col">Memory used</th>
              <th scope="col">VMs at peak</th>
            </tr>
          </thead>
          <tbody>
            {slots.map(
              (s) =>
                s && (
                  <tr key={s.at}>
                    <td>
                      <time dateTime={new Date(s.at * 1000).toISOString()}>
                        {stamp.format(s.at * 1000)}
                      </time>
                    </td>
                    <td>{s.used_mib.toLocaleString()} MiB</td>
                    <td>{s.running_vms}</td>
                  </tr>
                ),
            )}
          </tbody>
        </table>
      </details>
    </details>
  );
}
