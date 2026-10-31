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
				done: 'var(--success)',
				running: 'var(--running)',
				waiting: 'var(--attention)',
				failed: 'var(--danger)',
				paused: 'var(--text-muted)',
				pending: 'var(--line-strong)'
			} as const
		)[status]
	);
	const level = $derived(
		({ done: 100, running: 55, waiting: 70, failed: 100, paused: 50, pending: 0 } as const)[status]
	);
</script>

<span class="flask {size}" class:pulse={status === 'waiting'} style:--tone={hue} title={status}>
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
		border-radius: var(--r-flask);
		border: 1.5px solid var(--tone);
		overflow: hidden;
		box-shadow: 0 0 8px color-mix(in oklch, var(--tone) 30%, transparent);
	}
	.flask.sm {
		width: 10px;
		height: 14px;
		border-width: 1px;
		border-radius: var(--r-vial);
	}
	.liquid {
		position: absolute;
		left: -2px;
		right: -2px;
		bottom: 0;
		background: color-mix(in oklch, var(--tone) 75%, transparent);
		/* The level rides the spring when the stage's status changes. */
		transition:
			height var(--dur-spring) var(--spring),
			background var(--dur-fade) var(--ease-out);
	}
	.slosh {
		animation: ap-slosh var(--loop-slosh) steps(6, jump-none) infinite;
	}
	.bubble {
		position: absolute;
		left: 50%;
		bottom: 3px;
		width: 3px;
		height: 3px;
		margin-left: -1.5px;
		border-radius: 50%;
		background: var(--bubble);
		animation: ap-bubble var(--loop-bubble) steps(10, jump-none) infinite;
	}
	@media (prefers-reduced-motion: reduce) {
		.slosh,
		.bubble {
			animation: none;
		}
	}
</style>
