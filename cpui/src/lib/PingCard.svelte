<script lang="ts">
	import { api } from '$lib/api';
	import { ago } from '$lib/format';
	import AttachFiles from '$lib/AttachFiles.svelte';
	import { mdToHtml } from '$lib/planreview';
	import { isPhaseConfirm, SELECT_NONE, type PingLike } from '$lib/ping';

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
	let uploadErr = $state('');
	// Upload any attached files first, then hand the answer up. A failed
	// upload SURFACES and keeps the draft — it used to be swallowed, leaving
	// the card idle with the answer silently unsent.
	async function submit(answer: string) {
		uploading = true;
		uploadErr = '';
		try {
			const ids = files.length ? (await api.files.upload(files)).map((a) => a.id) : undefined;
			onanswer(answer, note, ids);
		} catch (e) {
			uploadErr = e instanceof Error ? e.message : String(e);
		} finally {
			uploading = false;
		}
	}
	// The card renders the ANSWER KIND; the type only tunes the wording (a
	// phase confirmation is a decision read as "run it / not now").
	const kind = $derived(ping.answerKind);
	const confirmPhase = $derived(isPhaseConfirm(ping.type));
	const options = $derived(ping.options ?? []);
	// multi: every option starts checked — unchecking is the deliberate act.
	let picked = $state<Record<string, boolean>>({});
	$effect(() => {
		for (const o of options) if (!(o.id in picked)) picked[o.id] = true;
	});
	const pickedIds = $derived(options.filter((o) => picked[o.id]).map((o) => o.id));
	const isPending = $derived((ping.status ?? 'pending') === 'pending');
	// Decision verbs are plain: what this particular gate means — re-run a
	// stage, merge, give up — is said in its question, which the flow author
	// wrote.
</script>

<div class="md" style="margin:8px 0">{@html mdToHtml(ping.question)}</div>
{#if ping.context}<div class="md muted small">{@html mdToHtml(ping.context)}</div>{/if}

{#if ping.link}
	<!-- Typed at the source (DESIGN-FILES §4): a link payload links out… -->
	<p class="mono small"><a href={ping.link} target="_blank" rel="noreferrer">{ping.link} ↗</a></p>
{:else if ping.primaryRef}
	<!-- …and a file payload opens the review workbench. -->
	<p class="mono small">
		{#if isPending}
			{ping.primaryRef} — <a href={`/pings/${ping.id}`}>review &amp; comment →</a>
		{:else}{ping.primaryRef}{/if}
	</p>
{/if}

{#if ping.status === 'answered'}
	<div class="answerbox">
		<div class="small muted">
			answered {ping.answeredAt ? ago(ping.answeredAt) : ''} —
			<strong class={ping.answer === 'approve' ? 'ok' : ping.answer === 'reject' ? 'bad' : ''}
				>{ping.answer}</strong
			>
		</div>
		{#if ping.feedback}<div class="md small" style="margin-top:4px">
				{@html mdToHtml(ping.feedback)}
			</div>{/if}
	</div>
{:else}
	<AttachFiles bind:files label="Attach files to your answer" />
	{#if uploadErr}<div class="error small">upload failed: {uploadErr}</div>{/if}
	{#if kind === 'multi'}
		<div class="picklist">
			{#each options as o (o.id)}
				<label class="pick">
					<input type="checkbox" bind:checked={picked[o.id]} />
					<span
						>{o.label}{#if o.detail}<span class="muted"> — {o.detail}</span>{/if}</span
					>
				</label>
			{/each}
		</div>
		<textarea bind:value={note} placeholder="Optional note"></textarea>
		<div class="row" style="margin-top:10px">
			<button
				class="approve"
				disabled={busy || uploading || pickedIds.length === 0}
				onclick={() => submit(pickedIds.join('\n'))}>Confirm {pickedIds.length} selected</button
			>
			<button class="reject" disabled={busy || uploading} onclick={() => submit(SELECT_NONE)}
				>Select none</button
			>
		</div>
	{:else if kind === 'decision' && confirmPhase}
		<div class="row" style="margin-top:10px">
			<button class="approve" disabled={busy || uploading} onclick={() => submit('approve')}
				>Run it</button
			>
			<button class="reject" disabled={busy || uploading} onclick={() => submit('reject')}
				>Not now</button
			>
		</div>
		<p class="small muted" style="margin:6px 0 0">
			Not now: it is offered again only when the situation changes (the target moves, new comments
			arrive).
		</p>
	{:else if kind === 'decision'}
		<textarea bind:value={note} placeholder="Feedback (required when rejecting)"></textarea>
		<div class="row" style="margin-top:10px">
			<button class="approve" disabled={busy || uploading} onclick={() => submit('approve')}
				>Approve</button
			>
			<button
				class="reject"
				disabled={busy || uploading || !note.trim()}
				onclick={() => submit('reject')}>Reject with feedback</button
			>
		</div>
	{:else if kind === 'choice'}
		<textarea
			bind:value={note}
			placeholder="Optional note — or your own answer if none of the options fit"></textarea>
		<div class="row" style="margin-top:10px; flex-wrap:wrap; gap:8px">
			{#each options as opt (opt.id)}
				<button
					class="primary"
					disabled={busy || uploading}
					title={opt.detail ?? ''}
					onclick={() => submit(opt.id)}>{opt.label}</button
				>
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
			<button
				class="primary"
				disabled={busy || uploading || !note.trim()}
				onclick={() => submit(note.trim())}>Send answer</button
			>
		</div>
	{/if}
{/if}

<style>
	.picklist {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-top: 8px;
	}
	.pick {
		display: flex;
		gap: 8px;
		align-items: flex-start;
		font-size: 13px;
		margin: 0;
		cursor: pointer;
	}
	.pick input {
		width: auto;
		margin-top: 2px;
	}
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
