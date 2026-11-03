import { useEffect, useState } from "react";
import { Browser } from "@wailsio/runtime";
import { listLicenses, type License } from "../lib/api";
import { Modal } from "./Modal";
import { useT } from "../i18n";
import { viewsMessages } from "../i18n/messages/views";

/** Toolbar entry point + the modal it opens: this project's own license and
 * every bundled third-party dependency's, fetched from the Go side (see
 * desktop/licenses/licenses.go) so the list can never drift from what the
 * binary actually embeds. */
export function LicensesButton() {
  const [open, setOpen] = useState(false);
  const t = useT(viewsMessages);
  return (
    <>
      <button className="licenses-button" onClick={() => setOpen(true)}>
        {t("licenses")}
      </button>
      {open && <LicensesModal onDismiss={() => setOpen(false)} />}
    </>
  );
}

const KIND_LABEL: Record<string, "kindProject" | "kindGo" | "kindNpm" | "kindFont" | "kindData"> = {
  project: "kindProject",
  go: "kindGo",
  npm: "kindNpm",
  font: "kindFont",
  data: "kindData",
};

function LicensesModal({ onDismiss }: { onDismiss: () => void }) {
  const [entries, setEntries] = useState<License[] | null>(null);
  const [selected, setSelected] = useState(0);
  const t = useT(viewsMessages);

  useEffect(() => {
    void (async () => {
      const list = await listLicenses();
      setEntries(list ?? []);
    })();
  }, []);

  const current = entries?.[selected];

  return (
    <Modal onDismiss={onDismiss} className="licenses-modal">
      <div className="licenses-modal-header">
        <span className="modal-title">{t("licenses")}</span>
        <button className="licenses-close" onClick={onDismiss} aria-label={t("close")}>
          ✕
        </button>
      </div>
      {!entries && <div className="licenses-loading">{t("loading")}</div>}
      {entries && (
        <div className="licenses-body">
          <div className="licenses-list">
            {entries.map((e, i) => (
              <button
                key={`${e.Kind}-${e.Name}`}
                className={`licenses-list-item${i === selected ? " active" : ""}`}
                onClick={() => setSelected(i)}
              >
                <span className="licenses-list-name">{e.Name}</span>
                <span className="licenses-list-meta">
                  {KIND_LABEL[e.Kind] ? t(KIND_LABEL[e.Kind]) : e.Kind} · {e.License}
                  {e.Version ? ` · ${e.Version}` : ""}
                </span>
              </button>
            ))}
          </div>
          <div className="licenses-detail">
            {current && (
              <>
                <div className="licenses-detail-header">
                  <span className="licenses-detail-name">{current.Name}</span>
                  {current.URL && (
                    <button className="licenses-detail-url" onClick={() => void Browser.OpenURL(current.URL)}>
                      {current.URL}
                    </button>
                  )}
                </div>
                <pre className="licenses-detail-text">{current.Text}</pre>
              </>
            )}
          </div>
        </div>
      )}
    </Modal>
  );
}
