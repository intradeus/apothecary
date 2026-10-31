<script lang="ts">
	// One numbered section of the workshop form: the anchor the step rail
	// scrolls to and watches (`section[id]`), its head, its body of FormSubs.
	import type { Snippet } from 'svelte';

	let {
		id,
		step,
		title,
		lead,
		children
	}: {
		id: string;
		step: number;
		title: string;
		/** The line beside the title: a snippet when it carries markup. */
		lead: Snippet | string;
		children: Snippet;
	} = $props();
</script>

<section {id} class="glass pane">
	<div class="pane-head">
		<span class="tag">{step}</span>
		<h2>{title}</h2>
		<span class="muted small"
			>{#if typeof lead === 'string'}{lead}{:else}{@render lead()}{/if}</span
		>
	</div>
	<div class="pane-body stack">{@render children()}</div>
</section>

<style>
	section {
		scroll-margin-top: 8px;
	}
	.pane-head {
		flex-wrap: wrap;
	}
	.stack {
		display: flex;
		flex-direction: column;
		gap: 16px;
	}
</style>
