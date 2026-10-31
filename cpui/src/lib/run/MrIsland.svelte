<script lang="ts" module>
	import type { MergeRequestLive } from '$lib/domain';
	import type { Hue } from '$lib/design/hues';

	/** One merge request of the run, or a slot its flow will fill. */
	export interface MrRow {
		key: string;
		/** "!20", or the branch's stand-in before there is a number. */
		label: string;
		title: string;
		url?: string;
		live?: MergeRequestLive;
	}

	/** Worst first: what the overflow chip's dot shows for the rest. */
	const RANK: Hue[] = ['danger', 'warning', 'muted', 'attention', 'success'];

	export function mrHue(m: MergeRequestLive | undefined): Hue {
		if (!m || m.error || m.state === 'closed') return 'muted';
		if (m.state === 'merged') return 'attention';
		if (m.mergeable === 'conflicts' || m.checks?.state === 'failure') return 'danger';
		if (m.draft) return 'muted';
		if (m.checks?.state === 'pending') return 'warning';
		return 'success';
	}
	export const worstHue = (rows: MrRow[]): Hue =>
		rows.map((r) => mrHue(r.live)).sort((a, b) => RANK.indexOf(a) - RANK.indexOf(b))[0] ?? 'muted';
</script>

<script lang="ts">
	// The MR island (DESIGN 4.5b): the only top-bar control that expands, on
	// the Dynamic Island pattern. A compact pill names the primary merge
	// request and the worst state of the rest; hover or focus grows the
	// outline out of the pill's own contour into an opaque island over the
	// panes, the content arriving a beat behind it. Leaving (or Esc) pours it
	// back along the same path. The shadow sits on a parent, so it follows the
	// clip-path.
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import MrStatus from './MrStatus.svelte';

	let {
		rows,
		target,
		multiRepo = false
	}: {
		rows: MrRow[];
		/** "ledger-api → main": where they land. */
		target?: string;
		multiRepo?: boolean;
	} = $props();

	const opened = $derived(rows.filter((r) => r.url));
	const primary = $derived(opened[0]);
	const rest = $derived(rows.slice(1));
	const conflicts = $derived(rows.filter((r) => r.live?.mergeable === 'conflicts').length);

	let phase = $state<'shut' | 'open' | 'closing'>('shut');
	let pw = $state(0);
	let timer: ReturnType<typeof setTimeout> | undefined;

	function show() {
		if (!opened.length) return;
		clearTimeout(timer);
		phase = 'open';
	}
	function hide() {
		if (phase !== 'open') return;
		clearTimeout(timer);
		phase = 'closing';
		timer = setTimeout(() => (phase = 'shut'), 330);
	}
	$effect(() => {
		if (phase !== 'open') return;
		const key = (e: KeyboardEvent) => e.key === 'Escape' && hide();
		window.addEventListener('keydown', key);
		return () => window.removeEventListener('keydown', key);
	});
</script>

