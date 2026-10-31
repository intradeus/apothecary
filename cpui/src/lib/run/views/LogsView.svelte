<script lang="ts">
	// Logs (DESIGN §6): the workspace's live exec output, the provisioning log
	// (clone + build + bootstrap) and each stage's captured log.
	import { errorMessage } from '$lib/core/errors';
	import { t } from '$lib/i18n/index.svelte';
	import { tick } from 'svelte';
	import { api } from '$lib/api';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Select from '$lib/ui/Select.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import { I } from '$lib/design/icons';
	import { PROVISION_STAGE } from '../derive';
	import type { RunStore } from '../store.svelte';

	let { rs, visible }: { rs: RunStore; visible: boolean } = $props();

	const stages = $derived([
		...new Set(
			rs.events
				.filter((e) => ['stage_completed', 'stage_failed', 'stage_paused'].includes(e.type))
				.map((e) => String(e.payload.stage_id))
		)
	]);
	const hasProvision = $derived(rs.events.some((e) => e.type === 'env_requested'));
	const kind = $derived<'workspace' | 'provision' | 'stage'>(
		rs.logSource === 'workspace'
			? 'workspace'
			: rs.logSource === PROVISION_STAGE
				? 'provision'
				: 'stage'
	);

	let text = $state<string | null>(null);
	let note = $state('');
	let loading = $state(false);
	let pre = $state<HTMLDivElement | null>(null);
	let stick = true;

	async function load(src: string, tailing = false) {
		loading = true;
		try {
			if (src === 'workspace') {
				const out = await api.runs.workspaceLog(rs.runId);
				if (src !== rs.logSource) return;
				text = out;
				note = out === null ? t('runview.logs.no_stream') : '';
			} else {
				const out = await api.runs.stageLog(rs.runId, src);
				if (src !== rs.logSource) return;
				text = out;
				note = '';
			}
		} catch (e) {
			if (src !== rs.logSource) return;
			// One failed poll is no reason to blank what is on screen.
			if (!tailing) text = null;
			note = t('runview.logs.no_log', { message: errorMessage(e) });
		} finally {
			loading = false;
		}
		if (stick) tick().then(() => pre && (pre.scrollTop = pre.scrollHeight));
	}

	// Load on switch and when the window comes on screen; tail the live one.
	$effect(() => {
		const src = rs.logSource;
		if (!visible) return;
		text = null;
		note = '';
		stick = true;
		void load(src);
		if (src !== 'workspace' || rs.terminal || !rs.hasWorkspace) return;
		const timer = setInterval(() => !document.hidden && load(src, true), 2500);
		return () => clearInterval(timer);
	});

	function onScroll() {
		if (pre) stick = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
	}

	// A line's source tints it (DESIGN §6 Logs): failures rose, the tunnel
	// teal, the harness azure, the shim violet.
	const hue = (l: string) =>
		/\b(DENY|denied|error|fatal|panic|failed)\b/i.test(l)
			? 'hue-danger'
			: /\btunnel\b/i.test(l)
				? 'hue-accent-2'
				: /\bharness\b/i.test(l)
					? 'hue-running'
					: /\bshim\b/i.test(l)
						? 'hue-accent'
						: '';
	const lines = $derived((text ?? '').replace(/\n$/, '').split('\n'));
</script>

<div class="view">
	<div class="vbar">
		<Segmented
			glass={false}
			size="sm"
			label={t('runview.logs.source')}
			value={kind}
			onchange={(v) =>
				(rs.logSource =
					v === 'workspace'
						? 'workspace'
						: v === 'provision'
							? PROVISION_STAGE
							: (stages.at(-1) ?? rs.logSource))}
			options={[
				{
					value: 'workspace',
					label: rs.terminal ? t('runview.logs.workspace') : t('runview.logs.workspace_live')
				},
				...(hasProvision
					? [{ value: 'provision' as const, label: t('runview.logs.provisioning') }]
					: []),
				...(stages.length ? [{ value: 'stage' as const, label: t('runview.logs.stage') }] : [])
			]}
		/>
		{#if kind === 'stage'}
			<Select
				size="sm"
				mono
				inline
				label={t('runview.logs.stage')}
				options={stages.map((s) => ({ value: s, label: s }))}
				bind:value={rs.logSource}
			/>
		{/if}
		{#if kind !== 'workspace' || rs.terminal || !rs.hasWorkspace}
			<span class="spacer"></span>
			<button
				class="btn-icon"
				title={t('runview.logs.reload')}
				aria-label={t('runview.logs.reload')}
				aria-busy={loading}
				onclick={() => load(rs.logSource)}>{I.retry}</button
			>
		{:else}
			<span class="hint live"><span class="live-dot sm pulse hue-running"></span>tail -f</span>
		{/if}
	</div>
	<div class="log out" bind:this={pre} onscroll={onScroll}>
		{#if text === null}
			{#if loading}<Loading text={t('runview.connecting')} />{:else}<div class="empty-line">
					{note || t('runview.logs.nothing_yet')}
				</div>{/if}
		{:else if !text}
			<div class="empty-line">
				{kind === 'workspace' ? t('runview.logs.no_live_ws') : t('runview.logs.empty')}
			</div>
		{:else}
			{#each lines as l, i (i)}<div class="l {hue(l)}">{l || ' '}</div>{/each}
		{/if}
	</div>
</div>

<style>
	.live {
		display: flex;
		gap: 6px;
		align-items: center;
	}
	.out {
		flex: 1;
		min-height: 0;
		max-height: none;
		margin: 0 8px 8px;
		line-height: 1.75;
	}
	.out > .empty-line {
		font-family: var(--font-body);
		padding: 0;
	}
	.l {
		white-space: pre-wrap;
		word-break: break-all;
	}
</style>
