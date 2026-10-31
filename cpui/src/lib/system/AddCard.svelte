<script lang="ts">
	// The inline "add" card above a System grid (a new integration, a new
	// channel): a display title, an optional kind chip and head controls, then
	// the form. It grows open on the spring and closes the same way; toggling
	// it mid-flight reverses from where it is.
	import type { Snippet } from 'svelte';
	import { slide } from 'svelte/transition';
	import { ms, spring } from '$lib/ui/motion';

	let {
		title,
		kind,
		head,
		children
	}: {
		title: string;
		kind?: string;
		/** Controls at the right of the head (a kind picker). */
		head?: Snippet;
		children: Snippet;
	} = $props();

	const grow = (node: Element) =>
		slide(node, { duration: ms('--dur-spring', 450), easing: spring });
</script>

<div
	class="ac"
	transition:grow
	onintroend={(e) => e.currentTarget.scrollIntoView({ block: 'nearest' })}
>
	<div class="glass inset">
		<div class="row nowrap">
			<span class="display at">{title}</span>
			{#if kind}<span class="pill sm flat sq tint-muted">{kind}</span>{/if}
			<span class="spacer"></span>
			{#if head}{@render head()}{/if}
		</div>
		{@render children()}
	</div>
</div>

<style>
	/* The outer box is what slides (it clips); the glass card sits inside. */
	.inset {
		border-radius: var(--r-pane);
		border-color: var(--brand-line);
		padding: 14px 16px;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.at {
		font-size: var(--fs-section);
	}
</style>
