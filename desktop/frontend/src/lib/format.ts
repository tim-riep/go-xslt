// formatXml pretty-prints an XML/HTML string for display. It collapses
// whitespace that sits purely between tags, then re-indents one level per
// nesting depth. Text content inside an element (>text<) is left untouched.
export function formatXml(xml: string): string {
  if (!xml) return "";
  const PAD = "  ";

  // Pull off a leading XML declaration / PI so it stays on its own line.
  let head = "";
  let body = xml;
  const decl = body.match(/^\s*<\?[^>]*\?>\s*/);
  if (decl) {
    head = decl[0].trim() + "\n";
    body = body.slice(decl[0].length);
  }

  // Collapse whitespace-only gaps between tags, then break adjacent tags apart.
  body = body.replace(/>\s+</g, "><").replace(/></g, ">\n<").trim();

  let depth = 0;
  const out: string[] = [];
  for (const raw of body.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const isClose = /^<\/[^>]+>/.test(line);
    const isSelf = /\/>\s*$/.test(line) || /^<\?/.test(line) || /^<!--/.test(line) || /^<!/.test(line);
    // A line that opens and closes the same element (<a>text</a>) keeps depth.
    const isOpenClose = /^<[^!?][^>]*>.*<\/[^>]+>\s*$/.test(line);

    if (isClose) depth = Math.max(0, depth - 1);
    out.push(PAD.repeat(depth) + line);
    if (!isClose && !isSelf && !isOpenClose && /^<[^!?]/.test(line)) depth += 1;
  }
  return head + out.join("\n");
}
