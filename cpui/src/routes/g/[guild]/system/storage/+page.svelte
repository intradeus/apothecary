<script lang="ts">
	// Storage (DESIGN 7.10): upload and snapshot retention.
	import SettingsFrame from '$lib/system/SettingsFrame.svelte';
	import SettingRow from '$lib/system/SettingRow.svelte';
	import DurationInput from '$lib/system/DurationInput.svelte';
	import { durWords } from '$lib/system/duration';
</script>

<SettingsFrame>
	{#snippet children(s)}
		<section class="c pane">
			<div class="pane-head">
				<h2>Retention</h2>
				<span class="mut small">records stay; only bytes go</span>
			</div>
			<SettingRow
				label="Sweep unclaimed uploads after"
				forId="s-orphans"
				help="Files uploaded but never attached to a ticket, a run or a ping answer — an abandoned form, a cancelled import."
			>
				{#snippet note()}Currently {durWords(s.orphanUploadsAfter)}{/snippet}
				<DurationInput
					id="s-orphans"
					label="Sweep unclaimed uploads after"
					bind:value={s.orphanUploadsAfter}
				/>
			</SettingRow>
			<SettingRow
				label="Expire snapshot content after"
				forId="s-snaps"
				help="Snapshots are what a run's Record keeps after its workspace is gone. When content expires, the record (name, hash, size) is kept and only the bytes are freed."
			>
				{#snippet note()}Currently {durWords(s.snapshotsAfter)}{/snippet}
				<DurationInput
					id="s-snaps"
					label="Expire snapshot content after"
					bind:value={s.snapshotsAfter}
					allowNever
				/>
			</SettingRow>
		</section>
	{/snippet}
</SettingsFrame>
