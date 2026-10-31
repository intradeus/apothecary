<script lang="ts">
	// Ping card (DESIGN 4.11): solid sky, because a
	// human must act now. Head: badge, type, stage and age, and — in the inbox —
	// the run and ticket it belongs to. Body: the question, then actions shaped
	// by the ping's ANSWER KIND (the type only tunes wording): a yes/no, or a
	// form — one option to pick (a button each), one free-text answer, or its
	// fields drawn (PingFormFields); any form may be declined with a reason.
	// Answered and withdrawn pings collapse to a single muted line.
	//
	// Used by the inbox and the run page. The required props (ping, busy,
	// onanswer) keep their old semantics; the rest are optional context.
	import { errorMessage } from '$lib/core/errors';
	import { to } from '$lib/core/urls';
	import { api } from '$lib/api';
	import { ago, fmtAbs } from '$lib/core/format';
	import AttachFiles from '$lib/artifacts/AttachFiles.svelte';
	import PingFormFields from '$lib/pings/PingFormFields.svelte';
	import { pingMeta } from '$lib/ui/PingPill.svelte';
	import Markdown from '$lib/ui/Markdown.svelte';
	import FileChip from '$lib/ui/FileChip.svelte';
	import { I } from '$lib/design/icons';
	import { t, th } from '$lib/i18n/index.svelte';
	import { untrack } from 'svelte';
	import {
		answerMeta,
		answerSummary,
		curatable,
		formContent,
		formDefaults,
		formMissing,
		formShape,
		isAgentPing,
		isDocumentPing,
		primaryFile,
		isPhaseConfirm,
		type PingLike
	} from '$lib/core/ping';
	import type { FormContent } from '$lib/domain';

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
		// answer is approve|reject (yes/no) or accept|decline (a form); note
		// is the feedback that rides along (required when rejecting or
		// declining); artifactIds are files the human attached to the
		// answer; content is an accepted form's values.
		onanswer: (
			answer: string,
			note?: string,
			artifactIds?: string[],
			content?: FormContent
		) => void;
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
	/** yes_no: "Request changes…" opens the feedback box. */
	let noteOpen = $state(false);
	/** A form: "Decline…" swaps the form for the reason box. */
	let declining = $state(false);
	/** What `note` held when Decline opened: the reason box shares it, and
	 *  Cancel takes back only the reason. */
	let noteBefore = '';

	// Upload any attached files first, then hand the answer up. A failed
	// upload SURFACES and keeps the draft — it used to be swallowed, leaving
	// the card idle with the answer silently unsent.
	/** The answer in flight, so only its button shows busy. */
	let sending = $state('');
	async function submit(answer: string, content?: FormContent, key = answer) {
		sending = key;
		uploading = true;
		uploadErr = '';
		try {
			const ids = files.length ? (await api.files.upload(files)).map((a) => a.id) : undefined;
			onanswer(answer, note, ids, content);
		} catch (e) {
			uploadErr = errorMessage(e);
		} finally {
			uploading = false;
		}
	}

	// The card renders the ANSWER KIND; the type only tunes the wording (a
	// phase confirmation is a yes/no read as "run it / not now").
	const kind = $derived(ping.answerKind);
	const confirmPhase = $derived(isPhaseConfirm(ping.type));
	// A form's values start at its defaults (a thread selection: every
	// thread picked — unpicking is the deliberate act).
	const shape = $derived(formShape(ping.form));
	const lone = $derived(ping.form?.fields[0]);
	let values = $state(formDefaults(untrack(() => ping.form)));
	const missing = $derived(formMissing(ping.form, values));
	// One field to pick any of: the button says how many.
	const pickCount = $derived(
		ping.form?.fields.length === 1 && lone?.type === 'array'
			? ((values[lone.id] as string[] | undefined)?.length ?? 0)
			: null
	);
	const accept = () => submit('accept', formContent(ping.form, values));
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
	/** A gate over a review document: the page curates it; inline, the card
	 *  can only approve it as written. */
	const cur = $derived(curatable(ping));
	// Giving up at a watcher's debug leaf ends that round, not the run: the
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

	// A reason taken back must not ride along on the answer that follows.
	function cancelNote() {
		noteOpen = false;
		note = '';
	}
	function openDecline() {
		noteBefore = note;
		declining = true;
	}
	function cancelDecline() {
		declining = false;
		note = noteBefore;
	}

	function keys(e: KeyboardEvent, send: () => void) {
		if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
			e.preventDefault();
			send();
		}
	}
</script>

