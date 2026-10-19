<script lang="ts">
	// Frame for the Limits / Reaper / Storage sections: loading and error
	// states, the section body (given the shared draft), and the sticky save
	// bar. The bar saves every section's edits at once.
	import type { Snippet } from 'svelte';
	import { onMount } from 'svelte';
	import type { Settings } from '$lib/domain';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import SaveBar from '$lib/ui/SaveBar.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { settingsDraft as sd } from './settings.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let { children }: { children: Snippet<[Settings]> } = $props();

	onMount(() => void sd.load());

	async function save() {
		const before = sd.savedAt;
		await sd.save();
		if (sd.savedAt !== before) toast(t('system.settingsSaved'));
	}
</script>

{#if sd.draft}
	<!-- Enter in a field submits through the bar's Save (the form's only
	     submit button); the click handler does the saving. -->
	<form class="sf" onsubmit={(e) => e.preventDefault()}>
		{@render children(sd.draft)}

		{#if sd.error}<ErrorCard cause={t('system.settingsRefused')} detail={sd.error} />{/if}

		<div class="bar">
			<SaveBar
				dirty={sd.dirty}
				busy={sd.busy}
				savedAt={sd.savedAt}
				note={t('system.applyNote')}
				onsave={save}
				ondiscard={() => sd.discard()}
			/>
		</div>
	</form>
{:else if sd.error}
	<ErrorCard cause={t('system.settingsLoadFail')} detail={sd.error} onretry={() => sd.load()} />
{:else}
	<Skeleton pane lines={4} />
{/if}

<style>
	.sf {
		display: flex;
		flex-direction: column;
		gap: 14px;
		min-height: 100%;
	}
	/* The bar rides the bottom of the section, however short it is (the
	 * wrapper is what sticks; the bar's own offset can't act inside it). */
	.bar {
		position: sticky;
		bottom: 12px;
		margin-top: auto;
	}
</style>
