<script lang="ts">
	// Skills (TODO 38): one's skill library — skills, skillsets and their
	// pages — under one header. A page's main action rides the header
	// (HeaderAction).
	import type { Snippet } from 'svelte';
	import { to } from '$lib/core/urls';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import { provideHeaderSlot } from '$lib/ui/headerSlot.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let { children }: { children: Snippet } = $props();

	const slot = provideHeaderSlot();
</script>

<PageHeader
	crumbs={[{ label: t('nav.desk'), href: to.desk() }, { label: t('nav.skills') }]}
	title={t('page.skills.title')}
	accent={t('page.skills.accent')}
>
	{#snippet meta()}<span>{t('page.skills.sub')}</span>{/snippet}
	{#snippet actions()}{@render slot.action?.()}{/snippet}
</PageHeader>

<div class="page glass">
	<div class="inner readable">{@render children()}</div>
</div>

<style>
	.inner {
		min-height: 100%;
		display: flex;
		flex-direction: column;
		gap: var(--shell-gap);
	}
</style>
