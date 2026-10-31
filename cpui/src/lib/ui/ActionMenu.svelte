<script lang="ts">
	// A page's main action when there are several kinds to start from (a new
	// integration, a new provider): one button, the kinds in a menu under it.
	import Popover from './Popover.svelte';
	import { I } from './icons';

	let {
		label,
		groups
	}: {
		label: string;
		groups: { title?: string; items: { label: string; run: () => void }[] }[];
	} = $props();

	let anchor = $state<HTMLButtonElement | null>(null);
	let open = $state(false);
</script>

<button
	bind:this={anchor}
	class="btn-primary btn-lg"
	aria-haspopup="menu"
	aria-expanded={open}
	onclick={() => (open = !open)}>{I.add} {label}</button
>

<Popover {anchor} bind:open placement="below-end" width={260} role="menu" portal flip>
	{#each groups.filter((g) => g.items.length) as g, i (g.title ?? i)}
		{#if i}<div class="menu-div"></div>{/if}
		{#if g.title}<div class="gt">{g.title}</div>{/if}
		{#each g.items as it (it.label)}
			<button
				class="mi"
				role="menuitem"
				onclick={() => {
					open = false;
					it.run();
				}}>{it.label}</button
			>
		{/each}
	{/each}
</Popover>

<style>
	.gt {
		font-size: var(--fs-meta);
		color: var(--mut);
		padding: 4px 9px 2px;
	}
</style>
