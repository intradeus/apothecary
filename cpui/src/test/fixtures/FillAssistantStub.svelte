<script lang="ts">
	// FillAssistant without the engine: open or not, what the page gave it,
	// and two buttons a test clicks — one reads the page as the modal would
	// send it, one offers a fill through the page's guard as an answer would.
	import type { Snippet } from 'svelte';
	import type { AssistantKind } from '$lib/domain';
	import type { PageGuard } from '$lib/assistant/fillGuard.svelte';

	let {
		open = $bindable(false),
		kind,
		subject,
		workshop = '',
		label,
		guard,
		pageState,
		workshopPicker,
		handoff
	}: {
		open: boolean;
		kind: AssistantKind;
		subject: string;
		workshop?: string;
		label?: string;
		guard: PageGuard;
		pageState: () => unknown;
		workshopPicker?: Snippet;
		handoff?: Snippet;
	} = $props();

	let read = $state('');
	let fill = $state('{}');
</script>

{#if open}
	<section
		aria-label="Fill assistant"
		data-kind={kind}
		data-subject={subject}
		data-workshop={workshop}
		data-label={label}
	>
		{@render workshopPicker?.()}
		<button onclick={() => (read = JSON.stringify(pageState()))}>read the page</button>
		<output aria-label="page state">{read}</output>
		<textarea aria-label="fill" bind:value={fill}></textarea>
		<button
			onclick={() => {
				guard.ask();
				guard.offer(JSON.parse(fill));
				guard.answered();
			}}>offer the fill</button
		>
		{@render handoff?.()}
		<button onclick={() => (open = false)}>close the assistant</button>
	</section>
{/if}
