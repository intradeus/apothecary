<script lang="ts">
	// The sidebar (DESIGN §5): brand, search, nav, workshops (the current one
	// expanded into its live runs), capacity and the user card — the person,
	// whose menu holds the account page, the admin's users page and sign-out,
	// and the theme. Window glass in every theme. Collapses to a rail.
	//
	// Collapsing is one surface changing shape, not a swap: the aside's width
	// rides the spring while the full and rail contents cross-fade. Each
	// layer keeps its own width and is clipped by the aside, so nothing inside
	// reflows mid-flight; flipping it again retargets from wherever it is.
	// Selection slides: one bead springs between nav items and workshops.
	import { fade } from 'svelte/transition';
	import { page } from '$app/state';
	import { fleet } from '$lib/app/fleet.svelte';
	import { prefs } from '$lib/app/prefs.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import type { NavItem } from '$lib/app/nav';
	import Meter from '$lib/ui/Meter.svelte';
	import { ms } from '$lib/ui/motion';
	import LocaleSelect from './sidebar/LocaleSelect.svelte';
	import NavList from './sidebar/NavList.svelte';
	import Rail from './sidebar/Rail.svelte';
	import SidebarHead from './sidebar/SidebarHead.svelte';
	import UserCard from './sidebar/UserCard.svelte';
	import UserMenu from './sidebar/UserMenu.svelte';
	import WorkshopTree from './sidebar/WorkshopTree.svelte';

	let {
		onsearch,
		/** Mobile drawer mode: always full width, closes on navigation. */
		drawer = false,
		onnavigate
	}: { onsearch: () => void; drawer?: boolean; onnavigate?: () => void } = $props();

	const rail = $derived(prefs.collapsed && !drawer);
	const xfade = { duration: ms('--dur-fade', 160) };

	let menuOpen = $state(false);
	let userBtnFull = $state<HTMLElement | null>(null);
	let userBtnRail = $state<HTMLElement | null>(null);
	const toggleMenu = () => (menuOpen = !menuOpen);

	const path = $derived(page.url.pathname);
	/** A destination's count: pending pings on the inbox, live runs on runs. */
	const count = (n: NavItem) =>
		n.id === 'inbox'
			? fleet.pending.length
			: n.id === 'notices'
				? fleet.notices.length
				: n.id === 'runs'
					? fleet.active.length
					: 0;

	// The workshop the page is "in": its own page, the run's, or ?project=.
	const currentRunId = $derived(page.params.runId);
	const currentWorkshop = $derived(
		page.params.workshop ??
			(currentRunId ? fleet.runById.get(currentRunId)?.projectId : undefined) ??
			page.url.searchParams.get('project') ??
			undefined
	);

	const workshops = $derived(
		[...fleet.projects].sort((a, b) => {
			const la = fleet.liveByProject.get(a.id)?.length ?? 0;
			const lb = fleet.liveByProject.get(b.id)?.length ?? 0;
			return lb - la || a.name.localeCompare(b.name);
		})
	);
</script>

<aside class="sb glass" class:rail class:drawer>
	{#if rail}
		<div class="layer rl" transition:fade={xfade}>
			<Rail
				{workshops}
				{currentWorkshop}
				{path}
				{count}
				{onsearch}
				bind:userBtn={userBtnRail}
				onmenu={toggleMenu}
			/>
		</div>
	{:else}
		<div class="layer fl" transition:fade={xfade}>
			<SidebarHead {drawer} {onsearch} />
			<NavList {path} {count} {onnavigate} />
			<WorkshopTree {workshops} {currentWorkshop} {currentRunId} {onnavigate} />

			<div class="cap">
				<Meter
					label={t('shell.sidebar.runs')}
					value={fleet.capacity.runs.used}
					max={fleet.capacity.runs.max}
				/>
				<Meter
					label={t('shell.sidebar.envs')}
					tone="success"
					value={fleet.capacity.envs.used}
					max={fleet.capacity.envs.max}
				/>
			</div>

			<div class="user">
				<UserCard bind:btn={userBtnFull} onmenu={toggleMenu} />
				<LocaleSelect />
			</div>
		</div>
	{/if}
</aside>

<UserMenu anchor={rail ? userBtnRail : userBtnFull} bind:open={menuOpen} {onnavigate} />

<style>
	.sb {
		position: relative;
		z-index: var(--z-raise);
		flex: none;
		width: var(--sidebar-w);
		margin: var(--shell-gap) 0 var(--shell-gap) var(--shell-gap);
		border-radius: var(--r-pane);
		background: var(--glass-rail-bg);
		overflow: hidden;
		transition: width var(--dur-spring) var(--spring);
	}
	.sb.rail {
		width: var(--rail-w);
	}
	.sb.drawer {
		margin: 0;
		height: 100%;
		border-radius: 0 var(--r-pane) var(--r-pane) 0;
	}
	/* Each layer keeps its own width (inside the 1px glass border); the
	 * aside clips whichever is leaving while it changes shape. */
	.layer {
		position: absolute;
		top: 0;
		bottom: 0;
		left: 0;
		display: flex;
		flex-direction: column;
		min-height: 0;
	}
	.fl {
		width: calc(var(--sidebar-w) - 2px);
	}
	.rl {
		width: calc(var(--rail-w) - 2px);
		align-items: center;
		gap: 4px;
		padding: 12px 0 10px;
	}

	.cap {
		flex: none;
		margin: 0 10px 6px;
		padding: 8px 10px;
		border-radius: var(--r-row);
		background: var(--fill-1);
		border: 1px solid var(--line);
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
	}

	.user {
		flex: none;
		padding: 0 10px 10px;
		display: flex;
		align-items: center;
		gap: 8px;
	}
</style>
