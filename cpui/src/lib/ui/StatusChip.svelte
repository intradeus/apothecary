<script lang="ts">
	// Run state chip (DESIGN 3.1). The workspace has its own badge: never merge
	// the two axes into one chip.
	import type { RunState } from '$lib/domain';
	import { stateHue, stateLabel } from '$lib/format';

	let {
		state,
		/** Show the lore suffix ("· brewing", "· batch spoiled"). */
		suffix = false,
		size = 'md',
		/** Just the dot (8px) with the state in a tooltip, for dense rows. */
		compact = false
	}: { state: RunState; suffix?: boolean; size?: 'sm' | 'md'; compact?: boolean } = $props();

	const tone = $derived(
		(
			{
				pending: 'pending',
				provisioning: 'az',
				running: 'az',
				waiting_on_human: 'wait',
				in_review: 'vio',
				paused: 'pending',
				completed: 'grn',
				abandoned: 'amb',
				failed: 'ros',
				archived: 'archived'
			} as const
		)[state]
	);
	const live = $derived(
		state === 'running' || state === 'provisioning' || state === 'waiting_on_human'
	);
	const aside = $derived(
		!suffix ? '' : state === 'running' ? 'brewing' : state === 'failed' ? 'batch spoiled' : ''
	);
</script>

{#if compact}
	<span
		class="ldot {state === 'archived' || state === 'pending' || state === 'paused' ? 'flat' : ''}"
		class:pulse={live}
		style:color={stateHue[state]}
		title={stateLabel[state]}
		role="img"
		aria-label={stateLabel[state]}
	></span>
{:else}
	<span class="sc {tone} {size}" title={state}>
		{#if state === 'completed'}
			<span class="g">✓</span>
		{:else if state === 'failed'}
			<span class="g">✕</span>
		{:else if state === 'paused'}
			<span class="g">❚❚</span>
		{:else if state !== 'archived'}
			<span class="d" class:pulse={live}></span>
		{/if}
		{stateLabel[state]}
		{#if aside}<span class="aside">· {aside}</span>{/if}
	</span>
{/if}

<style>
	.sc {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 3px 11px 3px 9px;
		border-radius: var(--r-pill);
		font-size: var(--fs-label);
		font-weight: 600;
		white-space: nowrap;
		border: 1px solid transparent;
		line-height: 1.35;
	}
	.sc.sm {
		font-size: var(--fs-meta);
		padding: 1px 9px 1px 7px;
	}
	.d {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: currentColor;
		flex: none;
	}
	.g {
		font-size: var(--fs-id);
	}
	.aside {
		font-family: var(--serif);
		font-style: italic;
		font-weight: 400;
		font-size: var(--fs-aside);
		opacity: 0.85;
	}
	.pending {
		background: var(--p2);
		color: var(--mut);
		border-color: var(--ln);
	}
	.az {
		background: var(--azBg);
		color: var(--az);
		border-color: var(--azLn);
	}
	.az .d {
		box-shadow: 0 0 8px var(--az);
	}
	.wait {
		background: var(--amb);
		color: var(--ink);
		font-weight: 700;
		box-shadow: 0 0 14px var(--ambLn);
	}
	.vio {
		background: var(--vioBg);
		color: var(--vio);
		border-color: var(--vioLn);
	}
	.grn {
		background: var(--grnBg);
		color: var(--grn);
		border-color: var(--grnLn);
	}
	.amb {
		background: var(--ambBg);
		color: var(--amb);
		border-color: var(--ambLn);
	}
	.ros {
		background: var(--rosBg);
		color: var(--ros);
		border-color: var(--rosLn);
	}
	.archived {
		background: transparent;
		color: var(--fnt);
		border: 1px dashed var(--ln2);
	}
</style>
