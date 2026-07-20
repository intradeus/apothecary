<script lang="ts">
  import { api, type Ping } from '$lib/api';
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

  let blocks = $state<PlanBlock[]>([]);
  let path = $state('');
  let error = $state('');
  let loading = $state(true);
  let comments = $state<ReviewComment[]>([]);
  let busy = $state(false);

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

  async function requestChanges() {
    if (comments.length === 0) return;
    busy = true;
    error = '';
    try {
      await api.answerPing(ping.ping_id, 'reject', composeFeedback(comments));
      clearDraft(ping.ping_id);
      onanswered();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  async function approve() {
    if (comments.length > 0 && !confirm(`Approve and discard ${comments.length} unsent comment(s)?`)) return;
    busy = true;
    error = '';
    try {
      await api.answerPing(ping.ping_id, 'approve');
      clearDraft(ping.ping_id);
      onanswered();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if error}<div class="error">{error}</div>{/if}

{#if loading}
  <div class="empty">Loading {ping.payload_ref}…</div>
{:else if blocks.length > 0}
  <div class="review row" style="align-items:flex-start; gap:14px">
    <div class="card plan" style="flex:2; min-width:0">
      <div class="muted small mono" style="margin-bottom:8px">{path}</div>
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
      <div class="row" style="margin-top:12px">
        <button class="approve" disabled={busy} onclick={approve}>Approve</button>
        <button class="reject" disabled={busy || comments.length === 0} onclick={requestChanges}>
          Request changes ({comments.length})
        </button>
      </div>
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
