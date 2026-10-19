<script lang="ts">
	// The System hub (DESIGN 7.10): guild-level integrations, channels,
	// members and engine policy. A section nav on the left, the section on the
	// right.
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { currentGuild, to, type SystemSection } from '$lib/guild';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SideNav from '$lib/ui/SideNav.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/ui/icons';

	let { children }: { children: Snippet } = $props();

	const SECTIONS: {
		id: SystemSection;
		label: string;
		icon: string;
		title: string;
		accent: string;
		sub: string;
	}[] = $derived([
		{
			id: 'integrations',
			label: t('page.system.integrations.label'),
			icon: I.integrations,
			title: t('page.system.integrations.title'),
			accent: t('page.system.integrations.accent'),
			sub: t('page.system.integrations.sub')
		},
		{
			id: 'channels',
			label: t('page.system.channels.label'),
			icon: I.channels,
			title: t('page.system.channels.title'),
			accent: t('page.system.channels.accent'),
			sub: t('page.system.channels.sub')
		},
		{
			id: 'members',
			label: t('page.system.members.label'),
			icon: I.members,
			title: t('page.system.members.title'),
			accent: t('page.system.members.accent'),
			sub: t('page.system.members.sub')
		},
		{
			id: 'limits',
			label: t('page.system.limits.label'),
			icon: I.limits,
			title: t('page.system.limits.title'),
			accent: t('page.system.limits.accent'),
			sub: t('page.system.limits.sub')
		},
		{
			id: 'reaper',
			label: t('page.system.reaper.label'),
			icon: I.reaper,
			title: t('page.system.reaper.title'),
			accent: t('page.system.reaper.accent'),
			sub: t('page.system.reaper.sub')
		},
		{
			id: 'storage',
			label: t('page.system.storage.label'),
			icon: I.storage,
			title: t('page.system.storage.title'),
			accent: t('page.system.storage.accent'),
			sub: t('page.system.storage.sub')
		}
	]);

	const current = $derived(
		SECTIONS.find((s) => page.url.pathname.endsWith(`/system/${s.id}`)) ?? SECTIONS[0]
	);
	const guild = $derived(currentGuild());
</script>

<PageHeader
	crumbs={[
		{ label: guild.name, href: to.desk() },
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
