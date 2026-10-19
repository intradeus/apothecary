<script lang="ts">
	// Ping card (DESIGN 4.11): control glass with an amber wash, because a
	// human must act now. Head: badge, type, stage and age, and — in the inbox —
	// the run and ticket it belongs to. Body: the question, then actions shaped
	// by the ping's ANSWER KIND (the type only tunes wording). Answered and
	// withdrawn pings collapse to a single muted line.
	//
	// Used by the inbox and the run page. The required props (ping, busy,
	// onanswer) keep their old semantics; the rest are optional context.
	import { to } from '$lib/guild';
	import { api } from '$lib/api';
	import { ago, fmtAbs } from '$lib/format';
	import AttachFiles from '$lib/AttachFiles.svelte';
	import { pingMeta } from '$lib/ui/PingPill.svelte';
	import { mdToHtml } from '$lib/planreview';
	import { isLore } from '$lib/lore';
	import {
		isAgentPing,
		isDocumentPing,
		isPhaseConfirm,
		SELECT_NONE,
		type PingLike
	} from '$lib/ping';

	let {
		ping,
		busy = false,
		onanswer,
		runTitle,
		runHref,
		ticketTitle,
		ticketKey,
		stage,
		compact = false
	}: {
		ping: PingLike;
		busy?: boolean;
		// answer is the choice (approve|reject|option|free text); note is the
		// optional feedback that rides along (required only when rejecting);
		// artifactIds are files the human attached to the answer.
		onanswer: (answer: string, note?: string, artifactIds?: string[]) => void;
		/** The run's title, shown in the head (inbox cards always say which run). */
		runTitle?: string;
		/** Where the run title links; defaults to the run page when runId is known. */
		runHref?: string;
		ticketTitle?: string;
		ticketKey?: string;
		/** Overrides ping.stageId in the head. */
		stage?: string;
		/** Tighter padding, no body indent (docked or inline use). */
		compact?: boolean;
	} = $props();

	// Owned here (not a bindable prop): each card keeps its own draft, keyed by
	// ping identity in the parent's {#each}, so a bind:{undefined} can't crash.
	let note = $state('');
	let files = $state<File[]>([]);
	let uploading = $state(false);
	let uploadErr = $state('');
	let showAttach = $state(false);
	/** decision: "Request changes…" opens the feedback box. choice: "Other…". */
	let noteOpen = $state(false);

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
	const status = $derived(ping.status ?? 'pending');
	const isPending = $derived(status === 'pending');
	const meta = $derived(pingMeta(ping.type));
	const unwanted = $derived(isAgentPing(ping.type));
	const stageId = $derived(stage ?? ping.stageId);
	const off = $derived(busy || uploading);
	const pingRunId = $derived((ping as { runId?: string }).runId);
	const runLink = $derived(runHref ?? (pingRunId ? to.run(pingRunId) : undefined));

	const aside = $derived(
		!isLore()
			? ''
			: ping.type === 'gate'
				? 'the bench awaits your seal'
				: unwanted
					? 'a note from the builder'
					: ''
	);

	function keys(e: KeyboardEvent, send: () => void) {
		if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
			e.preventDefault();
			send();
		}
	}
</script>

