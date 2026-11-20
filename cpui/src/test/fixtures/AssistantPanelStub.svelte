<script lang="ts">
	// AssistantPanel without the engine: what a page gave it, and two buttons
	// a test clicks — one reads the page as the panel would send it, one
	// offers a fill through the page's guard as a fill frame would.
	import { onMount } from 'svelte';
	import type { AssistantKind } from '$lib/domain';
	import type { PageGuard } from '$lib/assistant/fillGuard.svelte';

	let {
		kind,
		subject,
		workshop = '',
		guard,
		pageState,
		onsession
	}: {
		kind: AssistantKind;
		subject: string;
		workshop?: string;
		guard: PageGuard;
		pageState: () => unknown;
		onsession?: (id: string) => void;
	} = $props();

	onMount(() => onsession?.('s1'));

	let read = $state('');
	let fill = $state('{}');
</script>

<section aria-label="Assistant" data-kind={kind} data-subject={subject} data-workshop={workshop}>
	<button onclick={() => (read = JSON.stringify(pageState()))}>read the page</button>
	<output aria-label="page state">{read}</output>
	<textarea aria-label="fill" bind:value={fill}></textarea>
	<button
		onclick={() => {
			guard.ask();
			guard.offer(JSON.parse(fill));
		}}>offer the fill</button
	>
	<button onclick={() => guard.revert()}>undo the fill</button>
	<output aria-label="changed">{guard.changed.join(',')}</output>
</section>
