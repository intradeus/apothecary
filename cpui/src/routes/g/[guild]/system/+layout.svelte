<script lang="ts">
	// The System hub (DESIGN 7.10): guild-level integrations, channels,
	// members and engine policy. A section nav on the left, the section on the
	// right.
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { currentGuild, to, type SystemSection } from '$lib/guild';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import SideNav from '$lib/ui/SideNav.svelte';
	import { I } from '$lib/ui/icons';

	let { children }: { children: Snippet } = $props();

	const SECTIONS: {
		id: SystemSection;
		label: string;
		icon: string;
		title: string;
		accent: string;
		sub: string;
	}[] = [
		{
			id: 'integrations',
			label: 'Integrations',
			icon: I.integrations,
			title: 'Integrations',
			accent: 'the guild trusts',
			sub: 'Credentialed connections: forges, provisioners, harnesses and trackers. Secrets are write-only — stored encrypted, shown only as a mask.'
		},
		{
			id: 'channels',
			label: 'Channels',
			icon: I.channels,
			title: 'Channels',
			accent: 'that echo the inbox',
			sub: 'One-way mirrors of pings into chat. Pings are always answered in Apothecary.'
		},
		{
			id: 'members',
			label: 'Members & roles',
			icon: I.members,
			title: 'Members',
			accent: '& their roles',
			sub: 'Who belongs here and what they may do.'
		},
		{
			id: 'limits',
			label: 'Limits',
			icon: I.limits,
			title: 'Limits',
			accent: 'on the fire',
			sub: 'How many runs work at once, and how many workspaces may stand.'
		},
		{
			id: 'reaper',
			label: 'Reaper',
			icon: I.reaper,
			title: 'The reaper',
			accent: 'sweeps the bench',
			sub: 'When finished runs give their workspaces back.'
		},
		{
			id: 'storage',
			label: 'Storage',
			icon: I.storage,
			title: 'Storage',
			accent: '& retention',
			sub: 'How long uploads and snapshot content are kept.'
		}
	];

	const current = $derived(
		SECTIONS.find((s) => page.url.pathname.endsWith(`/system/${s.id}`)) ?? SECTIONS[0]
	);
	const guild = $derived(currentGuild());
</script>

<PageHeader
	crumbs={[
		{ label: guild.name, href: to.desk() },
		{ label: 'System', href: to.system() },
		{ label: current.label }
	]}
	title={current.title}
	accent={current.accent}
>
	{#snippet meta()}<span>{current.sub}</span>{/snippet}
</PageHeader>

<div class="sys">
	<SideNav
		title="System"
		label="System sections"
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