{#if !isPending}
	<!-- Answered / withdrawn: one muted line. -->
	<div class="card-in pc-done" class:withdrawn={status === 'withdrawn'}>
		<span class="hue-{verdict.hue} glyph">{verdict.glyph}</span>
		<span class="ty">{meta.label}</span>
		{#if stageId}<span class="mono faint">{stageId}</span>{/if}
		<span class="truncate grow what">
			{#if status === 'withdrawn'}
				{t('ping.withdrawn')}
			{:else}
				{ping.answeredAt
					? t('ping.answeredAgo', { ago: ago(ping.answeredAt) })
					: t('ping.answered')}
				<b class="hue-{verdict.hue}">{answerSummary(ping)}</b>{#if ping.feedback}<span
						class="faint"
						title={ping.feedback}>{' · '}{ping.feedback}</span
					>{/if}
			{/if}
		</span>
	</div>
{:else}
	<div class="pc attention-card" class:compact>
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
			{#if unwanted}<span class="pill sm tint-danger">{t('ping.unwanted')}</span>{/if}
		</div>
		{#if runTitle || ticketTitle || ticketKey}
			<div class="ctx">
				{#if runTitle}
					{#if runLink}<a href={runLink}>{runTitle}</a>{:else}<span>{runTitle}</span>{/if}
				{/if}
				{#if ticketKey || (ticketTitle && ticketTitle !== runTitle)}
					<span class="faint">·</span>
					{#if ticketKey}<span class="tag">{ticketKey}</span>{/if}
					{#if ticketTitle && ticketTitle !== runTitle}<span class="truncate">{ticketTitle}</span
						>{/if}
				{/if}
			</div>
		{/if}

		<div class="body">
			{#if debug}
				<div class="q">{@html th('ping.debugFailed', { stage: stageId })}</div>
				{#if ping.context}<pre class="log out">{ping.context}</pre>{/if}
			{:else}
				<div class="q"><Markdown src={ping.question} /></div>
				{#if ping.context}<Markdown src={ping.context} quiet />{/if}
			{/if}

			{#if ping.link}
				<!-- Typed at the source (DESIGN-FILES §4): a link payload links out… -->
				<a class="ref" href={ping.link} target="_blank" rel="noreferrer">
					<span class="mono truncate">{ping.link}</span><span>{I.external}</span>
				</a>
			{/if}
			{#if isDocumentPing(ping)}
				<!-- …and files open the review workbench. -->
				<div class="card-in docref">
					<FileChip name={primaryFile(ping)} />
					{#if (ping.files?.length ?? 0) > 1}<span class="faint"
							>{t('ping.moreFiles', { count: ping.files!.length - 1 })}</span
						>{/if}
					<span class="spacer"></span>
					<a class="btn {cur ? 'btn-hot' : 'btn-sel'} btn-sm" href={to.ping(ping.id)}
						>{cur ? t('ping.curate') : t('ping.reviewComment')} {I.forward}</a
					>
				</div>
			{/if}

			{#if kind === 'yes_no' && confirmPhase}
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
			{:else if kind === 'yes_no'}
				{#if noteOpen}
					<textarea
						aria-label={debug
							? endsRound
								? t('ping.debugEndPlaceholder', { phase: phaseName })
								: t('ping.debugReasonPlaceholder')
							: t('ping.changesPlaceholder')}
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
						class="{cur ? 'btn-hot-outline' : 'btn-hot'} btn-md"
						disabled={off}
						aria-busy={busyOn('approve')}
						onclick={() => submit('approve')}
						>{debug
							? t('ping.continueRerun', { stage: stageId ?? '' })
							: cur
								? t('ping.approveAsWritten')
								: t('ping.approve')}</button
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
						<button class="btn-ghost btn-sm" onclick={cancelNote}>{t('common.cancel')}</button>
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
			{:else if declining}
				<textarea
					aria-label={t('ping.declinePlaceholder')}
					bind:value={note}
					rows="3"
					placeholder={t('ping.declinePlaceholder')}
					onkeydown={(e) => keys(e, () => note.trim() && !off && submit('decline'))}></textarea>
				<div class="acts">
					<button
						class="btn-hot-outline btn-md"
						disabled={off || !note.trim()}
						aria-busy={busyOn('decline')}
						onclick={() => submit('decline')}>{t('ping.sendDecline')}</button
					>
					<button class="btn-ghost btn-sm" onclick={cancelDecline}>{t('common.cancel')}</button>
					{@render attachBtn()}
				</div>
			{:else if shape === 'pick' && lone}
				<div class="acts">
					{#each lone.options as opt, i (opt.id)}
						<button
							class="{i === 0 ? 'btn-hot' : 'btn-hot-outline'} btn-md"
							disabled={off}
							aria-busy={busyOn(opt.id)}
							title={opt.detail ?? ''}
							onclick={() => submit('accept', { [lone.id]: opt.id }, opt.id)}>{opt.label}</button
						>
					{/each}
					{@render declineBtn()}
					{@render attachBtn()}
				</div>
			{:else if shape === 'text' && lone}
				<textarea
					aria-label={t('ping.answerPlaceholder')}
					bind:value={values[lone.id]}
					rows="3"
					placeholder={t('ping.answerPlaceholder')}
					onkeydown={(e) => keys(e, () => !missing.length && !off && accept())}></textarea>
				<div class="acts">
					<button
						class="btn-hot btn-md"
						disabled={off || missing.length > 0}
						aria-busy={busyOn('accept')}
						onclick={accept}>{t('ping.sendAnswer')}</button
					>
					<span class="help"><span class="kbd">⌘↵</span> {t('ping.sendHint')}</span>
					{@render declineBtn()}
					{@render attachBtn()}
				</div>
			{:else if ping.form}
				<PingFormFields form={ping.form} bind:values disabled={off} />
				<textarea
					aria-label={t('ping.optionalNote')}
					bind:value={note}
					rows="2"
					placeholder={t('ping.optionalNote')}></textarea>
				<div class="acts">
					<button
						class="btn-hot btn-md"
						disabled={off || missing.length > 0}
						aria-busy={busyOn('accept')}
						title={missing.length
							? t('ping.missing', { fields: missing.map((f) => f.title || f.id).join(', ') })
							: ''}
						onclick={accept}
						>{pickCount === null
							? t('ping.sendAnswer')
							: pickCount
								? t('ping.confirmSelected', { count: pickCount })
								: t('ping.selectNone')}</button
					>
					{@render declineBtn()}
					{@render attachBtn()}
				</div>
			{/if}

			{#if showAttach || files.length}
				<AttachFiles bind:files label={t('ping.attachToAnswer')} compact />
			{/if}
			{#if uploadErr}<div class="error">{t('ping.uploadFailed', { error: uploadErr })}</div>{/if}
		</div>
	</div>
{/if}

{#snippet declineBtn()}
	<button
		class="btn-dashed btn-md"
		disabled={off}
		title={t('ping.declineTitle')}
		onclick={openDecline}>{t('ping.decline')}</button
	>
{/snippet}

{#snippet attachBtn()}
	<span class="spacer"></span>
	<button class="btn-ghost btn-sm" onclick={() => (showAttach = !showAttach)}
		>{I.add}
		{t('ping.attach')}{#if files.length}<span class="mono">&nbsp;{files.length}</span>{/if}</button
	>
{/snippet}

<style>
	/* Solid sky (.attention-card): a human must act now. */
	.pc {
		border-radius: var(--r-float);
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	/* The compact form (DESIGN 4.6): the window's card. */
	.pc.compact {
		border-radius: var(--r-btn);
	}
	.compact .ty {
		font-family: var(--font-body);
		font-size: var(--fs-meta);
		font-weight: 700;
		letter-spacing: 0.04em;
		text-transform: uppercase;
	}
	.compact .q {
		font-size: var(--fs-read);
		font-weight: 600;
		line-height: 1.3;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 11px 14px 2px;
		flex-wrap: wrap;
		row-gap: 4px;
	}
	.ty {
		font-family: var(--font-display);
		font-size: var(--fs-section);
		line-height: 1.1;
	}
	.aside {
		font-family: var(--font-display);
		font-weight: 600;
		font-size: var(--fs-aside);
		color: var(--text-muted);
	}
	.when {
		font-size: var(--fs-help);
		color: var(--text-muted);
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
		color: var(--text-muted);
		min-width: 0;
		white-space: nowrap;
	}
	.ctx a {
		color: var(--text);
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
		max-height: 240px;
		word-break: break-word;
	}
	.ref {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		align-self: flex-start;
		max-width: 100%;
		color: var(--attention-text);
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
	/* answered / withdrawn */
	.pc-done {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 12px;
		color: var(--text-muted);
		font-size: var(--fs-label);
		min-width: 0;
		white-space: nowrap;
	}
	.pc-done .glyph {
		font-weight: 700;
	}
	.pc-done .ty {
		font-size: var(--fs-lead);
		color: var(--text);
	}
	.pc-done .mono {
		font-size: var(--fs-meta);
	}
	.pc-done.withdrawn,
	.pc-done.withdrawn .glyph {
		color: var(--text-faint);
	}
	.pc-done.withdrawn .what,
	.pc-done.withdrawn .ty {
		text-decoration: line-through;
		color: var(--text-faint);
	}
	@media (max-width: 760px) {
		.body,
		.ctx {
			padding-left: 14px;
		}
	}
</style>
