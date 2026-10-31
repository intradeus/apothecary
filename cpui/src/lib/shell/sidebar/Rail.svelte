<script lang="ts">
	// The collapsed sidebar: the same destinations and workshops as icons, each
	// list with its own bead, then capacity, language and the user card.
	import { fleet } from '$lib/app/fleet.svelte';
	import { prefs } from '$lib/app/prefs.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { NAV, type NavItem } from '$lib/app/nav';
	import { to } from '$lib/core/urls';
	import type { Project } from '$lib/domain';
	import Logo from '$lib/ui/Logo.svelte';
	import Vial from '$lib/ui/Vial.svelte';
	import { I } from '$lib/design/icons';
	import { bump, indicator } from '$lib/ui/motion';
	import ChromeButton from './ChromeButton.svelte';
	import LocaleSelect from './LocaleSelect.svelte';
	import UserCard from './UserCard.svelte';

	let {
		workshops,
		currentWorkshop,
		path,
		count,
		onsearch,
		userBtn = $bindable(null),
		onmenu
	}: {
		workshops: Project[];
		currentWorkshop?: string;
		path: string;
		count: (n: NavItem) => number;
		onsearch: () => void;
		userBtn: HTMLElement | null;
		onmenu: () => void;
	} = $props();
</script>

<Logo />
<ChromeButton
	icon={I.expand}
	size="exp"
	title={t('shell.sidebar.expandTitle')}
	label={t('shell.sidebar.expand')}
	onclick={() => prefs.toggleCollapsed()}
/>
<ChromeButton
	icon={I.search}
	title={t('shell.sidebar.searchTitle')}
	label={t('shell.sidebar.search')}
	onclick={onsearch}
/>
<nav class="rlist on-rail" aria-label={t('shell.sidebar.primary')} use:indicator>
	{#each NAV as n (n.id)}
		{@const c = count(n)}
		<a
			class="rnav"
			class:on={n.match(path)}
			href={n.href()}
			title={n.label}
			aria-label={n.label}
			aria-current={n.match(path) ? 'page' : undefined}
			>{n.icon}{#if n.id === 'inbox'}<span class="count hot sm rbadge" class:gone={!c} use:bump={c}
					>{c}</span
				>{/if}</a
		>
	{/each}
</nav>
<span class="rdiv"></span>
<div class="rlist rws on-rail" use:indicator>
	{#each workshops as p (p.id)}
		{@const pings = fleet.pingsByProject.get(p.id) ?? 0}
		<a
			class="rnav ws"
			class:on={currentWorkshop === p.id}
			href={to.workshop(p.id)}
			title={fleet.projectName(p.id)}
		>
			<Vial projectId={p.id} idle={!fleet.liveByProject.get(p.id)?.length} />
			<span class="count hot sm rbadge" class:gone={!pings} use:bump={pings}>{pings}</span>
		</a>
	{/each}
</div>
<span class="grow"></span>
<span
	class="mono rcap"
	title={t('ui.meter.title', {
		label: t('shell.sidebar.runs'),
		value: fleet.capacity.runs.used,
		max: fleet.capacity.runs.max || '–'
	})}>{fleet.capacity.runs.used}/{fleet.capacity.runs.max || '–'}</span
>
<LocaleSelect rail />
<UserCard rail bind:btn={userBtn} {onmenu} />

<style>
	.rlist {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 4px;
		flex: none;
	}
	.rws {
		flex: 0 1 auto;
		overflow: auto;
		min-height: 0;
	}
	.rnav {
		position: relative;
		width: 40px;
		height: 36px;
		border-radius: var(--r-row);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: var(--fs-lead);
		color: var(--text-muted);
		flex: none;
	}
	.rnav.ws {
		height: 34px;
	}
	.rnav:hover {
		background: var(--fill-1);
		text-decoration: none;
		color: var(--text);
	}
	.rnav:active {
		opacity: 0.75;
	}
	.rnav.on {
		color: var(--text);
	}
	.rbadge {
		position: absolute;
		top: 2px;
		right: 1px;
	}
	.gone {
		display: none;
	}
	.rdiv {
		flex: none;
		width: 24px;
		height: 1px;
		margin: 6px 0;
		background: var(--line);
	}
	.count {
		color: var(--text-muted);
	}
	.count.hot {
		color: var(--on-brand);
	}
	.rcap {
		font-size: var(--fs-id);
		color: var(--text-faint);
		margin-bottom: 4px;
	}
</style>
