<script lang="ts">
  import { ago } from '$lib/api';
  import { mdToHtml } from '$lib/planreview';
  import { GATE_TYPES, type PingLike } from '$lib/ping';

  let {
    ping,
    busy = false,
    onanswer
  }: {
    ping: PingLike;
    busy?: boolean;
    // answer is the choice (approve|reject|option|free text); note is the
    // optional feedback that rides along (required only when rejecting).
    onanswer: (answer: string, note?: string) => void;
  } = $props();

  // Owned here (not a bindable prop): each card keeps its own draft, keyed by
  // ping identity in the parent's {#each}, so a bind:{undefined} can't crash.
  let note = $state('');
  const isGate = $derived(GATE_TYPES.includes(ping.type));
  const isPending = $derived((ping.status ?? 'pending') === 'pending');
  const options = $derived(ping.options ?? []);
  // manual_debug is "go look at the workspace": approve = fixed/continue,
  // reject = give up (routes on_reject — typically abort).
  const approveLabel = $derived(ping.type === 'manual_debug' ? 'Resolved — continue' : 'Approve');
  const rejectLabel = $derived(ping.type === 'manual_debug' ? 'Give up (reject)' : 'Reject with feedback');
</script>

<div class="md" style="margin:8px 0">{@html mdToHtml(ping.question)}</div>
{#if ping.context}<div class="md muted small">{@html mdToHtml(ping.context)}</div>{/if}

{#if ping.payload_ref}
  <p class="mono small">
    {#if ping.payload_ref.startsWith('http')}
      <a href={ping.payload_ref} target="_blank" rel="noreferrer">{ping.payload_ref} ↗</a>
    {:else if isPending}
      {ping.payload_ref} — <a href={`/pings/${ping.ping_id}`}>review &amp; comment →</a>
    {:else}{ping.payload_ref}{/if}
  </p>
{/if}

{#if ping.status === 'answered'}
  <div class="answerbox">
    <div class="small muted">
      answered {ping.answered_at ? ago(ping.answered_at) : ''} —
      <strong class={ping.answer === 'approve' ? 'ok' : ping.answer === 'reject' ? 'bad' : ''}>{ping.answer}</strong>
    </div>
    {#if ping.feedback}<div class="md small" style="margin-top:4px">{@html mdToHtml(ping.feedback)}</div>{/if}
  </div>
{:else if isGate}
  <textarea bind:value={note} placeholder={ping.type === 'manual_debug' ? 'What you found / changed (required when giving up)' : 'Feedback (required when rejecting)'}></textarea>
  <div class="row" style="margin-top:10px">
    <button class="approve" disabled={busy} onclick={() => onanswer('approve', note)}>{approveLabel}</button>
    <button class="reject" disabled={busy || !note.trim()} onclick={() => onanswer('reject', note)}>{rejectLabel}</button>
  </div>
{:else if options.length}
  <textarea bind:value={note} placeholder="Optional note to send along…"></textarea>
  <div class="row" style="margin-top:10px; flex-wrap:wrap; gap:8px">
    {#each options as opt (opt)}
      <button class="primary" disabled={busy} onclick={() => onanswer(opt, note)}>{opt}</button>
    {/each}
  </div>
{:else}
  <textarea bind:value={note} placeholder="Your answer to the agent…"></textarea>
  <div class="row" style="margin-top:10px">
    <button class="primary" disabled={busy || !note.trim()} onclick={() => onanswer(note.trim())}>Send answer</button>
  </div>
{/if}

<style>
  .answerbox {
    border-left: 3px solid var(--border, #444);
    padding: 6px 10px;
    margin-top: 8px;
  }
  .answerbox .ok {
    color: var(--green, #4caf7d);
  }
  .answerbox .bad {
    color: var(--red, #e06c75);
  }
  .md :global(p) {
    margin: 4px 0;
  }
  .md :global(pre) {
    overflow-x: auto;
  }
</style>
