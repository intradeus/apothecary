<script lang="ts">
	// One run as a compact row inside a card (DESIGN 7.2): a state dot (or a
	// ✓/✕ mark once finished), the title over "state · stage", and a trailing
	// slot (id and age by default; MR for finished runs). The whole row opens
	// the run; links in the trailing slot stay clickable above it.
	import type { Snippet } from 'svelte';
	import type { Run } from '$lib/domain';
	import { to } from '$lib/urls';
	import { age, ago, fmtAbs, shortId, stateHueClass, stateLabel } from '$lib/format';
	import { t } from '$lib/i18n/index.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import StatusMark from '$lib/ui/StatusMark.svelte';
	import MrLink from './MrLink.svelte';

	let {
		run,
		variant = 'live',
		/** Replaces the stage on the second line ("build · attempt 2"). */
		detail,
		trailing
	}: {
		run: Run;
		variant?: 'live' | 'finished';
		detail?: string;
		trailing?: Snippet;
	} = $props();

	const mark = $derived(
		run.state === 'completed' ? 'ok' : run.state === 'failed' ? 'bad' : ('mut' as const)
	);
</script>

<div class="rr rowlink">
	{#if variant === 'finished'}
		<StatusMark tone={mark} size="sm" title={stateLabel[run.state]} />
	{:else}
		<StatusChip state={run.state} compact />
	{/if}
	<span class="mid">
		<a class="t ell stretch" href={to.run(run.id)}>{run.title || t('run.row.untitled')}</a>
		<span class="s ell"
			><span class={stateHueClass[run.state]}>{stateLabel[run.state]}</span>
			{#if variant === 'finished'}
				· <span title={fmtAbs(run.updatedAt)}>{ago(run.updatedAt)}</span>
			{:else if detail ?? run.currentStage}
				· <span class="mono">{detail ?? run.currentStage}</span>
			{/if}</span
		>
	</span>
	<span class="end over-stretch">
		{#if trailing}
			{@render trailing()}
		{:else if variant === 'finished'}
			<MrLink {run} />
		{:else}
			<span class="mono fnt id">{shortId(run.id)}</span>
			<span class="mono fnt" title={fmtAbs(run.updatedAt)}>{age(run.updatedAt)}</span>
		{/if}
	</span>
</div>

<style>
	.rr {
		position: relative;
		display: grid;
		grid-template-columns: 15px minmax(0, 1fr) auto;
		gap: 10px;
		align-items: center;
		padding: 8px 14px;
		border-top: 1px solid var(--cLn);
	}
	.rr:hover {
		background: var(--p1);
	}
	.rr:active {
		background: var(--p3);
	}
	.mid {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}
	.t {
		font-size: var(--fs-ui);
		font-weight: 500;
		color: var(--tx);
	}
	.t:hover {
		text-decoration: none;
	}
	.s {
		font-size: var(--fs-meta);
		color: var(--mut);
	}
	.s .mono {
		font-size: var(--fs-id);
	}
	.end {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 1px;
		font-size: var(--fs-id);
		white-space: nowrap;
	}
	.id {
		font-size: var(--fs-id);
	}
</style>
