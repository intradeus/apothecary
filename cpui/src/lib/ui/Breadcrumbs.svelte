<script lang="ts" module>
	export interface Crumb {
		label: string;
		href?: string;
		/** Ids, keys and hashes render in mono. */
		mono?: boolean;
	}
</script>

<script lang="ts">
	// Guild / Workshop / Ticket key / Run id (DESIGN 5.2). The last crumb is
	// the current page.
	let { crumbs }: { crumbs: Crumb[] } = $props();
</script>

<nav class="bc" aria-label="Breadcrumb">
	{#each crumbs as c, i (i)}
		{#if i > 0}<span class="sep">/</span>{/if}
		{#if i === crumbs.length - 1}
			<span class="cur" class:mono={c.mono} aria-current="page">{c.label}</span>
		{:else if c.href}
			<a href={c.href} class:mono={c.mono}>{c.label}</a>
		{:else}
			<span class:mono={c.mono}>{c.label}</span>
		{/if}
	{/each}
</nav>

<style>
	.bc {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		color: var(--mut);
		white-space: nowrap;
		overflow: hidden;
		min-width: 0;
	}
	.bc > * {
		overflow: hidden;
		text-overflow: ellipsis;
		flex: none;
		max-width: 28ch;
	}
	.sep {
		color: var(--fnt);
	}
	a:hover {
		color: var(--tx);
	}
	.cur {
		color: var(--tx);
	}
	.mono {
		font-size: 11.5px;
	}
</style>
