<script lang="ts">
	// Logs (DESIGN §6): the workspace's live exec output, the provisioning log
	// (clone + build + bootstrap) and each stage's captured log.
	import { tick } from 'svelte';
	import { api } from '$lib/api';
	import Segmented from '$lib/ui/Segmented.svelte';
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

	async function load(src: string) {
		loading = true;
		try {
			if (src === 'workspace') {
				const t = await api.runs.workspaceLog(rs.runId);
				if (src !== rs.logSource) return;
				text = t;
				note = t === null ? 'No live output here — this provisioner does not stream it.' : '';
			} else {
				const t = await api.runs.stageLog(rs.runId, src);
				if (src !== rs.logSource) return;
				text = t;
				note = '';
			}
		} catch (e) {
			if (src !== rs.logSource) return;
			text = null;
			note = `No log: ${e instanceof Error ? e.message : String(e)}`;
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
		const t = setInterval(() => !document.hidden && load(src), 2500);
		return () => clearInterval(t);
	});

	function onScroll() {
		if (pre) stick = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
	}

	const hue = (l: string) =>
		/\b(DENY|denied|error|fatal|panic|failed)\b/i.test(l)
			? 'ros'
			: /\btunnel\b/i.test(l)
				? 'tea'
				: /\bharness\b/i.test(l)
					? 'az'
					: /\bshim\b/i.test(l)
						? 'vio'
						: '';
	const lines = $derived((text ?? '').replace(/\n$/, '').split('\n'));
</script>

<div class="lv">
	<div class="bar">
		<Segmented
			glass={false}
			size="sm"
			value={kind}
			onchange={(v) =>
				(rs.logSource =
					v === 'workspace'
						? 'workspace'
						: v === 'provision'
							? PROVISION_STAGE
							: (stages.at(-1) ?? rs.logSource))}
			options={[
				{ value: 'workspace', label: rs.terminal ? 'Workspace' : 'Workspace · live' },
				...(hasProvision ? [{ value: 'provision' as const, label: 'Provisioning' }] : []),
				...(stages.length ? [{ value: 'stage' as const, label: 'Stage' }] : [])
			]}
		/>
		{#if kind === 'stage'}
			<select class="sel mono" bind:value={rs.logSource} aria-label="Stage">
				{#each stages as s (s)}<option value={s}>{s}</option>{/each}
			</select>
		{/if}
		<span class="sp"></span>
		{#if kind !== 'workspace' || rs.terminal}
			<button class="btn-icon" title="Reload" onclick={() => load(rs.logSource)}>↻</button>
		{:else}
			<span class="hint"><span class="d pulse"></span>tail -f</span>
		{/if}
	</div>
	<div class="out" bind:this={pre} onscroll={onScroll}>
		{#if text === null}
			<div class="msg">
				{#if loading}<span class="d pulse"></span>Connecting…{:else}{note || 'Nothing yet.'}{/if}
			</div>
		{:else if !text}
			<div class="msg">
				{kind === 'workspace'
					? 'No live output yet — it appears here as the workspace works (bootstrap, commands, agent events).'
					: 'The log is empty.'}
			</div>
		{:else}
			{#each lines as l, i (i)}<div class="l {hue(l)}">{l || ' '}</div>{/each}
		{/if}
	</div>
</div>

<style>
	.lv {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	.bar {
		flex: none;
		display: flex;
		gap: 6px;
		align-items: center;
		padding: 8px 12px;
	}
	.sel {
		width: auto;
		padding: 3px 26px 3px 8px;
		font-size: 11.5px;
		border-radius: 8px;
	}
	.sp {
		flex: 1;
	}
	.hint {
		font-size: 11px;
		color: var(--fnt);
		display: flex;
		gap: 6px;
		align-items: center;
	}
	.d {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--az);
		flex: none;
	}
	.out {
		flex: 1;
		min-height: 0;
		overflow: auto;
		margin: 0 8px 8px;
		padding: 10px 12px;
		border-radius: 13px;
		background: var(--code);
		font-family: var(--mono);
		font-size: 11px;
		line-height: 1.75;
		color: var(--mut);
	}
	.l {
		white-space: pre-wrap;
		word-break: break-all;
	}
	.l.ros {
		color: var(--ros);
	}
	.l.tea {
		color: var(--tea);
	}
	.l.az {
		color: var(--az);
	}
	.l.vio {
		color: var(--vio);
	}
	.msg {
		font-family: var(--sans);
		font-size: 12px;
		display: flex;
		gap: 8px;
		align-items: center;
	}
</style>