{#if !opened.length}
	<span class="pill-mr none" title={rows[0]?.title}>{t('run.mr.none')}</span>
{:else}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="pill-mr"
		bind:clientWidth={pw}
		onmouseenter={show}
		onmouseleave={hide}
		onfocusin={show}
		onfocusout={(e) => !e.currentTarget.contains(e.relatedTarget as Node) && hide()}
	>
		<span class="lb">{t(rows.length > 1 ? 'run.mr.requests' : 'run.mr.title')}</span>
		<a class="chip" href={primary.url} target="_blank" rel="noreferrer" title={primary.title}
			>{primary.label} {I.external}</a
		>
		{#if rest.length}
			<span class="more"
				><span class="live-dot sm flat hue-{worstHue(rest)}"></span>+{rest.length}</span
			>
		{/if}

		{#if phase !== 'shut'}
			<div class="shadow">
				<div class="mr-panel" class:closing={phase === 'closing'} style:--pw="{pw}px">
					<div class="content">
						<div class="head">
							<b>{t('run.mr.requests')}</b>
							<span class="n">{rows.length}</span>
							{#if conflicts}<span class="hue-danger conflict"
									>{t('run.mr.conflicts', { count: conflicts })}</span
								>{/if}
							{#if target}<span class="target">{target}</span>{/if}
						</div>
						<div class="list">
							{#each rows as r, i (r.key)}
								{@const hue = mrHue(r.live)}
								<div class="row" class:first={i === 0}>
									<span class="live-dot flat hue-{hue}" class:hollow={!r.url}></span>
									<span class="num">{r.label}</span>
									<span class="what">
										<span class="truncate">{r.title}</span>
										{#if r.live}
											<span class="st"><MrStatus mr={r.live} repo={multiRepo} /></span>
										{:else if !r.url}
											<span class="st faint">{t('run.mr.notYet')}</span>
										{/if}
									</span>
									{#if r.url}
										{#if i === 0}
											<a class="btn btn-primary" href={r.url} target="_blank" rel="noreferrer"
												>{t('run.mr.open')} {I.external}</a
											>
										{:else if r.live?.mergeable === 'conflicts'}
											<a class="btn btn-primary" href={r.url} target="_blank" rel="noreferrer"
												>{t('run.mr.resolve')}</a
											>
										{:else}
											<a
												class="btn go"
												href={r.url}
												target="_blank"
												rel="noreferrer"
												aria-label={t('run.mr.open')}>{I.external}</a
											>
										{/if}
									{/if}
								</div>
							{/each}
						</div>
					</div>
				</div>
			</div>
		{/if}
	</div>
{/if}

<style>
	.pill-mr {
		position: relative;
		flex: none;
		height: 40px;
		min-width: 88px;
		align-self: center;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		padding: 0 6px 0 12px;
		border-radius: var(--r-float);
		background: var(--inset-bg);
		border: 1px solid var(--line-strong);
		white-space: nowrap;
	}
	.pill-mr.none {
		border-style: dashed;
		padding: 0 14px;
		color: var(--text-faint);
		font-size: var(--fs-help);
	}
	.lb {
		font-size: var(--fs-help);
		color: var(--text-muted);
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 3px 10px;
		border-radius: var(--r-pill);
		background: var(--brand);
		color: var(--on-brand);
		font-family: var(--font-code);
		font-size: var(--fs-help);
		font-weight: 700;
		max-width: 120px;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.chip:hover {
		text-decoration: none;
		filter: brightness(1.06);
	}
	.more {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 3px 9px;
		border-radius: var(--r-pill);
		border: 1px solid var(--line-strong);
		font-size: var(--fs-help);
		font-weight: 600;
		color: var(--brand-light);
	}
	.shadow {
		position: absolute;
		top: -6px;
		right: -6px;
		z-index: var(--z-pop);
		width: min(420px, calc(100vw - 32px));
		filter: var(--island-drop);
	}
	.mr-panel {
		border-radius: var(--r-island);
		background: var(--island-bg);
		border: 1px solid var(--island-line);
		box-shadow: var(--specular);
		clip-path: inset(0 0 0 0 round var(--r-island));
		will-change: clip-path;
		animation: es-morph-in var(--dur-morph-in) var(--ease-island) both;
	}
	.mr-panel.closing {
		animation: es-morph-out var(--dur-morph-out) var(--ease-island) forwards;
	}
	.content {
		padding: 12px 14px 14px;
		display: flex;
		flex-direction: column;
		gap: 10px;
		will-change: opacity, transform, filter;
		animation: es-content-in var(--dur-morph-in) var(--ease-island) both;
	}
	.closing .content {
		animation: es-content-out var(--dur-settle-out) ease-out forwards;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: var(--fs-label);
		color: var(--text-muted);
		white-space: nowrap;
	}
	.head b {
		color: var(--text);
		font-size: var(--fs-read);
	}
	.n {
		padding: 0 6px;
		border-radius: var(--r-tag);
		background: var(--selected-bg);
		color: var(--brand-light);
		font-weight: 700;
		font-size: var(--fs-id);
	}
	.conflict {
		font-weight: 600;
	}
	.target {
		margin-left: auto;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.list {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.row {
		display: grid;
		grid-template-columns: auto 52px minmax(0, 1fr) auto;
		gap: 10px;
		align-items: center;
		padding: 7px 9px;
		border-radius: var(--r-row);
		border: 1px solid transparent;
		font-size: var(--fs-label);
		white-space: nowrap;
		background: var(--fill-1);
	}
	.row.first {
		background: var(--fill-3);
		border-color: var(--brand-line);
	}
	.num {
		font-family: var(--font-code);
		font-weight: 700;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.what {
		display: flex;
		flex-direction: column;
		min-width: 0;
		line-height: 1.3;
	}
	.st {
		font-size: var(--fs-meta);
		color: var(--text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.go {
		padding: 4px 9px;
		color: var(--brand-light);
	}
	@media (prefers-reduced-motion: reduce) {
		.mr-panel,
		.mr-panel.closing,
		.content,
		.closing .content {
			animation: none;
		}
	}
	/* The MR island: the outline grows out of the pill along its contour, the
	 * content arrives a beat behind it, and closing pours back the same way. */
	@keyframes es-morph-in {
		from {
			clip-path: inset(6px 6px calc(100% - 46px) calc(100% - var(--pw) - 6px) round var(--r-float));
		}
		to {
			clip-path: inset(0 0 0 0 round var(--r-island));
		}
	}
	@keyframes es-morph-out {
		from {
			clip-path: inset(0 0 0 0 round var(--r-island));
		}
		to {
			clip-path: inset(6px 6px calc(100% - 46px) calc(100% - var(--pw) - 6px) round var(--r-float));
		}
	}
	@keyframes es-content-in {
		0%,
		25% {
			opacity: 0;
			transform: translateY(-6px) scale(0.98);
			filter: blur(6px);
		}
		100% {
			opacity: 1;
			transform: none;
			filter: blur(0);
		}
	}
	@keyframes es-content-out {
		0% {
			opacity: 1;
			filter: blur(0);
		}
		55%,
		100% {
			opacity: 0;
			filter: blur(5px);
		}
	}
</style>
