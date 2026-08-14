// UF.5: a dependency-free markdown renderer for the "rendered" half of the
// Context preview drawer. The server's bundle markdown (contextbundle.go) is
// a pinned, narrow shape — headings, fenced code, bullets, blockquote lines,
// plain paragraphs — so a full CommonMark parser would be pure overhead;
// this only needs to recognize that shape.
export type MdBlock =
  | { type: "heading"; level: 1 | 2 | 3; text: string }
  | { type: "code"; lang: string; text: string }
  | { type: "list"; items: string[] }
  | { type: "quote"; text: string }
  | { type: "paragraph"; text: string };

export function parseMarkdownLite(md: string): MdBlock[] {
  const lines = md.split("\n");
  const blocks: MdBlock[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];

    if (line.startsWith("```")) {
      const lang = line.slice(3).trim();
      const codeLines: string[] = [];
      i++;
      while (i < lines.length && !lines[i].startsWith("```")) {
        codeLines.push(lines[i]);
        i++;
      }
      i++; // skip closing fence
      blocks.push({ type: "code", lang, text: codeLines.join("\n") });
      continue;
    }

    const heading = /^(#{1,3})\s+(.*)$/.exec(line);
    if (heading) {
      blocks.push({ type: "heading", level: heading[1].length as 1 | 2 | 3, text: heading[2] });
      i++;
      continue;
    }

    if (line.startsWith("> ")) {
      blocks.push({ type: "quote", text: line.slice(2) });
      i++;
      continue;
    }

    if (line.startsWith("- ")) {
      const items: string[] = [];
      while (i < lines.length && lines[i].startsWith("- ")) {
        items.push(lines[i].slice(2));
        i++;
      }
      blocks.push({ type: "list", items });
      continue;
    }

    if (line.trim() === "") {
      i++;
      continue;
    }

    const paraLines: string[] = [line];
    i++;
    while (i < lines.length && lines[i].trim() !== "" && !/^(#{1,3})\s/.test(lines[i]) && !lines[i].startsWith("```") && !lines[i].startsWith("- ") && !lines[i].startsWith("> ")) {
      paraLines.push(lines[i]);
      i++;
    }
    blocks.push({ type: "paragraph", text: paraLines.join("\n") });
  }
  return blocks;
}
