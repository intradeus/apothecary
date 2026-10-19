<script lang="ts">
	// Frame for the Limits / Reaper / Storage sections: loading and error
	// states, the section body (given the shared draft), and a sticky save
	// bar. The bar saves every section's edits at once.
	import type { Snippet } from 'svelte';
	import { onMount } from 'svelte';
	import type { Settings } from '$lib/domain';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { settingsDraft as sd } from './settings.svelte';

	let { children }: { children: Snippet<[Settings]> } = $props();

	onMount(() => void sd.load());

	let now = $state(Date.now());
	onMount(() => {
		const t = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(t);
	});
	const justSaved = $derived(sd.savedAt !== null && now - sd.savedAt < 4000 && !sd.dirty);
</script>

{#if sd.draft}
	<form
		class="sf"
		onsubmit={(e) => {
			e.preventDefault();
			void sd.save();
		}}
	>
		{@render children(sd.draft)}

		{#if sd.error}<ErrorCard cause="The engine refused these settings" detail={sd.error} />{/if}

		<div class="bar lg">
			<span class="note"><span class="bolt">↯</span> Applies immediately — no restart</span>
			<span class="spacer"></span>
			{#if sd.dirty}
				<span class="unsaved"><span class="d"></span>Unsaved changes</span>
				<button type="button" class="btn-ghost" onclick={() => sd.discard()}>Discard</button>
			{:else if justSaved}
				<span class="ok">✓ Saved</span>
			{/if}
			<button class="btn-primary" type="submit" disabled={!sd.dirty || sd.busy}
				>{sd.busy ? 'Saving…' : 'Save'}</button
			>
		</div>
	</form>
{:else if sd.error}
	<ErrorCard
		cause="Couldn’t load the engine settings"
		detail={sd.error}
		onretry={() => sd.load()}
	/>
{:else}
	<div class="c pane"><div class="pane-body"><Skeleton lines={4} /></div></div>
{/if}

<style>
	.sf {
		display: flex;
		flex-direction: column;
		gap: 14px;
		min-height: 100%;
	}
	.bar {
		position: sticky;
		bottom: 0;
		margin-top: auto;
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 7px 8px 7px 14px;
		border-radius: 14px;
	}
	.note {
		font-size: 12px;
		color: var(--mut);
	}
	.bolt {
		color: var(--grn);
		margin-right: 2px;
	}
	.unsaved {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		color: var(--amb);
	}
	.unsaved .d {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--amb);
		box-shadow: 0 0 8px var(--amb);
	}
	.ok {
		font-size: 12px;
		color: var(--grn);
	}
</style>
