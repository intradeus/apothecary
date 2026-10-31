<script lang="ts">
	// Play, pause and stop, drawn (DESIGN 2.6). As text each comes from whichever
	// font has it, at that font's size, so side by side on buttons they never
	// match. Here they share one box and take the colour of the text.
	let { kind }: { kind: 'play' | 'pause' | 'stop' } = $props();
</script>

<svg viewBox="0 0 12 12" aria-hidden="true">
	{#if kind === 'play'}
		<path d="M3 2.1c0-.7.8-1.2 1.4-.8l6 3.9c.6.4.6 1.2 0 1.6l-6 3.9c-.6.4-1.4-.1-1.4-.8z" />
	{:else if kind === 'pause'}
		<rect x="2" y="2" width="3" height="8" rx="1" />
		<rect x="7" y="2" width="3" height="8" rx="1" />
	{:else}
		<rect x="2" y="2" width="8" height="8" rx="1.5" />
	{/if}
</svg>

<style>
	svg {
		flex: none;
		width: 1em;
		height: 1em;
		fill: currentColor;
	}
</style>
