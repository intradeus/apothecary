<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// Run state chip (DESIGN 3.1). The workspace has its own badge: never merge
	// the two axes into one chip.
	import type { RunState } from '$lib/domain';
	import { stateHueClass, stateLabel } from '$lib/format';

	let {
		state,
		/** Show the lore suffix ("· brewing", "· batch spoiled"). */
		suffix = false,
		size = 'md',
		/** Just the dot (8px) with the state in a tooltip, for dense rows. */
		compact = false
	}: { state: RunState; suffix?: boolean; size?: 'sm' | 'md'; compact?: boolean } = $props();

	// DESIGN 3.1: a tinted .pill per state; waiting is the solid sky .t-hot.
	const tone = $derived(
		(
			{
				pending: 't-mut',
				provisioning: 't-emb',
				running: 't-emb',
				waiting_on_human: 't-hot',
				in_review: 't-vio',
				paused: 't-mut',
				completed: 't-grn',
				abandoned: 't-gold',
				failed: 't-ros',
				archived: 'archived'
			} as const
		)[state]
	);
	const live = $derived(
		state === 'running' || state === 'provisioning' || state === 'waiting_on_human'
	);
	const aside = $derived(
		!suffix
			? ''
			: state === 'running'
				? t('ui.status.brewing')
				: state === 'failed'
					? t('ui.status.spoiled')
					: ''
	);
</script>

{#if compact}
	<span
		class="ldot {stateHueClass[state]}"
		class:flat={state === 'archived' || state === 'pending' || state === 'paused'}
		class:pulse={live}
		title={stateLabel[state]}
		role="img"
		aria-label={stateLabel[state]}
	></span>
{:else}
	<span class="pill sc {tone}" class:dense={size === 'sm'} title={state}>
		{#if state === 'completed'}
			<span class="g">✓</span>
		{:else if state === 'failed'}
			<span class="g">✕</span>
		{:else if state === 'paused'}
			<span class="g">❚❚</span>
		{:else if state !== 'archived'}
			<!-- Only the running/provisioning dot glows; waiting's ink dot just pulses. -->
			<span class="ldot" class:flat={tone !== 't-emb'} class:pulse={live}></span>
		{/if}
		{stateLabel[state]}
		{#if aside}<span class="display aside">· {aside}</span>{/if}
	</span>
{/if}

<style>
	/* .pill at DESIGN 3.1's metrics (12px); size sm keeps .pill's 11px. */
	.sc {
		font-size: var(--fs-label);
		line-height: 1.35;
		padding: 3px 11px 3px 9px;
	}
	.sc.dense {
		font-size: var(--fs-meta);
		font-weight: 700;
		padding: 1px 8px;
		gap: 5px;
	}
	.sc.dense .aside {
		font-size: inherit;
	}
	.sc.dense .ldot {
		width: 6px;
		height: 6px;
	}
	.g {
		font-size: var(--fs-id);
	}
	.aside {
		font-size: var(--fs-aside);
		opacity: 0.85;
	}
	.archived {
		background: transparent;
		color: var(--fnt);
		border: 1px dashed var(--ln2);
	}
</style>
