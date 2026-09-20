// Shared single-letter/glyph badges for file kinds and run kinds, used
// anywhere a compact visual tag stands in for a real icon (tree rows, run
// list rows, tab strip items) — kept in one place so the three stay in sync.
export const FILE_KIND_LABEL: Record<string, string> = {
  stylesheet: "X",
  schema: "S",
  xml: "M",
  text: "T",
  other: "•",
};

export const RUN_KIND_BADGE: Record<string, string> = {
  transform: "▶",
  validate: "✓",
  xpath: "ƒ",
};
