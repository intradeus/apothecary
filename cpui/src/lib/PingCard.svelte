<script lang="ts">
  import { ago, api } from '$lib/api';
  import AttachFiles from '$lib/AttachFiles.svelte';
  import { mdToHtml } from '$lib/planreview';
  import { isGatePing, type PingLike } from '$lib/ping';

  let {
    ping,
    busy = false,
    onanswer
  }: {
    ping: PingLike;
    busy?: boolean;
    // answer is the choice (approve|reject|option|free text); note is the
    // optional feedback that rides along (required only when rejecting);
    // artifactIds are files the human attached to the answer.
    onanswer: (answer: string, note?: string, artifactIds?: string[]) => void;
  } = $props();

  // Owned here (not a bindable prop): each card keeps its own draft, keyed by
  // ping identity in the parent's {#each}, so a bind:{undefined} can't crash.
  let note = $state('');
  let files = $state<File[]>([]);
  let uploading = $state(false);
  // Upload any attached files first, then hand the answer up.
  async function submit(answer: string) {
    uploading = true;
    try {
      const ids = files.length ? (await api.uploadArtifacts(files)).map((a) => a.id) : undefined;
      onanswer(answer, note, ids);
    } finally {
      uploading = false;
    }
  }
  const isGate = $derived(isGatePing(ping.type));
  const isPending = $derived((ping.status ?? 'pending') === 'pending');
  const options = $derived(ping.options ?? []);
  // Gate verbs are plain: what this particular gate means — re-run a stage,
  // merge, give up — is said in its question, which the flow author wrote.
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
{:else}
  <AttachFiles bind:files label="Attach files to your answer" />
  {#if isGate}
    <textarea bind:value={note} placeholder="Feedback (required when rejecting)"></textarea>
    <div class="row" style="margin-top:10px">
      <button class="approve" disabled={busy || uploading} onclick={() => submit('approve')}>Approve</button>
      <button class="reject" disabled={busy || uploading || !note.trim()} onclick={() => submit('reject')}>Reject with feedback</button>
    </div>
  {:else if options.length}
    <textarea bind:value={note} placeholder="Optional note — or your own answer if none of the options fit"></textarea>
    <div class="row" style="margin-top:10px; flex-wrap:wrap; gap:8px">
      {#each options as opt (opt)}
        <button class="primary" disabled={busy || uploading} onclick={() => submit(opt)}>{opt}</button>
      {/each}
      <button
        disabled={busy || uploading || !note.trim()}
        title="None of the options fit — sends the note text as your answer"
        onclick={() => submit('other')}>Other…</button
      >
    </div>
  {:else}
    <textarea bind:value={note} placeholder="Your answer to the agent…"></textarea>
    <div class="row" style="margin-top:10px">
      <button class="primary" disabled={busy || uploading || !note.trim()} onclick={() => submit(note.trim())}>Send answer</button>
    </div>
  {/if}
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
