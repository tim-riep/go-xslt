import { useEffect, useRef, useState } from "react";
import { useAppState } from "../state/store";
import type { DialogState } from "../state/types";
import { Modal } from "./Modal";

type PromptDialogState = Extract<DialogState, { kind: "prompt" }>;
type ConfirmDialogState = Extract<DialogState, { kind: "confirm" }>;

/** Mounted once at the app root. Renders whatever `ui.dialog` currently
 * holds — a prompt (the window.prompt() replacement; that API is a no-op in
 * the Wails WKWebView) or a confirm (ditto window.confirm()). */
export function DialogHost() {
  const { ui } = useAppState();
  const dialog = ui.dialog;
  if (!dialog) return null;

  if (dialog.kind === "prompt") {
    return (
      <Modal onDismiss={() => dialog.onCancel?.()}>
        <PromptBody dialog={dialog} />
      </Modal>
    );
  }
  return (
    <Modal onDismiss={() => dialog.onCancel?.()}>
      <ConfirmBody dialog={dialog} />
    </Modal>
  );
}

function PromptBody({ dialog }: { dialog: PromptDialogState }) {
  const [value, setValue] = useState(dialog.initialValue ?? "");
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.select();
  }, []);

  const submit = () => {
    const problem = dialog.validate?.(value) ?? null;
    if (problem) {
      setError(problem);
      return;
    }
    dialog.onConfirm(value);
  };

  return (
    <>
      <div className="modal-title">{dialog.title}</div>
      <div className="modal-field">
        {dialog.label && <label>{dialog.label}</label>}
        <input
          ref={inputRef}
          autoFocus
          value={value}
          placeholder={dialog.placeholder}
          onChange={(e) => {
            setValue(e.target.value);
            setError(null);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              submit();
            }
          }}
        />
        <div className="modal-error">{error ?? ""}</div>
      </div>
      <div className="modal-actions">
        <button onClick={() => dialog.onCancel?.()}>Cancel</button>
        <button className="primary" onClick={submit} disabled={value.trim() === ""}>
          {dialog.confirmLabel ?? "OK"}
        </button>
      </div>
    </>
  );
}

function ConfirmBody({ dialog }: { dialog: ConfirmDialogState }) {
  return (
    <>
      <div className="modal-title">{dialog.title}</div>
      <div className="modal-message">{dialog.message}</div>
      <div className="modal-actions">
        <button onClick={() => dialog.onCancel?.()}>Cancel</button>
        <button className={dialog.danger ? "danger" : "primary"} autoFocus onClick={dialog.onConfirm}>
          {dialog.confirmLabel ?? "OK"}
        </button>
      </div>
    </>
  );
}
