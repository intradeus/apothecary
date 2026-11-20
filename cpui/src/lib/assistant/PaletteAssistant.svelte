<script lang="ts">
	// The assistant with no page, from the command palette: it answers from
	// what it reads and acts only through requests the person confirms. On a
	// workshop's pages it reads that workshop's repositories.
	import { page } from '$app/state';
	import { t } from '$lib/i18n/index.svelte';
	import Sheet from '$lib/ui/Sheet.svelte';
	import AssistantPanel from './AssistantPanel.svelte';
	import { NoFill } from './fillGuard.svelte';

	let { open = $bindable(false) }: { open: boolean } = $props();

	const guard = new NoFill();
	const workshop = $derived(page.params.workshop ?? '');
	const pageState = () => ({ page: page.url.pathname });
</script>

<Sheet bind:open side="right" label={t('assistant.palette')}>
	<div class="pa">
		{#key workshop}
			<AssistantPanel kind="general" subject="palette" {workshop} {guard} {pageState} />
		{/key}
	</div>
</Sheet>

<style>
	.pa {
		width: min(440px, 100vw);
		height: 100%;
		display: flex;
		flex-direction: column;
		padding: 12px;
	}
</style>
