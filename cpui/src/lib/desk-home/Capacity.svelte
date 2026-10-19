<script lang="ts">
	// Capacity strip (DESIGN 7.3): concurrent runs, environments and the FIFO
	// queue, against the guild's limits. Reads the shared fleet poll.
	import { to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import Meter from '$lib/ui/Meter.svelte';
	import Updated from '$lib/ui/Updated.svelte';
	import { I } from '$lib/ui/icons';

	const next = $derived(
		[...fleet.queued].sort((a, b) => a.createdAt.localeCompare(b.createdAt))[0]
	);
</script>

<section class="cap c pane">
	<div class="card-in tile">
		<Meter
			label="Concurrent runs"
			value={fleet.capacity.runs.used}
			max={fleet.capacity.runs.max}
			sub="a run parked on a human gives its slot back"
		/>
	</div>
	<div class="card-in tile">
		<Meter
			label="Environments"
			value={fleet.capacity.envs.used}
			max={fleet.capacity.envs.max}
			tone="grn"
			sub="at the cap, new runs are refused"
		/>
	</div>
	<div class="card-in tile q">
		<div class="row spread nowrap">
			<span>Queue</span><span class="mono tx">{fleet.capacity.queued} pending</span>
		</div>
		<div class="sub ell">
			{#if next}next: <a href={to.run(next.id)}>{next.title}</a> · FIFO{:else}nothing waiting for a
				slot{/if}
		</div>
	</div>
	<div class="end">
		<a href={to.system('limits')}>Limits {I.forward}</a>
		<Updated at={fleet.updatedAt} paused={fleet.paused} />
	</div>
</section>

<style>
	.cap {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr)) auto;
		gap: 6px;
		padding: 8px;
	}
	.tile {
		padding: 9px 12px 10px;
		min-width: 0;
	}
	.q {
		display: flex;
		flex-direction: column;
		gap: 4px;
		font-size: var(--fs-meta);
		color: var(--mut);
	}
	.q .row {
		gap: 8px;
	}
	.q .mono {
		font-size: var(--fs-meta);
	}
	.sub {
		color: var(--fnt);
		font-size: var(--fs-help);
	}
	.end {
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		align-items: flex-end;
		padding: 6px 8px;
		font-size: var(--fs-label);
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
