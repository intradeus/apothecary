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
	import Markdown from '$lib/ui/Markdown.svelte';
	import FileChip from '$lib/ui/FileChip.svelte';
	import { I } from '$lib/ui/icons';
	import { t, th } from '$lib/i18n/index.svelte';
	import {
		answerMeta,
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
	/** The answer in flight, so only its button shows busy. */
	let sending = $state('');
	async function submit(answer: string) {
		sending = answer;
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
	const busyOn = (answer: string) => off && sending === answer;
	const verdict = $derived(answerMeta(ping));
	const pingRunId = $derived((ping as { runId?: string }).runId);
	const runLink = $derived(runHref ?? (pingRunId ? to.run(pingRunId) : undefined));

	/** The debug leaf carries facts, not prose: the card is the wording. */
	const debug = $derived(ping.type === 'debug');
	// Giving up at a reaction's debug leaf ends that round, not the run: the
	// engine says which on the ping, and the buttons say what will happen.
	const endsRound = $derived(debug && ping.endsPhase);
	const phaseName = $derived(ping.phase ?? '');
	const aside = $derived(
		ping.type === 'gate'
			? t('ping.aside.gate')
			: debug
				? t('ping.aside.debug')
				: unwanted
					? t('ping.aside.agent')
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
		<span class="hue-{verdict.hue} glyph">{verdict.glyph}</span>
		<span class="ty">{meta.label}</span>
		{#if stageId}<span class="mono fnt">{stageId}</span>{/if}
		<span class="ell grow what">
			{#if status === 'withdrawn'}
				{t('ping.withdrawn')}
			{:else}
				{ping.answeredAt
					? t('ping.answeredAgo', { ago: ago(ping.answeredAt) })
					: t('ping.answered')}
				<b class="hue-{verdict.hue}">{ping.answer}</b>{#if ping.feedback}<span
						class="fnt"
						title={ping.feedback}>{' · '}{ping.feedback}</span
					>{/if}
			{/if}
		</span>
	</div>
{:else}
	<div class="pc lg wash h-amb" class:compact>
		<div class="head">
			<span class="count hot mark" aria-hidden="true">?</span>
			<span class="ty">{meta.label}</span>
			{#if aside}<span class="aside">{aside}</span>{/if}
			<span class="spacer"></span>
			<span class="when">
				{#if stageId}<span class="mono">{stageId}</span
					>{/if}{#if stageId && ping.raisedAt}{' · '}{/if}{#if ping.raisedAt}<span
						title={fmtAbs(ping.raisedAt)}>{t('ping.raised', { ago: ago(ping.raisedAt) })}</span
					>{/if}
			</span>
			{#if unwanted}<span class="pill sm t-ros">{t('ping.unwanted')}</span>{/if}
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
			{#if debug}
				<div class="q">{@html th('ping.debugFailed', { stage: stageId })}</div>
				{#if ping.context}<pre class="out">{ping.context}</pre>{/if}
			{:else}
				<div class="q"><Markdown src={ping.question} /></div>
				{#if ping.context}<Markdown src={ping.context} quiet />{/if}
			{/if}

			{#if ping.link}
				<!-- Typed at the source (DESIGN-FILES §4): a link payload links out… -->
				<a class="ref" href={ping.link} target="_blank" rel="noreferrer">
					<span class="mono ell">{ping.link}</span><span>{I.external}</span>
				</a>
			{:else if isDocumentPing(ping) && ping.primaryRef}
				<!-- …and a file payload opens the review workbench. -->
				<div class="card-in docref">
					<FileChip name={ping.primaryRef} />
					<span class="spacer"></span>
					<a class="btn btn-sel btn-sm" href={to.ping(ping.id)}
						>{t('ping.reviewComment')} {I.forward}</a
					>
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
				<textarea bind:value={note} rows="2" placeholder={t('ping.optionalNote')}></textarea>
				<div class="acts">
					<button
						class="btn-hot btn-md"
						disabled={off || pickedIds.length === 0}
						aria-busy={busyOn(pickedIds.join('\n'))}
						onclick={() => submit(pickedIds.join('\n'))}
						>{t('ping.confirmSelected', { count: pickedIds.length })}</button
					>
					<button
						class="btn-hot-outline btn-md"
						disabled={off}
						aria-busy={busyOn(SELECT_NONE)}
						onclick={() => submit(SELECT_NONE)}>{t('ping.selectNone')}</button
					>
					{@render attachBtn()}
				</div>
			{:else if kind === 'decision' && confirmPhase}
				<div class="acts">
					<button
						class="btn-hot btn-md"
						disabled={off}
						aria-busy={busyOn('approve')}
						onclick={() => submit('approve')}>{t('ping.runIt')}</button
					>
					<button
						class="btn-hot-outline btn-md"
						disabled={off}
						aria-busy={busyOn('reject')}
						onclick={() => submit('reject')}>{t('ping.notNow')}</button
					>
					{@render attachBtn()}
				</div>
				<p class="help">{t('ping.notNowHelp')}</p>
			{:else if kind === 'decision'}
				{#if noteOpen}
					<textarea
						bind:value={note}
						rows="3"
						placeholder={debug
							? endsRound
								? t('ping.debugEndPlaceholder', { phase: phaseName })
								: t('ping.debugReasonPlaceholder')
							: t('ping.changesPlaceholder')}
						onkeydown={(e) => keys(e, () => note.trim() && !off && submit('reject'))}></textarea>
				{/if}
				<div class="acts">
					<button
						class="btn-hot btn-md"
						disabled={off}
						aria-busy={busyOn('approve')}
						onclick={() => submit('approve')}
						>{debug ? t('ping.continueRerun', { stage: stageId ?? '' }) : t('ping.approve')}</button
					>
					{#if noteOpen}
						<button
							class="btn-hot-outline btn-md"
							disabled={off || !note.trim()}
							aria-busy={busyOn('reject')}
							onclick={() => submit('reject')}
							>{debug
								? endsRound
									? t('ping.endPhase', { phase: phaseName })
									: t('ping.abandonRun')
								: t('ping.sendChanges')}</button
						>
						<button class="btn-ghost btn-sm" onclick={() => (noteOpen = false)}
							>{t('common.cancel')}</button
						>
					{:else}
						<button class="btn-hot-outline btn-md" disabled={off} onclick={() => (noteOpen = true)}
							>{debug
								? endsRound
									? t('ping.endPhaseOpen')
									: t('ping.abandon')
								: t('ping.requestChanges')}</button
						>
					{/if}
					{@render attachBtn()}
				</div>
			{:else if kind === 'choice'}
				{#if noteOpen}
					<textarea bind:value={note} rows="2" placeholder={t('ping.otherPlaceholder')}></textarea>
				{/if}
				<div class="acts">
					{#each options as opt, i (opt.id)}
						<button
							class="{i === 0 ? 'btn-hot' : 'btn-hot-outline'} btn-md"
							disabled={off}
							aria-busy={busyOn(opt.id)}
							title={opt.detail ?? ''}
							onclick={() => submit(opt.id)}>{opt.label}</button
						>
					{/each}
					{#if noteOpen}
						<button
							class="btn-dashed btn-md"
							disabled={off || !note.trim()}
							aria-busy={busyOn('other')}
							title={t('ping.otherTitle')}
							onclick={() => submit('other')}>{t('ping.sendAsAnswer')}</button
						>
					{:else}
						<button class="btn-dashed btn-md" disabled={off} onclick={() => (noteOpen = true)}
							>{t('ping.other')}</button
						>
					{/if}
					{@render attachBtn()}
				</div>
			{:else}
				<textarea
					bind:value={note}
					rows="3"
					placeholder={t('ping.answerPlaceholder')}
					onkeydown={(e) => keys(e, () => note.trim() && !off && submit(note.trim()))}></textarea>
				<div class="acts">
					<button
						class="btn-hot btn-md"
						disabled={off || !note.trim()}
						aria-busy={off && sending === note.trim()}
						onclick={() => submit(note.trim())}>{t('ping.sendAnswer')}</button
					>
					<span class="help"><span class="kbd">⌘↵</span> {t('ping.sendHint')}</span>
					{@render attachBtn()}
				</div>
			{/if}

			{#if showAttach || files.length}
				<AttachFiles bind:files label={t('ping.attachToAnswer')} compact />
			{/if}
			{#if uploadErr}<div class="err">{t('ping.uploadFailed', { error: uploadErr })}</div>{/if}
		</div>
	</div>
{/if}

{#snippet attachBtn()}
	<span class="spacer"></span>
	<button class="btn-ghost btn-sm" onclick={() => (showAttach = !showAttach)}
		>{I.add}
		{t('ping.attach')}{#if files.length}<span class="mono">&nbsp;{files.length}</span>{/if}</button
	>
{/snippet}

<style>
	/* Control glass with an amber wash (.wash.h-amb): a human must act now. */
	.pc {
		border-radius: var(--r-float);
		border-color: var(--ambLn);
		box-shadow:
			var(--lgRim),
			0 0 var(--glow-lg) var(--ambGlow);
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
	/* The round amber "?" — the same mark as the desk's needs-you count. */
	.mark {
		flex: none;
		width: 22px;
		height: 22px;
		padding: 0;
		border-radius: 50%;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		font-size: var(--fs-label);
		box-shadow: 0 0 var(--glow-sm) var(--ambLn);
	}
	.ty {
		font-family: var(--serif);
		font-size: var(--fs-section);
		line-height: 1.1;
	}
	.aside {
		font-family: var(--serif);
		font-style: italic;
		font-size: var(--fs-aside);
		color: var(--mut);
	}
	.when {
		font-size: var(--fs-help);
		color: var(--mut);
		white-space: nowrap;
	}
	.when .mono {
		font-size: var(--fs-meta);
	}
	.ctx {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 2px 14px 0 46px;
		font-size: var(--fs-label);
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
	.q {
		text-wrap: pretty;
	}
	/* What the failed stage printed: verbatim, scrollable. */
	.out {
		margin: 0;
		padding: 8px 10px;
		max-height: 240px;
		overflow: auto;
		font-family: var(--mono, ui-monospace, monospace);
		font-size: var(--fs-label);
		white-space: pre-wrap;
		word-break: break-word;
		color: var(--tx);
		background: var(--cBg);
		border: 1px solid var(--ln2);
		border-radius: 8px;
	}
	.ref {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		align-self: flex-start;
		max-width: 100%;
		color: var(--az);
		font-size: var(--fs-label);
	}
	.docref {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 5px 6px 5px 5px;
		min-width: 0;
	}
	.acts {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
	}
	.help {
		margin: 0;
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
		font-size: var(--fs-body);
		font-weight: 400;
		margin: 0;
		cursor: pointer;
	}
	.pick input {
		width: auto;
		margin-top: 3px;
	}

	/* answered / withdrawn */
	.pc-done {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 12px;
		border-radius: var(--r-card);
		border: 1px solid var(--ln);
		background: var(--p1);
		color: var(--mut);
		font-size: var(--fs-label);
		min-width: 0;
		white-space: nowrap;
	}
	.pc-done .glyph {
		font-weight: 700;
	}
	.pc-done .ty {
		font-size: var(--fs-lead);
		color: var(--tx);
	}
	.pc-done .mono {
		font-size: var(--fs-meta);
	}
	.pc-done.withdrawn,
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
