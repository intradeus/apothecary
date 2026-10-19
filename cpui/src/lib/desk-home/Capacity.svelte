<script lang="ts">
	// Capacity strip (DESIGN 7.3): concurrent runs, environments and the FIFO
	// queue, against the guild's limits. Reads the shared fleet poll.
	import { to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import Updated from '$lib/ui/Updated.svelte';

	const maxRuns = $derived(fleet.settings?.maxConcurrentRuns ?? 0);
	const maxEnvs = $derived(fleet.settings?.maxEnvironments ?? 0);
	const pct = (n: number, max: number) => (max > 0 ? Math.min(100, (n / max) * 100) : 0);
	const next = $derived(
		[...fleet.queued].sort((a, b) => a.createdAt.localeCompare(b.createdAt))[0]
	);
</script>

<section class="cap c">
	<div class="tile">
		<div class="lbl">Concurrent runs</div>
		<div class="val mono">
			{fleet.slots.length}<span class="fnt">/{maxRuns || '∞'}</span>
		</div>
		<div class="bar">
			<span class="fill brand" style:width="{pct(fleet.slots.length, maxRuns)}%"></span>
		</div>
		<div class="sub">a run parked on a human gives its slot back</div>
	</div>
	<div class="tile">
		<div class="lbl">Environments</div>
		<div class="val mono">
			{fleet.envs.length}<span class="fnt">/{maxEnvs || '∞'}</span>
		</div>
		<div class="bar">
			<span class="fill grn" style:width="{pct(fleet.envs.length, maxEnvs)}%"></span>
		</div>
		<div class="sub">at the cap, new runs are refused</div>
	</div>
	<div class="tile">
		<div class="lbl">Queue</div>
		<div class="val mono">
			{fleet.queued.length}<span class="fnt">{' pending'}</span>
		</div>
		<div class="sub ell">
			{#if next}next: <a href={to.run(next.id)}>{next.title}</a> · FIFO{:else}nothing waiting for a
				slot{/if}
		</div>
	</div>
	<div class="end">
		<a href={to.system('limits')}>Limits →</a>
		<Updated at={fleet.updatedAt} paused={fleet.paused} />
	</div>
</section>

<style>
	.cap {
		border-radius: 16px;
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr)) auto;
		gap: 6px;
		padding: 8px;
		flex: none;
	}
	.tile {
		background: var(--p2);
		border-radius: 13px;
		padding: 8px 12px 9px;
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.lbl {
		font-size: 11px;
		color: var(--mut);
	}
	.val {
		font-size: 15px;
		font-weight: 600;
	}
	.val .fnt {
		font-weight: 500;
		font-size: 12px;
	}
	.bar {
		height: 5px;
		border-radius: 3px;
		background: var(--p1);
		border: 1px solid var(--ln);
		overflow: hidden;
	}
	.fill {
		display: block;
		height: 100%;
		border-radius: 3px;
	}
	.fill.brand {
		background: linear-gradient(
			90deg,
			color-mix(in oklch, var(--brand) 55%, transparent),
			var(--brand)
		);
	}
	.fill.grn {
		background: linear-gradient(
			90deg,
			color-mix(in oklch, var(--grn) 55%, transparent),
			var(--grn)
		);
	}
	.sub {
		font-size: 11px;
		color: var(--fnt);
	}
	.end {
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		align-items: flex-end;
		padding: 6px 8px;
		font-size: 12px;
		gap: 8px;
		white-space: nowrap;
	}
	@media (max-width: 760px) {
		.cap {
			grid-template-columns: minmax(0, 1fr);
		}
		.end {
			flex-direction: row;
			align-items: center;
		}
	}
</style>
