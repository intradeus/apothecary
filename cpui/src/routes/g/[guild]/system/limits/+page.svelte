<script lang="ts">
	// Limits (DESIGN 7.10): concurrency and environment caps.
	import { fleet } from '$lib/fleet.svelte';
	import SettingsFrame from '$lib/system/SettingsFrame.svelte';
	import SettingRow from '$lib/ui/SettingRow.svelte';
	import Stepper from '$lib/ui/Stepper.svelte';
	import { plural } from '$lib/format';
</script>

<SettingsFrame>
	{#snippet children(s)}
		<section class="c pane">
			<div class="pane-head">
				<h2>Capacity</h2>
				<span class="mut small">checked on every admission decision</span>
			</div>
			<SettingRow
				label="Max concurrent runs"
				forId="s-conc"
				help="Runs actively working at once. Beyond this, runs queue as pending, first in first out. A run waiting on a human or paused gives its slot back."
			>
				{#snippet note()}
					Now: {fleet.slots.length} running{#if fleet.queued.length}
						· {plural(fleet.queued.length, 'run')} queued{/if}
				{/snippet}
				<Stepper id="s-conc" label="Max concurrent runs" bind:value={s.maxConcurrentRuns} />
			</SettingRow>
			<SettingRow
				label="Max environments"
				forId="s-envs"
				help="Workspaces that may exist at once, live or stopped. At the cap, new runs are refused until a run is archived or reaped."
			>
				{#snippet note()}Now: {plural(fleet.envs.length, 'environment')} standing{/snippet}
				<Stepper id="s-envs" label="Max environments" bind:value={s.maxEnvironments} />
			</SettingRow>
		</section>
	{/snippet}
</SettingsFrame>
