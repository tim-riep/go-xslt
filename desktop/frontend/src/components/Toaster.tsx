import { useAppDispatch, useAppState } from "../state/store";

/** Mounted once at the app root. Toasts auto-dismiss (scheduled by useToast
 * when pushed) but can also be dismissed by hand. */
export function Toaster() {
  const { ui } = useAppState();
  const dispatch = useAppDispatch();
  if (ui.toasts.length === 0) return null;

  return (
    <div className="toaster">
      {ui.toasts.map((t) => (
        <div key={t.id} className={`toast ${t.kind}`}>
          <span>{t.message}</span>
          <span className="toast-dismiss" onClick={() => dispatch({ type: "TOAST_DISMISS", id: t.id })}>
            ×
          </span>
        </div>
      ))}
    </div>
  );
}
