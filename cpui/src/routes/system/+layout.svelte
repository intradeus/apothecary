<script lang="ts">
	// The System hub (DESIGN 7.10): one's integrations, channels, account
	// and policy — and, for an admin, the instance's accounts. A section nav
	// on the left, the section on the right.
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { to, type SystemSection } from '$lib/urls';
	import { session } from '$lib/session.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SideNav from '$lib/ui/SideNav.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/ui/icons';

	let { children }: { children: Snippet } = $props();

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
</PageHeader>

<div class="sys">
	<SideNav
		title={t('page.system.crumb')}
		label={t('page.system.sections')}
		active={current.id}
		items={SECTIONS.map((s) => ({ id: s.id, label: s.label, icon: s.icon, href: to.system(s.id) }))}
	/>
	<div class="body">
		<div class="inner">{@render children()}</div>
	</div>
</div>

<style>
	.sys {
		flex: 1;
		min-height: 0;
		display: flex;
		gap: 16px;
		padding: 4px 0 0 22px;
	}
	.body {
		flex: 1;
		min-width: 0;
		overflow: auto;
		padding: 0 22px 22px 0;
	}
	.inner {
		max-width: 980px;
		min-height: 100%;
		display: flex;
		flex-direction: column;
		gap: 16px;
	}
	@media (max-width: 1000px) {
		.sys {
			flex-direction: column;
			padding: 0 16px;
		}
		.body {
			padding: 0 0 22px;
		}
	}
</style>
