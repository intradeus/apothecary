<script lang="ts">
	// Reaper (DESIGN 7.10): when finished runs give their workspaces back.
	import SettingsFrame from '$lib/system/SettingsFrame.svelte';
	import SettingRow from '$lib/system/SettingRow.svelte';
	import DurationInput from '$lib/system/DurationInput.svelte';
	import { durWords } from '$lib/system/duration';
</script>

<SettingsFrame>
	{#snippet children(s)}
		<section class="c pane">
			<div class="pane-head">
				<h2>Workspaces of finished runs</h2>
				<span class="mut small">the run itself is never touched — only its machine</span>
			</div>
			<SettingRow
				label="Destroy completed environments after"
				forId="s-after"
				help="Once a run completes, its workspace stays up this long for a look around, then the reaper destroys it. The run stays completed and its record is kept."
			>
				{#snippet note()}Currently {durWords(s.destroyCompletedAfter)}{/snippet}
				<DurationInput
					id="s-after"
					label="Destroy completed environments after"
					bind:value={s.destroyCompletedAfter}
				/>
			</SettingRow>
			<SettingRow
				label="Destroy abandoned environments"
				forId="s-abandoned"
				help="Abandoned and failed runs are inspection material: their workspaces stay until you archive the run. The engine accepts only “never” today."
			>
				{#snippet note()}Archive a run to destroy its workspace by hand.{/snippet}
				<DurationInput
					id="s-abandoned"
					label="Destroy abandoned environments"
					bind:value={s.destroyAbandoned}
					allowNever
					disabled
				/>
			</SettingRow>
		</section>
	{/snippet}
</SettingsFrame>