{#if !isPending}
	<!-- Answered / withdrawn: one muted line. -->
	<div class="pc-done" class:withdrawn={status === 'withdrawn'}>
		<span class="glyph">{status === 'withdrawn' ? '–' : '✓'}</span>
		<span class="ty">{meta.label}</span>
		{#if stageId}<span class="mono st">{stageId}</span>{/if}
		<span class="ell what">
			{#if status === 'withdrawn'}
				withdrawn
			{:else}
				answered{ping.answeredAt ? ` ${ago(ping.answeredAt)}` : ''} —
				<b class:ok={ping.answer === 'approve'} class:bad={ping.answer === 'reject'}
					>{ping.answer}</b
				>{#if ping.feedback}<span class="fb" title={ping.feedback}> · {ping.feedback}</span>{/if}
			{/if}
		</span>
	</div>
{:else}
	<div class="pc" class:compact>
		<div class="head">
			<span class="badge" aria-hidden="true">?</span>
			<span class="ty">{meta.label}</span>
			{#if aside}<span class="aside">{aside}</span>{/if}
			<span class="spacer"></span>
			<span class="when">
				{#if stageId}<span class="mono">{stageId}</span
					>{/if}{#if stageId && ping.raisedAt}{' · '}{/if}{#if ping.raisedAt}<span
						title={fmtAbs(ping.raisedAt)}>raised {ago(ping.raisedAt)}</span
					>{/if}
			</span>
			{#if unwanted}<span class="cls">unwanted · counts against run</span>{/if}
		</div>
		{#if runTitle || ticketTitle || ticketKey}
			<div class="ctx">
				{#if runTitle}
					{#if runLink}<a href={runLink}>{runTitle}</a>{:else}<span>{runTitle}</span>{/if}
				{/if}
				{#if ticketKey || (ticketTitle && ticketTitle !== runTitle)}
					<span class="fnt">·</span>
					{#if ticketKey}<span class="tag">{ticketKey}</span>{/if}
					{#if ticketTitle && ticketTitle !== runTitle}<span class="ell">{ticketTitle}</span>{/if}
				{/if}
			</div>
		{/if}

		<div class="body">
			<div class="md q">{@html mdToHtml(ping.question)}</div>
			{#if ping.context}<div class="md ctxt">{@html mdToHtml(ping.context)}</div>{/if}

			{#if ping.link}
				<!-- Typed at the source (DESIGN-FILES §4): a link payload links out… -->
				<a class="ref" href={ping.link} target="_blank" rel="noreferrer">
					<span class="mono ell">{ping.link}</span><span>↗</span>
				</a>
			{:else if isDocumentPing(ping) && ping.primaryRef}
				<!-- …and a file payload opens the review workbench. -->
				<div class="docref">
					<span class="ico">▤</span>
					<span class="mono ell">{ping.primaryRef}</span>
					<a class="btn btn-sel btn-sm" href={to.ping(ping.id)}>Review &amp; comment →</a>
				</div>
			{/if}

			{#if kind === 'multi'}
				<div class="picklist">
					{#each options as o (o.id)}
						<label class="pick">
							<input type="checkbox" bind:checked={picked[o.id]} />
							<span
								>{o.label}{#if o.detail}<span class="mut"> — {o.detail}</span>{/if}</span
							>
						</label>
					{/each}
				</div>
				<textarea bind:value={note} rows="2" placeholder="Optional note"></textarea>
				<div class="acts">
					<button
						class="a-pri"
						disabled={off || pickedIds.length === 0}
						onclick={() => submit(pickedIds.join('\n'))}>Confirm {pickedIds.length} selected</button
					>
					<button class="a-sec" disabled={off} onclick={() => submit(SELECT_NONE)}
						>Select none</button
					>
					{@render attachBtn()}
				</div>
			{:else if kind === 'decision' && confirmPhase}
				<div class="acts">
					<button class="a-pri" disabled={off} onclick={() => submit('approve')}>Run it</button>
					<button class="a-sec" disabled={off} onclick={() => submit('reject')}>Not now</button>
					{@render attachBtn()}
				</div>
				<p class="hint">
					Not now: it is offered again only when the situation changes (the target moves, new
					comments arrive).
				</p>
			{:else if kind === 'decision'}
				{#if noteOpen}
					<textarea
						bind:value={note}
						rows="3"
						placeholder="What should change? Required — the run takes it as feedback."
						onkeydown={(e) => keys(e, () => note.trim() && !off && submit('reject'))}></textarea>
				{/if}
				<div class="acts">
					<button class="a-pri" disabled={off} onclick={() => submit('approve')}>Approve</button>
					{#if noteOpen}
						<button class="a-sec" disabled={off || !note.trim()} onclick={() => submit('reject')}
							>Send changes</button
						>
						<button class="btn-ghost btn-sm" onclick={() => (noteOpen = false)}>Cancel</button>
					{:else}
						<button class="a-sec" disabled={off} onclick={() => (noteOpen = true)}
							>Request changes…</button
						>
					{/if}
					{@render attachBtn()}
				</div>
			{:else if kind === 'choice'}
				{#if noteOpen}
					<textarea
						bind:value={note}
						rows="2"
						placeholder="Your own answer — or a note that rides along with an option"></textarea>
				{/if}
				<div class="acts">
					{#each options as opt, i (opt.id)}
						<button
							class={i === 0 ? 'a-pri' : 'a-sec'}
							disabled={off}
							title={opt.detail ?? ''}
							onclick={() => submit(opt.id)}>{opt.label}</button
						>
					{/each}
					{#if noteOpen}
						<button
							class="a-other"
							disabled={off || !note.trim()}
							title="None of the options fit — sends the note text as your answer"
							onclick={() => submit('other')}>Send as answer</button
						>
					{:else}
						<button class="a-other" disabled={off} onclick={() => (noteOpen = true)}>Other…</button>
					{/if}
					{@render attachBtn()}
				</div>
			{:else}
				<textarea
					bind:value={note}
					rows="3"
					placeholder="Your answer to the agent…"
					onkeydown={(e) => keys(e, () => note.trim() && !off && submit(note.trim()))}></textarea>
				<div class="acts">
					<button class="a-pri" disabled={off || !note.trim()} onclick={() => submit(note.trim())}
						>Send answer</button
					>
					<span class="fnt keyhint">⌘↵ send</span>
					{@render attachBtn()}
				</div>
			{/if}

			{#if showAttach || files.length}
				<AttachFiles bind:files label="Attach files to your answer" compact />
			{/if}
			{#if uploadErr}<div class="err">Upload failed: {uploadErr}</div>{/if}
		</div>
	</div>
{/if}

{#snippet attachBtn()}
	<span class="spacer"></span>
	<button class="btn-ghost btn-sm" onclick={() => (showAttach = !showAttach)}
		>+ Attach{#if files.length}<span class="mono">&nbsp;{files.length}</span>{/if}</button
	>
{/snippet}

<style>
	.pc {
		border-radius: 20px;
		background:
			radial-gradient(ellipse 70% 140% at 0% 0%, var(--ambBg), transparent 70%), var(--lgBg);
		border: 1px solid var(--ambLn);
		backdrop-filter: var(--lgBf);
		-webkit-backdrop-filter: var(--lgBf);
		box-shadow:
			var(--lgRim),
			0 0 44px var(--ambGlow);
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 11px 14px 2px;
		flex-wrap: wrap;
		row-gap: 4px;
	}
	.badge {
		flex: none;
		width: 22px;
		height: 22px;
		border-radius: 50%;
		background: var(--amb);
		color: var(--ink);
		display: flex;
		align-items: center;
		justify-content: center;
		font-weight: 800;
		font-size: 12px;
		box-shadow: 0 0 12px var(--ambLn);
	}
	.ty {
		font-family: var(--serif);
		font-size: 20px;
		line-height: 1.1;
	}
	.aside {
		font-family: var(--serif);
		font-style: italic;
		font-size: 14.5px;
		color: var(--mut);
	}
	.spacer {
		flex: 1;
	}
	.when {
		font-size: 11.5px;
		color: var(--mut);
		white-space: nowrap;
	}
	.when .mono {
		font-size: 11px;
	}
	.cls {
		font-size: 11px;
		font-weight: 600;
		padding: 1px 8px;
		border-radius: 12px;
		color: var(--ros);
		background: var(--rosBg);
		white-space: nowrap;
	}
	.ctx {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 2px 14px 0 46px;
		font-size: 12px;
		color: var(--mut);
		min-width: 0;
		white-space: nowrap;
	}
	.ctx a {
		color: var(--tx);
		font-weight: 500;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.body {
		padding: 8px 14px 14px 46px;
		display: flex;
		flex-direction: column;
		gap: 10px;
		min-width: 0;
	}
	.compact .body,
	.compact .ctx {
		padding-left: 14px;
	}
	.compact .ty {
		font-size: 18px;
	}
	.q {
		text-wrap: pretty;
	}
	.ctxt {
		font-size: 12.5px;
		color: var(--mut);
	}
	.ref {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		align-self: flex-start;
		max-width: 100%;
		color: var(--az);
		font-size: 12px;
	}
	.docref {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 6px 6px 6px 10px;
		border-radius: 12px;
		background: var(--p2);
		border: 1px solid var(--ln);
		min-width: 0;
	}
	.docref .ico {
		color: var(--mut);
	}
	.docref .mono {
		flex: 1;
	}
	.acts {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
	}
	.a-pri,
	.a-sec,
	.a-other {
		font-size: 12.5px;
		padding: 5px 13px;
		border-radius: 12px;
		box-shadow: none;
	}
	.a-pri {
		background: var(--amb);
		color: var(--ink);
		border-color: transparent;
		font-weight: 600;
		box-shadow: 0 0 14px var(--ambGlow);
	}
	.a-pri:hover {
		background: var(--amb);
		filter: brightness(1.06);
	}
	.a-sec {
		background: transparent;
		border: 1px solid var(--ambLn);
		color: var(--amb);
	}
	.a-sec:hover {
		background: var(--ambBg);
	}
	.a-other {
		background: transparent;
		border: 1px dashed var(--ln2);
		color: var(--mut);
	}
	.a-other:hover {
		background: var(--p2);
		color: var(--tx);
	}
	.keyhint {
		font-size: 11px;
	}
	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--mut);
	}
	.picklist {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.pick {
		display: flex;
		gap: 8px;
		align-items: flex-start;
		font-size: 13px;
		font-weight: 400;
		margin: 0;
		cursor: pointer;
	}
	.pick input {
		width: auto;
		margin-top: 3px;
	}
	.md :global(p) {
		margin: 4px 0;
	}
	.md :global(pre) {
		overflow-x: auto;
	}

	/* answered / withdrawn */
	.pc-done {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 12px;
		border-radius: 14px;
		border: 1px solid var(--ln);
		background: var(--p1);
		color: var(--mut);
		font-size: 12px;
		min-width: 0;
		white-space: nowrap;
	}
	.pc-done .glyph {
		color: var(--grn);
		font-weight: 700;
	}
	.pc-done .ty {
		font-size: 15px;
		color: var(--tx);
	}
	.pc-done .st {
		font-size: 11px;
		color: var(--fnt);
	}
	.pc-done .what {
		flex: 1;
	}
	.pc-done .ok {
		color: var(--grn);
	}
	.pc-done .bad {
		color: var(--ros);
	}
	.pc-done .fb {
		color: var(--fnt);
	}
	.pc-done.withdrawn {
		color: var(--fnt);
	}
	.pc-done.withdrawn .glyph {
		color: var(--fnt);
	}
	.pc-done.withdrawn .what,
	.pc-done.withdrawn .ty {
		text-decoration: line-through;
		color: var(--fnt);
	}
	@media (max-width: 760px) {
		.body,
		.ctx {
			padding-left: 14px;
		}
	}
</style>
