// Imperative hooks over the dialog/menu/toast slice of AppState. window.prompt()
// and window.confirm() are no-ops in the Wails WKWebView (they return null
// immediately without showing anything) — every rename/delete/new-file flow
// must go through these instead.
import { useCallback } from "react";
import { useAppDispatch } from "./store";
import type { ContextMenuItem } from "./types";

let toastSeq = 0;

export function usePrompt() {
  const dispatch = useAppDispatch();
  return useCallback(
    (opts: {
      title: string;
      label?: string;
      initialValue?: string;
      confirmLabel?: string;
      placeholder?: string;
      validate?: (value: string) => string | null;
    }): Promise<string | null> => {
      return new Promise((resolve) => {
        dispatch({
          type: "DIALOG_OPEN",
          dialog: {
            kind: "prompt",
            title: opts.title,
            label: opts.label,
            initialValue: opts.initialValue,
            confirmLabel: opts.confirmLabel,
            placeholder: opts.placeholder,
            validate: opts.validate,
            onConfirm: (value) => {
              dispatch({ type: "DIALOG_CLOSE" });
              resolve(value);
            },
            onCancel: () => {
              dispatch({ type: "DIALOG_CLOSE" });
              resolve(null);
            },
          },
        });
      });
    },
    [dispatch],
  );
}

export function useConfirm() {
  const dispatch = useAppDispatch();
  return useCallback(
    (opts: { title: string; message: string; confirmLabel?: string; danger?: boolean }): Promise<boolean> => {
      return new Promise((resolve) => {
        dispatch({
          type: "DIALOG_OPEN",
          dialog: {
            kind: "confirm",
            title: opts.title,
            message: opts.message,
            confirmLabel: opts.confirmLabel,
            danger: opts.danger,
            onConfirm: () => {
              dispatch({ type: "DIALOG_CLOSE" });
              resolve(true);
            },
            onCancel: () => {
              dispatch({ type: "DIALOG_CLOSE" });
              resolve(false);
            },
          },
        });
      });
    },
    [dispatch],
  );
}

export function useContextMenu() {
  const dispatch = useAppDispatch();
  return useCallback(
    (e: { clientX: number; clientY: number; preventDefault: () => void }, items: ContextMenuItem[]) => {
      e.preventDefault();
      dispatch({ type: "MENU_OPEN", menu: { x: e.clientX, y: e.clientY, items } });
    },
    [dispatch],
  );
}

export function useToast() {
  const dispatch = useAppDispatch();
  return useCallback(
    (kind: "info" | "error" | "success", message: string) => {
      const id = `t${++toastSeq}`;
      dispatch({ type: "TOAST_PUSH", toast: { id, kind, message } });
      window.setTimeout(() => dispatch({ type: "TOAST_DISMISS", id }), 4000);
    },
    [dispatch],
  );
}
