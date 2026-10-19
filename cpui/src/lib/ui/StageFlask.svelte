<script lang="ts" module>
	export type StageStatus = 'done' | 'running' | 'waiting' | 'failed' | 'paused' | 'pending';
</script>

<script lang="ts">
	// Stage flask (DESIGN 2.6 / 3.4): the liquid level is the stage's progress.
	let {
		status,
		/** 'sm' = 10×14 for dense rows; default 14×20. */
		size = 'md'
	}: { status: StageStatus; size?: 'sm' | 'md' } = $props();

	const hue = $derived(
		(
			{
				done: 'var(--grn)',
				running: 'var(--az)',
				waiting: 'var(--amb)',
				failed: 'var(--ros)',
				paused: 'var(--mut)',
				pending: 'var(--ln2)'
			} as const
		)[status]
	);
	const level = $derived(
		({ done: 100, running: 55, waiting: 70, failed: 100, paused: 50, pending: 0 } as const)[status]
	);
</script>

<span class="flask {size}" class:pulse={status === 'waiting'} style:--h={hue} title={status}>
	<span class="liquid" class:slosh={status === 'running'} style:height="{level}%"></span>
	{#if status === 'running'}<span class="bubble"></span>{/if}
</span>

<style>
	.flask {
		position: relative;
		flex: none;
		display: inline-block;
		width: 14px;
		height: 20px;
		border-radius: 3px 3px 7px 7px;
		border: 1.5px solid var(--h);
		overflow: hidden;
		box-shadow: 0 0 8px color-mix(in oklch, var(--h) 30%, transparent);
	}
	.flask.sm {
		width: 10px;
		height: 14px;
		border-width: 1px;
		border-radius: 2px 2px 5px 5px;
	}
	.liquid {
		position: absolute;
		left: -2px;
		right: -2px;
		bottom: 0;
		background: color-mix(in oklch, var(--h) 75%, transparent);
	}
	.slosh {
		animation: ap-slosh 2.4s ease-in-out infinite;
	}
	.bubble {
		position: absolute;
		left: 50%;
		bottom: 3px;
		width: 3px;
		height: 3px;
		margin-left: -1.5px;
		border-radius: 50%;
		background: rgba(255, 255, 255, 0.8);
		animation: ap-bubble 1.8s ease-in infinite;
	}
	@media (prefers-reduced-motion: reduce) {
		.slosh,
		.bubble {
			animation: none;
		}
	}
</style>
