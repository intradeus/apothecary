<script lang="ts">
	import { isLore } from '$lib/lore';
	// The System hub (DESIGN 7.10): guild-level integrations, channels,
	// members and engine policy. A section nav on the left, the section on the
	// right.
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { currentGuild, to, type SystemSection } from '$lib/guild';
	import PageHeader from '$lib/ui/PageHeader.svelte';

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
			icon: '⌁',
			title: 'Integrations',
			accent: 'the guild trusts',
			sub: 'Credentialed connections: forges, provisioners, harnesses and trackers. Secrets are write-only — stored encrypted, shown only as a mask.'
		},
		{
			id: 'channels',
			label: 'Channels',
			icon: '✉',
			title: 'Channels',
			accent: 'that echo the inbox',
			sub: 'One-way mirrors of pings into chat. Pings are always answered in Apothecary.'
		},
		{
			id: 'members',
			label: 'Members & roles',
			icon: '☺',
			title: 'Members',
			accent: '& their roles',
			sub: 'Who belongs here and what they may do.'
		},
		{
			id: 'limits',
			label: 'Limits',
			icon: '▤',
			title: 'Limits',
			accent: 'on the fire',
			sub: 'How many runs work at once, and how many workspaces may stand.'
		},
		{
			id: 'reaper',
			label: 'Reaper',
			icon: '⌛',
			title: 'The reaper',
			accent: 'sweeps the bench',
			sub: 'When finished runs give their workspaces back.'
		},
		{
			id: 'storage',
			label: 'Storage',
			icon: '⛁',
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
	accent={isLore() ? current.accent : undefined}
>
	{#snippet meta()}<span>{current.sub}</span>{/snippet}
</PageHeader>

<div class="sys">
	<nav class="snav lg" aria-label="System sections">
		<div class="sh">System</div>
		{#each SECTIONS as s (s.id)}
			<a
				class="si"
				class:on={s.id === current.id}
				href={to.system(s.id)}
				aria-current={s.id === current.id ? 'page' : undefined}
				><span class="ic">{s.icon}{'︎'}</span>{s.label}</a
			>
		{/each}
	</nav>
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
	.snav {
		flex: none;
		width: 220px;
		align-self: flex-start;
		border-radius: 16px;
		padding: 8px;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.sh {
		font-family: var(--serif);
		font-style: italic;
		font-size: 14px;
		color: var(--mut);
		padding: 4px 10px 6px;
	}
	.si {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 10px;
		border-radius: 10px;
		color: var(--tx);
		font-size: 12.5px;
		font-weight: 500;
		white-space: nowrap;
	}
	.si:hover {
		background: var(--p3);
		text-decoration: none;
	}
	.si.on {
		background: var(--railSel);
		box-shadow:
			inset 0 0 0 1px rgba(140, 230, 175, 0.26),
			0 1px 3px rgba(0, 0, 0, 0.35);
		color: #e6efe8;
		font-weight: 600;
	}
	:global(:root[data-theme='light']) .si.on {
		color: #fff;
	}
	.ic {
		width: 16px;
		text-align: center;
		font-size: 13px;
		color: var(--mut);
	}
	.si.on .ic {
		color: inherit;
		opacity: 0.8;
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
	@media (max-width: 900px) {
		.sys {
			flex-direction: column;
			padding: 0 16px;
		}
		.snav {
			width: 100%;
			flex-direction: row;
			overflow-x: auto;
			align-self: stretch;
		}
		.sh {
			display: none;
		}
		.body {
			padding: 0 0 22px;
		}
	}
</style>
