<script lang="ts">
  import { api, type Ping, type PingFile } from '$lib/api';
  import AttachFiles from '$lib/AttachFiles.svelte';
  import {
    byPosition,
    clearDraft,
    composeFeedback,
    loadDraft,
    planBlocks,
    rangeAt,
    saveDraft,
    textOffset,
    type CommentAnchor,
    type PlanBlock,
    type ReviewComment
  } from '$lib/planreview';
  import { isGatePing } from '$lib/ping';
  import { onMount } from 'svelte';

  let { ping, onanswered }: { ping: Ping; onanswered: () => void } = $props();

  // Gates answer approve/reject; ask_human artifact reviews (PLAN4 M16)
  // answer with the composed review text — or a declared option with the
  // review as its note.
  const isGate = $derived(isGatePing(ping.type));

  let blocks = $state<PlanBlock[]>([]);
  let path = $state('');
  let error = $state('');
  let loading = $state(true);
  let comments = $state<ReviewComment[]>([]);
  let busy = $state(false);
  let files = $state<File[]>([]); // the human's answer attachments
  let extraFiles = $state<PingFile[]>([]); // agent-attached extras, beyond the primary doc

  // The composer pops up where a selection was released. It is deliberately
  // sticky: only Add/Save or Close dismiss it — clicking away (to scroll, to
  // re-read the plan, to pick a file) must never throw away what was typed.
  interface Composer {
    anchor: CommentAnchor | null;
    quote: string;
    heading: string;
    startLine: number;
    endLine: number;
    text: string;
    files: File[];
    editing: number | null; // index of the comment being edited, else null
    x: number;
    y: number;
  }
  let composer = $state<Composer | null>(null);
  let composerEl = $state<HTMLTextAreaElement | null>(null);
  let hovered = $state<number | null>(null);
  let planEl = $state<HTMLDivElement | null>(null);
  const blockEls: HTMLElement[] = [];

  onMount(async () => {
    comments = loadDraft(ping.ping_id);
    // ONE fetch: the ping's files, primary first, each with its raise-time
    // snapshot. The reviewed document's content comes from the snapshot —
    // what you approve is what was captured, not what the workspace holds
    // by the time you look (DESIGN-FILES §9).
    try {
      const { files: fs } = await api.pingFiles(ping.ping_id);
      const primary = fs[0];
      if (!primary) {
        error = 'this ping has no attached files';
      } else if (!primary.captured || !primary.snapshot_id) {
        error = `${primary.path} was not captured when the ping was raised`;
      } else {
        path = primary.path;
        const res = await fetch(api.snapshotUrl(primary.snapshot_id));
        if (!res.ok) throw new Error(`document unavailable (${res.status})`);
        blocks = planBlocks(await res.text());
      }
      extraFiles = fs.slice(1);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  });

  // ---- selection → anchor -------------------------------------------------

  function blockIndexOf(node: Node): number {
    const el = node.nodeType === Node.ELEMENT_NODE ? (node as Element) : node.parentElement;
    const b = el?.closest<HTMLElement>('.block');
    return b?.dataset.idx === undefined ? -1 : Number(b.dataset.idx);
  }

  function anchorFrom(range: Range) {
    const sb = blockIndexOf(range.startContainer);
    let eb = blockIndexOf(range.endContainer);
    if (sb < 0 || eb < sb || !blockEls[sb] || !blockEls[eb]) return null;
    const startOffset = textOffset(blockEls[sb], range.startContainer, range.startOffset);
    let endOffset = textOffset(blockEls[eb], range.endContainer, range.endOffset);
    // A drag released just past a block lands on the head of the next one:
    // credit the text actually covered, not the untouched block below it.
    while (eb > sb && endOffset === 0) {
      eb -= 1;
      endOffset = blockEls[eb].textContent?.length ?? 0;
    }
    return {
      anchor: { startBlock: sb, startOffset, endBlock: eb, endOffset },
      heading: blocks[sb].heading,
      startLine: blocks[sb].startLine,
      endLine: blocks[eb].endLine
    };
  }

  const POPUP_W = 340;
  function place(rect: { left: number; bottom: number }): { x: number; y: number } {
    return {
      x: Math.max(8, Math.min(rect.left, window.innerWidth - POPUP_W - 8)),
      y: Math.max(8, Math.min(rect.bottom + 8, window.innerHeight - 180))
    };
  }

  function captureSelection() {
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0 || !planEl) return;
    const range = sel.getRangeAt(0);
    if (!planEl.contains(range.commonAncestorContainer)) return;
    const quote = sel.toString().trim();
    if (quote.length < 2) return;
    // Never clobber a composer that already holds work.
    if (composer && (composer.text.trim() || composer.files.length || composer.editing !== null))
      return;
    const a = anchorFrom(range);
    if (!a) return;
    composer = {
      ...a,
      quote,
      text: '',
      files: [],
      editing: null,
      ...place(range.getBoundingClientRect())
    };
  }

  // Listeners live on the document (a drag can be released outside the plan)
  // and are attached imperatively so the plan stays a plain, non-interactive
  // region as far as a11y is concerned.
  $effect(() => {
    const onUp = () => setTimeout(captureSelection, 0); // let the selection settle
    const onKeyUp = (e: KeyboardEvent) => {
      if (e.shiftKey || e.key === 'Shift') captureSelection();
    };
    document.addEventListener('mouseup', onUp);
    document.addEventListener('touchend', onUp);
    document.addEventListener('keyup', onKeyUp);
    return () => {
      document.removeEventListener('mouseup', onUp);
      document.removeEventListener('touchend', onUp);
      document.removeEventListener('keyup', onKeyUp);
    };
  });

  $effect(() => {
    if (composer) composerEl?.focus();
  });

  // ---- highlighting -------------------------------------------------------

  const PENDING = 'apo-plan-pending';
  const ACTIVE = 'apo-plan-active';
  type HighlightRegistry = { set(k: string, v: object): void; delete(k: string): void };
  type HighlightCtor = new (...ranges: Range[]) => object;

  function registry(): HighlightRegistry | null {
    const g = globalThis as unknown as {
      CSS?: { highlights?: HighlightRegistry };
      Highlight?: HighlightCtor;
    };
    return g.CSS?.highlights && typeof g.Highlight === 'function' ? g.CSS.highlights : null;
  }

  // One Range per spanned block: the anchor's offsets are per-block, and a
  // Highlight happily holds several.
  function rangesFor(a: CommentAnchor): Range[] {
    const out: Range[] = [];
    for (let i = a.startBlock; i <= a.endBlock; i++) {
      const el = blockEls[i];
      if (!el) continue;
      const len = el.textContent?.length ?? 0;
      const from = i === a.startBlock ? a.startOffset : 0;
      const to = i === a.endBlock ? a.endOffset : len;
      const r = rangeAt(el, Math.min(from, len), Math.min(to, len));
      if (r) out.push(r);
    }
    return out;
  }

  function paint(name: string, anchor: CommentAnchor | null | undefined) {
    const reg = registry();
    if (!reg) return; // no Custom Highlight API — the block tint carries it
    reg.delete(name);
    const rs = anchor ? rangesFor(anchor) : [];
    if (!rs.length) return;
    const Ctor = (globalThis as unknown as { Highlight: HighlightCtor }).Highlight;
    reg.set(name, new Ctor(...rs));
  }

  $effect(() => {
    void composer; // repaint on open/close/edit, not on every keystroke
    void hovered;
    void blocks.length;
    void comments.length;
    paint(PENDING, composer?.anchor);
    paint(ACTIVE, hovered === null ? null : comments[hovered]?.anchor);
    return () => {
      const reg = registry();
      reg?.delete(PENDING);
      reg?.delete(ACTIVE);
    };
  });

  // Block-level span of a comment: the anchor when we have one, else the line
  // overlap (drafts saved before comments were anchored to exact text).
  function spans(c: ReviewComment | undefined, i: number): boolean {
    if (!c) return false;
    if (c.anchor) return i >= c.anchor.startBlock && i <= c.anchor.endBlock;
    return blocks[i].endLine >= c.startLine && blocks[i].startLine <= c.endLine;
  }
  const blockOf = (c: ReviewComment) =>
    c.anchor?.startBlock ?? blocks.findIndex((b) => b.endLine >= c.startLine);

  // ---- comments -----------------------------------------------------------

  function commitComment() {
    if (!composer || !composer.text.trim()) return;
    const c: ReviewComment = {
      quote: composer.quote,
      heading: composer.heading,
      startLine: composer.startLine,
      endLine: composer.endLine,
      text: composer.text.trim(),
      ...(composer.anchor ? { anchor: composer.anchor } : {}),
      ...(composer.files.length ? { files: composer.files } : {})
    };
    if (composer.editing !== null) comments[composer.editing] = c;
    else comments.push(c);
    comments.sort(byPosition);
    saveDraft(ping.ping_id, comments);
    closeComposer();
  }

  function closeComposer() {
    composer = null;
    window.getSelection()?.removeAllRanges();
  }

  function editComment(i: number) {
    const c = comments[i];
    const rect =
      (c.anchor ? rangesFor(c.anchor)[0]?.getBoundingClientRect() : null) ??
      blockEls[blockOf(c)]?.getBoundingClientRect();
    composer = {
      anchor: c.anchor ?? null,
      quote: c.quote,
      heading: c.heading,
      startLine: c.startLine,
      endLine: c.endLine,
      text: c.text,
      files: [...(c.files ?? [])],
      editing: i,
      ...place(rect ?? { left: 40, bottom: 80 })
    };
  }

  function removeComment(i: number) {
    comments.splice(i, 1);
    saveDraft(ping.ping_id, comments);
    if (hovered === i) hovered = null;
    if (composer?.editing === i) closeComposer();
    else if (composer && composer.editing !== null && composer.editing > i) composer.editing -= 1;
  }

  function reveal(c: ReviewComment) {
    blockEls[blockOf(c)]?.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }

  function onComposerKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      closeComposer();
    } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      commitComment();
    }
  }

  const trunc = (s: string, n: number) => (s.length > n ? s.slice(0, n) + '…' : s);

  // ---- submit -------------------------------------------------------------

  const feedback = () => composeFeedback(comments, files.map((f) => f.name));

  async function submit(answer: string, text?: string) {
    busy = true;
    error = '';
    try {
      // Per-comment attachments ride along with the review-level ones — all
      // land in the workspace, and the feedback prompt names them per comment.
      const all = [...comments.flatMap((c) => c.files ?? []), ...files];
      const ids = all.length ? (await api.uploadAttachments(all)).map((a) => a.id) : undefined;
      await api.answerPing(ping.ping_id, answer, text, ids);
      clearDraft(ping.ping_id);
      onanswered();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  async function requestChanges() {
    if (comments.length === 0) return;
    await submit('reject', feedback());
  }

  async function sendReview() {
    if (comments.length === 0) return;
    await submit(feedback());
  }

  async function chooseOption(opt: string) {
    await submit(opt, comments.length > 0 ? feedback() : undefined);
  }

  async function approve() {
    if (comments.length > 0 && !confirm(`Approve and discard ${comments.length} unsent comment(s)?`))
      return;
    await submit('approve');
  }
</script>

{#if error}<div class="error">{error}</div>{/if}

{#if loading}
  <div class="empty">Loading {ping.payload_ref}…</div>
{:else if blocks.length > 0}
  <div class="review row" style="align-items:flex-start; gap:14px">
    <div class="card plan" style="flex:2; min-width:0" bind:this={planEl}>
      <div class="muted small mono" style="margin-bottom:8px">{path}</div>
      {#if extraFiles.length}
        <div style="display:flex; flex-wrap:wrap; gap:8px; margin-bottom:10px; align-items:center">
          <span class="small muted">From the agent:</span>
          {#each extraFiles as f (f.path)}
            {#if !f.captured || !f.snapshot_id}
              <span class="small mono muted" title="this file was not captured when the ping was raised">📎 {f.path} (not captured)</span>
            {:else if f.mime?.startsWith('image/')}
              <a href={api.snapshotUrl(f.snapshot_id)} target="_blank" rel="noreferrer" title={f.path}>
                <img src={api.snapshotUrl(f.snapshot_id)} alt={f.path}
                  style="max-height:80px; border-radius:4px; border:1px solid var(--border,#444)" />
              </a>
            {:else}
              <a class="small mono" href={api.snapshotUrl(f.snapshot_id)} target="_blank" rel="noreferrer">📎 {f.path}</a>
            {/if}
          {/each}
        </div>
      {/if}
      {#each blocks as b, i (i)}
        <div
          class="block"
          data-idx={i}
          bind:this={blockEls[i]}
          class:commented={comments.some((c) => spans(c, i))}
          class:active={hovered !== null && spans(comments[hovered], i)}
          class:pending={!!composer?.anchor && i >= composer.anchor.startBlock && i <= composer.anchor.endBlock}
        >
          <div class="md">{@html b.html}</div>
        </div>
      {/each}
    </div>

    <div class="card side" style="flex:1; min-width:260px">
      <h2 style="margin-top:0">Review</h2>
      <div role="list">
        {#each comments as c, i (c)}
          <div
            class="comment"
            class:hot={hovered === i}
            role="listitem"
            onmouseenter={() => (hovered = i)}
            onmouseleave={() => (hovered = null)}
          >
            <div class="row" style="gap:6px; flex-wrap:nowrap">
              <button class="jump muted small" title="Scroll to this section" onclick={() => reveal(c)}>
                § {c.heading || '(top)'} · lines {c.startLine}–{c.endLine}
              </button>
              <button class="small" style="margin-left:auto" onclick={() => editComment(i)}>Edit</button>
              <button class="danger small" onclick={() => removeComment(i)} aria-label="Delete comment">×</button>
            </div>
            <blockquote class="small mono">{trunc(c.quote, 120)}</blockquote>
            <div class="small" style="white-space:pre-wrap">{c.text}</div>
            {#if c.files?.length}
              <div class="muted small" style="margin-top:4px">
                📎 {c.files.map((f) => f.name).join(', ')}
              </div>
            {/if}
          </div>
        {:else}
          <p class="muted small">
            No comments yet. <strong>Select any text</strong> in the plan — a feedback box opens
            where you let go. All comments submit together as one feedback prompt.
          </p>
        {/each}
      </div>
      <div style="margin-top:12px">
        <AttachFiles bind:files label="Attach files to your review" />
      </div>
      {#if isGate}
        <div class="row" style="margin-top:12px">
          <button class="approve" disabled={busy} onclick={approve}>Approve</button>
          <button class="reject" disabled={busy || comments.length === 0} onclick={requestChanges}>
            Request changes ({comments.length})
          </button>
        </div>
      {:else if ping.options?.length}
        <div class="row" style="margin-top:12px; flex-wrap:wrap; gap:8px">
          {#each ping.options as opt (opt)}
            <button class="primary" disabled={busy} onclick={() => chooseOption(opt)}>{opt}</button>
          {/each}
        </div>
        <p class="muted small" style="margin-top:6px">
          Your pick answers the agent's closed question; comments (if any) ride along as a note.
        </p>
      {:else}
        <div class="row" style="margin-top:12px">
          <button class="primary" disabled={busy || comments.length === 0} onclick={sendReview}>
            Send review ({comments.length})
          </button>
        </div>
      {/if}
      <p class="muted small" style="margin-top:8px">
        Drafts survive reloads (kept in this browser); attachments have to be re-picked.
      </p>
    </div>
  </div>

  {#if composer}
    <div
      class="card composer"
      style="left:{composer.x}px; top:{composer.y}px"
      role="dialog"
      aria-label={composer.editing !== null ? 'Edit feedback' : 'Add feedback'}
      tabindex="-1"
      onkeydown={onComposerKey}
    >
      <div class="muted small">§ {composer.heading || '(top)'} · lines {composer.startLine}–{composer.endLine}</div>
      <blockquote class="small mono">{trunc(composer.quote, 180)}</blockquote>
      <textarea
        bind:this={composerEl}
        bind:value={composer.text}
        rows="3"
        placeholder="What should change here?"
      ></textarea>
      <div style="display:flex; align-items:flex-start; margin-top:8px">
        <AttachFiles bind:files={composer.files} label="Attach" />
      </div>
      <div class="row" style="margin-top:10px">
        <button class="primary" onclick={commitComment} disabled={!composer.text.trim()}>
          {composer.editing !== null ? 'Save' : 'Add'}
        </button>
        <button onclick={closeComposer}>Close</button>
        <span class="muted small" style="margin-left:auto">⌘↵ to add · esc to close</span>
      </div>
    </div>
  {/if}
{:else}
  <div class="empty">The artifact is empty.</div>
{/if}

<style>
  .block {
    position: relative;
    border-radius: 4px;
    padding: 0 6px;
    margin: 0 -6px;
    transition: background 0.12s ease;
  }
  .block.commented {
    box-shadow: inset 2px 0 0 var(--amber, #fbbf24);
  }
  .block.active,
  .block.pending {
    background: color-mix(in srgb, var(--amber, #fbbf24) 9%, transparent);
  }

  /* Exact-selection highlights. ::highlight() isn't attached to an element,
     so it can't be scoped — hence :global. Falls back to the block tint above
     where the Custom Highlight API is missing. */
  :global(::highlight(apo-plan-pending)) {
    background: color-mix(in srgb, var(--accent, #8b7cf6) 45%, transparent);
  }
  :global(::highlight(apo-plan-active)) {
    background: color-mix(in srgb, var(--amber, #fbbf24) 40%, transparent);
  }

  .composer {
    position: fixed;
    z-index: 60;
    width: 340px;
    margin: 0;
    padding: 12px;
    background: var(--panel2, #1e2230);
    box-shadow: 0 12px 32px rgba(0, 0, 0, 0.55);
  }

  .comment {
    border-top: 1px solid var(--border, #333);
    padding: 8px 6px;
    margin: 0 -6px;
    border-radius: 4px;
  }
  .comment.hot {
    background: color-mix(in srgb, var(--amber, #fbbf24) 8%, transparent);
  }
  .comment .jump {
    background: none;
    border: none;
    padding: 0;
    text-align: left;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .comment .jump:hover {
    color: var(--text, #e6e9f0);
  }

  blockquote {
    white-space: pre-wrap;
    opacity: 0.7;
    margin: 6px 0;
    padding-left: 8px;
    border-left: 2px solid var(--border, #333);
  }

  .side {
    position: sticky;
    top: 12px;
  }

  .md :global(h1),
  .md :global(h2),
  .md :global(h3) {
    margin: 12px 0 6px;
  }
  .md :global(pre) {
    overflow-x: auto;
  }
</style>
