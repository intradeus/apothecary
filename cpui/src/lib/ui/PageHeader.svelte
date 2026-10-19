<script lang="ts">
	// The page header pattern (DESIGN §7): breadcrumb row, h1 with one serif
	// accent phrase and an optional status chip, a meta row, and right-aligned
	// control groups.
	//
	//   <PageHeader crumbs={…} title="Tickets for" accent="the bench">
	//     {#snippet meta()}…{/snippet}
	//     {#snippet actions()}…{/snippet}
	//   </PageHeader>
	import type { Snippet } from 'svelte';
	import Breadcrumbs, { type Crumb } from './Breadcrumbs.svelte';

	let {
		crumbs = [],
		title,
		accent,
		/** Put the accent before the title instead of after it. */
		accentFirst = false,
		lead,
		chip,
		meta,
		actions,
		crumbsEnd,
		children
	}: {
		crumbs?: Crumb[];
		title?: string;
		accent?: string;
		accentFirst?: boolean;
		/** Something left of the title block (the run badge, a vial). */
		lead?: Snippet;
		chip?: Snippet;
		meta?: Snippet;
		actions?: Snippet;
		/** Right end of the breadcrumb row (e.g. the Layouts button). */
		crumbsEnd?: Snippet;
		children?: Snippet;
	} = $props();
</script>

<header class="ph">
	{#if lead}<div class="lead">{@render lead()}</div>{/if}
	<div class="mid">
		{#if crumbs.length || crumbsEnd}
			<div class="crow">
				<Breadcrumbs {crumbs} />
				{#if crumbsEnd}<span class="ce">{@render crumbsEnd()}</span>{/if}
			</div>
		{/if}
		{#if title || accent || chip}
			<div class="trow">
				<h1>
					{#if accentFirst && accent}<span class="accent">{accent}</span>{' '}{/if}{title ??
						''}{#if !accentFirst && accent}{' '}<span class="accent">{accent}</span>{/if}
				</h1>
				{#if chip}{@render chip()}{/if}
			</div>
		{/if}
		{#if meta}<div class="meta">{@render meta()}</div>{/if}
		{#if children}{@render children()}{/if}
	</div>
	{#if actions}<div class="acts">{@render actions()}</div>{/if}
</header>

<style>
	.ph {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		gap: 16px;
		align-items: start;
		padding: 18px 22px 12px;
	}
	.ph:not(:has(.lead)) {
		grid-template-columns: minmax(0, 1fr) auto;
	}
	.mid {
		display: flex;
		flex-direction: column;
		gap: 7px;
		min-width: 0;
	}
	.crow {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
	}
	.ce {
		margin-left: auto;
		flex: none;
	}
	.trow {
		display: flex;
		align-items: center;
		gap: 12px;
		flex-wrap: wrap;
	}
	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 4px 14px;
		font-size: var(--fs-label);
		color: var(--mut);
	}
	.acts {
		display: flex;
		gap: 8px;
		align-items: stretch;
		flex-wrap: wrap;
		justify-content: flex-end;
	}
	@media (max-width: 900px) {
		.ph,
		.ph:not(:has(.lead)) {
			grid-template-columns: minmax(0, 1fr);
		}
		.acts {
			justify-content: flex-start;
		}
	}
</style>
