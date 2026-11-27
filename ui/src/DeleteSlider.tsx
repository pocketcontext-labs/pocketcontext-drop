import { useRef, useState, type PointerEvent } from "react";

/** A track click cannot confirm a deletion. The pointer starts on the handle
 * and must travel the full width before it is released. */
export default function DeleteSlider({
  disabled,
  onConfirm,
}: {
  disabled: boolean;
  onConfirm: () => void;
}) {
  const [position, setPosition] = useState(0);
  const start = useRef<{ id: number; x: number } | null>(null);
  const handle = useRef<HTMLSpanElement>(null);
  function fraction(e: PointerEvent<HTMLDivElement>) {
    if (!start.current || start.current.id !== e.pointerId) return 0;
    const travel =
      e.currentTarget.clientWidth - (handle.current?.offsetWidth ?? 0);
    return Math.max(
      0,
      Math.min(
        100,
        ((e.clientX - start.current.x) * 100) / Math.max(1, travel),
      ),
    );
  }
  function reset() {
    start.current = null;
    setPosition(0);
  }
  return (
    <>
      <div
        className="delete-slider"
        role="slider"
        aria-label="Slide to delete workspace"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(position)}
        aria-disabled={disabled}
        tabIndex={disabled ? -1 : 0}
        onPointerDown={(e) => {
          if (
            disabled ||
            e.button !== 0 ||
            e.target !== handle.current ||
            start.current
          )
            return;
          start.current = { id: e.pointerId, x: e.clientX };
          setPosition(0);
          e.currentTarget.setPointerCapture(e.pointerId);
        }}
        onPointerMove={(e) => {
          if (start.current?.id === e.pointerId) setPosition(fraction(e));
        }}
        onPointerUp={(e) => {
          if (start.current?.id !== e.pointerId) return;
          const complete = fraction(e) === 100;
          reset();
          if (complete && !disabled) onConfirm();
        }}
        onPointerCancel={reset}
        onLostPointerCapture={reset}
        onBlur={reset}
        onKeyDown={(e) => {
          if (disabled || start.current) return;
          if (e.key === "ArrowRight") setPosition((v) => Math.min(100, v + 10));
          else if (e.key === "ArrowLeft")
            setPosition((v) => Math.max(0, v - 10));
          else if (e.key === "Escape" || e.key === "Home") reset();
          else if (e.key === "Enter" && position === 100) {
            reset();
            onConfirm();
          } else return;
          e.preventDefault();
        }}
      >
        Slide to delete
        <span
          aria-hidden="true"
          ref={handle}
          style={{ left: `calc(${position}% - ${position * 0.48}px)` }}
        >
          →
        </span>
      </div>
      <p className="quiet">
        Drag all the way right and release. With a keyboard, use Right Arrow to
        reach the end, then Enter.
      </p>
    </>
  );
}
