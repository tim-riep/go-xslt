import { useEffect, type ReactNode } from "react";

/** Generic modal shell: backdrop + centered card, Escape and backdrop-click
 * both call onDismiss. Content (prompt or confirm body) is supplied by the
 * caller — see DialogHost, the only mount point that actually renders one. */
export function Modal({
  children,
  onDismiss,
  className,
}: {
  children: ReactNode;
  onDismiss: () => void;
  /** Extra class(es) merged onto the same element as "modal", e.g. so a
   * `.modal.licenses-modal` CSS rule can actually match — it never does if
   * the extra class instead lands on a child div. */
  className?: string;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onDismiss();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onDismiss]);

  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onDismiss()}>
      <div className={className ? `modal ${className}` : "modal"} role="dialog" aria-modal="true">
        {children}
      </div>
    </div>
  );
}
