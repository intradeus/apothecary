<script lang="ts">
	// The System hub (DESIGN 7.10): one's integrations, channels, account
	// and policy — and, for an admin, the instance's accounts. A section nav
	// on the left, the section on the right.
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { to, type SystemSection } from '$lib/core/urls';
	import { session } from '$lib/app/session.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import { provideHeaderSlot } from '$lib/ui/headerSlot.svelte';
	import SideNav from '$lib/ui/SideNav.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';

	let { children }: { children: Snippet } = $props();

	const slot = provideHeaderSlot();

	type Section = {
		id: SystemSection;
		label: string;
		icon: string;
		title: string;
		accent: string;
		sub: string;
	};
	const section = (id: SystemSection, icon: string): Section => ({
		id,
		icon,
		label: t(`page.system.${id}.label`),
		title: t(`page.system.${id}.title`),
		accent: t(`page.system.${id}.accent`),
		sub: t(`page.system.${id}.sub`)
	});

	const SECTIONS: Section[] = $derived([
		section('providers', I.providers),
		section('integrations', I.integrations),
		section('skills', I.skills),
		section('channels', I.channels),
		section('account', I.members),
		...(session.admin ? [section('users', I.members)] : []),
		section('limits', I.limits),
		section('reaper', I.reaper),
		section('storage', I.storage)
	]);

	// A section's pages may nest (a skill under Skills): the section is the
	// one whose path the page is in, not only the one it ends with.
	const current = $derived(
		SECTIONS.find(
			(s) =>
				page.url.pathname === `/system/${s.id}` || page.url.pathname.startsWith(`/system/${s.id}/`)
		) ?? SECTIONS[0]
	);
</script>

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		{ label: t('page.system.crumb'), href: to.system() },
		{ label: current.label }
	]}
	title={current.title}
	accent={current.accent}
>
	{#snippet meta()}<span>{current.sub}</span>{/snippet}
	{#snippet actions()}{@render slot.action?.()}{/snippet}
</PageHeader>

<div class="sys">
	<SideNav
		fill
		title={t('page.system.crumb')}
		label={t('page.system.sections')}
		active={current.id}
		items={SECTIONS.map((s) => ({ id: s.id, label: s.label, icon: s.icon, href: to.system(s.id) }))}
	/>
	<div class="body glass">
		<div class="inner readable">{@render children()}</div>
	</div>
</div>

<style>
	.sys {
		flex: 1;
		min-height: 0;
		display: flex;
		gap: var(--shell-gap);
		padding: var(--shell-gap);
	}
	.body {
		flex: 1;
		min-width: 0;
		min-height: 0;
		overflow: auto;
		padding: var(--shell-gap);
		border-radius: var(--r-pane);
	}
	.inner {
		min-height: 100%;
		display: flex;
		flex-direction: column;
		gap: var(--shell-gap);
	}
	@media (max-width: 1000px) {
		.sys {
			flex-direction: column;
		}
	}
</style>
