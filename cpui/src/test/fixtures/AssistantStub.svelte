<script lang="ts">
	// The assistant without the engine, for a page's tests: the button that
	// opens its page mode while the page offers itself, and that mode as a
	// region holding what the page gave it, with two buttons a test clicks —
	// one reads the page as a question would send it, one offers a fill
	// through the page's guard as an answer would.
	import { onDestroy } from 'svelte';
	import { assistant } from '$lib/assistant/assistant.svelte';

	const p = $derived(assistant.page);
	let read = $state('');
	let fill = $state('{}');

	onDestroy(() => assistant.close());
</script>

{#if p}
	<button onclick={() => assistant.show('page')}>Fill with the assistant</button>
{/if}
{#if p && assistant.open && assistant.mode === 'page'}
	<section
		aria-label="Fill assistant"
		data-kind={p.kind}
		data-subject={p.subject}
		data-workshop={p.workshop ?? ''}
		data-name={p.name}
	>
		{@render p.workshopPicker?.()}
		<button onclick={() => (read = JSON.stringify(p.pageState()))}>read the page</button>
		<output aria-label="page state">{read}</output>
		<textarea aria-label="fill" bind:value={fill}></textarea>
		<button
			onclick={() => {
				p.guard.ask();
				p.guard.offer(JSON.parse(fill));
				p.guard.answered();
			}}>offer the fill</button
		>
		{@render p.handoff?.()}
		<button onclick={() => assistant.close()}>close the assistant</button>
	</section>
{/if}
