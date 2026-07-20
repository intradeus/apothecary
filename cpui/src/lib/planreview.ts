// Plan review support (DESIGN3 §3): markdown split into line-tracked
// blocks for anchoring, and the deterministic composition of accumulated
// comments into ONE feedback prompt for the existing on_reject feed path —
// the engine learns nothing new.
import { marked, type Token } from 'marked';

export interface PlanBlock {
  html: string;
  raw: string;
  heading: string; // nearest heading above (or at) this block
  startLine: number;
  endLine: number;
}

export interface ReviewComment {
  quote: string;
  heading: string;
  startLine: number;
  endLine: number;
  text: string;
}

// Agent-generated markdown is semi-trusted: escape raw HTML instead of
// injecting it (marked passes HTML through by default).
function escapeHTML(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
const renderer = {
  html({ text }: { text: string }) {
    return `<pre class="small mono">${escapeHTML(text)}</pre>`;
  }
};
marked.use({ renderer });

// mdToHtml renders markdown for ping questions/context/answers (same
// escaped-HTML posture as plans — agent output is semi-trusted).
export function mdToHtml(src: string): string {
  return marked.parse(src, { async: false }) as string;
}

// planBlocks lexes the markdown into top-level blocks, each carrying its
// source line range and nearest heading (marked tokens keep `raw`, so line
// numbers are cumulative newline counts).
export function planBlocks(src: string): PlanBlock[] {
  const tokens = marked.lexer(src);
  const out: PlanBlock[] = [];
  let line = 1;
  let heading = '';
  for (const tok of tokens) {
    const raw = (tok as Token & { raw: string }).raw ?? '';
    const lines = (raw.match(/\n/g) ?? []).length;
    if (tok.type === 'heading') heading = (tok as Token & { text: string }).text;
    if (tok.type !== 'space') {
      out.push({
        html: marked.parser([tok] as Token[], { async: false }) as string,
        raw,
        heading,
        startLine: line,
        endLine: Math.max(line, line + lines - 1)
      });
    }
    line += lines;
  }
  return out;
}

// composeFeedback renders the single structured feedback string (§3.2).
export function composeFeedback(comments: ReviewComment[]): string {
  const parts = [`PLAN REVIEW — ${comments.length} comment${comments.length === 1 ? '' : 's'}:`, ''];
  comments.forEach((c, i) => {
    const where = c.heading ? `§ ${c.heading}, lines ${c.startLine}–${c.endLine}` : `lines ${c.startLine}–${c.endLine}`;
    const quote = c.quote.length > 200 ? c.quote.slice(0, 200) + '…' : c.quote;
    parts.push(`${i + 1}. [${where}]`);
    for (const ql of quote.trim().split('\n')) parts.push(`   > ${ql}`);
    parts.push(`   ${c.text.trim().split('\n').join('\n   ')}`);
    parts.push('');
  });
  return parts.join('\n').trimEnd() + '\n';
}

const draftKey = (pingId: string) => `plan-review-${pingId}`;

export function loadDraft(pingId: string): ReviewComment[] {
  try {
    const raw = localStorage.getItem(draftKey(pingId));
    return raw ? (JSON.parse(raw) as ReviewComment[]) : [];
  } catch {
    return [];
  }
}

export function saveDraft(pingId: string, comments: ReviewComment[]) {
  try {
    if (comments.length === 0) localStorage.removeItem(draftKey(pingId));
    else localStorage.setItem(draftKey(pingId), JSON.stringify(comments));
  } catch {
    /* private mode etc. — drafts are best-effort */
  }
}

export function clearDraft(pingId: string) {
  try {
    localStorage.removeItem(draftKey(pingId));
  } catch {
    /* ignore */
  }
}
