<script lang="ts">
  import { api, type Ping } from '$lib/api';
  import AttachFiles from '$lib/AttachFiles.svelte';
  import {
    clearDraft,
    composeFeedback,
    loadDraft,
    planBlocks,
    saveDraft,
    type PlanBlock,
    type ReviewComment
  } from '$lib/planreview';
  import { onMount } from 'svelte';

  let { ping, onanswered }: { ping: Ping; onanswered: () => void } = $props();

  // Gates answer approve/reject; ask_human artifact reviews (PLAN4 M16)
  // answer with the composed review text — or a declared option with the
  // review as its note.
  const isGate = $derived(['plan_approval', 'final_review'].includes(ping.type));

  let blocks = $state<PlanBlock[]>([]);
  let path = $state('');
  let error = $state('');
  let loading = $state(true);
  let comments = $state<ReviewComment[]>([]);
  let busy = $state(false);
  let files = $state<File[]>([]); // the human's answer attachments
  let extraFiles = $state<{ path: string; mime: string }[]>([]); // agent-attached extras

  // Comment being written: anchored to a block, quote defaults to the
  // selected text (if the selection is inside the block) or the block head.
  let draftFor = $state<number | null>(null);
  let draftText = $state('');
  let draftQuote = $state('');

  onMount(async () => {
    comments = loadDraft(ping.ping_id);
    try {
      const art = await api.pingArtifact(ping.ping_id);
      path = art.path;
      blocks = planBlocks(art.content);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
    // Extra agent-attached files (multi ask_human), beyond the primary doc.
    try {
      const { files: fs } = await api.pingArtifacts(ping.ping_id);
      extraFiles = fs.filter((f) => f.path !== path);
    } catch {
      /* none */
    }
  });

  function openComment(i: number) {
    const b = blocks[i];
    const sel = window.getSelection()?.toString().trim();
    draftFor = i;
    draftQuote = sel && sel.length > 2 ? sel : b.raw.trim().slice(0, 200);
    draftText = '';
  }

  function addComment() {
    if (draftFor === null || !draftText.trim()) return;
    const b = blocks[draftFor];
    comments.push({
      quote: draftQuote,
      heading: b.heading,
      startLine: b.startLine,
      endLine: b.endLine,
      text: draftText.trim()
    });
    saveDraft(ping.ping_id, comments);
    draftFor = null;
    draftText = '';
  }

  function removeComment(i: number) {
    comments.splice(i, 1);
    saveDraft(ping.ping_id, comments);
  }

  async function submit(answer: string, feedback?: string) {
    busy = true;
    error = '';
    try {
      const ids = files.length ? (await api.uploadArtifacts(files)).map((a) => a.id) : undefined;
      await api.answerPing(ping.ping_id, answer, feedback, ids);
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
    await submit('reject', composeFeedback(comments));
  }

  async function sendReview() {
    if (comments.length === 0) return;
    await submit(composeFeedback(comments));
  }

  async function chooseOption(opt: string) {
    await submit(opt, comments.length > 0 ? composeFeedback(comments) : undefined);
  }

  async function approve() {
    if (comments.length > 0 && !confirm(`Approve and discard ${comments.length} unsent comment(s)?`)) return;
    await submit('approve');
  }
</script>

{#if error}<div class="error">{error}</div>{/if}

{#if loading}
  <div class="empty">Loading {ping.payload_ref}…</div>
{:else if blocks.length > 0}
  <div class="review row" style="align-items:flex-start; gap:14px">
    <div class="card plan" style="flex:2; min-width:0">
      <div class="muted small mono" style="margin-bottom:8px">{path}</div>
      {#if extraFiles.length}
        <div style="display:flex; flex-wrap:wrap; gap:8px; margin-bottom:10px">
          {#each extraFiles as f (f.path)}
            {#if f.mime.startsWith('image/')}
              <a href={api.pingArtifactFileUrl(ping.ping_id, f.path)} target="_blank" rel="noreferrer" title={f.path}>
                <img src={api.pingArtifactFileUrl(ping.ping_id, f.path)} alt={f.path}
                  style="max-height:80px; border-radius:4px; border:1px solid var(--border,#444)" />
              </a>
            {:else}
              <a class="small mono" href={api.pingArtifactFileUrl(ping.ping_id, f.path)} target="_blank" rel="noreferrer">📎 {f.path}</a>
            {/if}
          {/each}
        </div>
      {/if}
      {#each blocks as b, i (i)}
        {@const n = comments.filter((c) => c.startLine === b.startLine).length}
        <div class="block" class:commented={n > 0}>
          <div class="md">{@html b.html}</div>
          <button class="add small" title="Comment on this section ({b.heading || 'top'}, lines {b.startLine}–{b.endLine}). Select text first to quote it."
            onclick={() => openComment(i)}>+{#if n > 0}<span class="count">{n}</span>{/if}</button>
        </div>
        {#if draftFor === i}
          <div class="card" style="margin:6px 0 12px">
            <div class="muted small">
              § {b.heading || '(top)'} · lines {b.startLine}–{b.endLine}
            </div>
            <blockquote class="small mono" style="white-space:pre-wrap; opacity:.8; margin:6px 0">{draftQuote}</blockquote>
            <textarea rows="3" bind:value={draftText} placeholder="What should change here?"></textarea>
            <div class="row" style="margin-top:8px">
              <button class="primary" onclick={addComment} disabled={!draftText.trim()}>Add comment</button>
              <button onclick={() => (draftFor = null)}>Cancel</button>
            </div>
          </div>
        {/if}
      {/each}
    </div>

    <div class="card" style="flex:1; min-width:260px; position:sticky; top:12px">
      <h2 style="margin-top:0">Review</h2>
      {#each comments as c, i (i)}
        <div class="comment">
          <div class="muted small">§ {c.heading || '(top)'} · lines {c.startLine}–{c.endLine}
            <button class="danger small" style="float:right" onclick={() => removeComment(i)}>×</button>
          </div>
          <blockquote class="small mono" style="white-space:pre-wrap; opacity:.7; margin:4px 0">{c.quote.slice(0, 120)}{c.quote.length > 120 ? '…' : ''}</blockquote>
          <div class="small" style="white-space:pre-wrap">{c.text}</div>
        </div>
      {:else}
        <p class="muted small">
          No comments yet. Hover a section of the plan and hit <span class="mono">+</span> — select
          text first to quote it precisely. All comments submit together as one feedback prompt.
        </p>
      {/each}
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
      <p class="muted small" style="margin-top:8px">Drafts survive reloads (kept in this browser).</p>
    </div>
  </div>
{:else}
  <div class="empty">The artifact is empty.</div>
{/if}

<style>
  .block {
    position: relative;
    border-radius: 4px;
    padding-right: 34px;
  }
  .block:hover {
    background: color-mix(in srgb, currentColor 4%, transparent);
  }
  .block.commented {
    box-shadow: inset 2px 0 0 var(--yellow, #e0af68);
  }
  .block .add {
    position: absolute;
    top: 2px;
    right: 0;
    opacity: 0;
    padding: 0 8px;
  }
  .block:hover .add,
  .block.commented .add {
    opacity: 1;
  }
  .add .count {
    margin-left: 4px;
    color: var(--yellow, #e0af68);
  }
  .comment {
    border-top: 1px solid var(--border, #333);
    padding: 8px 0;
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
