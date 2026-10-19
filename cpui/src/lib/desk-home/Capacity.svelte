<script lang="ts">
	// Capacity strip (DESIGN 7.3): concurrent runs, environments and the FIFO
	// queue, against the guild's limits. Reads the shared fleet poll.
	import { t } from '$lib/i18n/index.svelte';
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
			label={t('deskhome.cap.runs')}
			value={fleet.capacity.runs.used}
			max={fleet.capacity.runs.max}
			sub={t('deskhome.cap.runsSub')}
		/>
	</div>
	<div class="card-in tile">
		<Meter
			label={t('deskhome.cap.envs')}
			value={fleet.capacity.envs.used}
			max={fleet.capacity.envs.max}
			tone="grn"
			sub={t('deskhome.cap.envsSub')}
		/>
	</div>
	<div class="card-in tile q">
		<div class="row spread nowrap">
			<span>{t('deskhome.cap.queue')}</span><span class="mono tx"
				>{t('deskhome.cap.pending', { count: fleet.capacity.queued })}</span
			>
		</div>
		<div class="sub ell">
			{#if next}{t('deskhome.cap.next')} <a href={to.run(next.id)}>{next.title}</a> · FIFO{:else}{t(
					'deskhome.cap.none'
				)}{/if}
		</div>
	</div>
	<div class="end">
		<a href={to.system('limits')}>{t('deskhome.cap.limits')} {I.forward}</a>
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
