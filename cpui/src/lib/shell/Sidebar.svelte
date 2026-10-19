<script lang="ts">
	// The sidebar (DESIGN §5): brand, guild switcher, search, guild-scoped nav,
	// workshops (the current one expanded into its live runs), capacity and
	// the user row with the theme toggle. Collapses to a 60px rail.
	import { page } from '$app/state';
	import { fleet } from '$lib/fleet.svelte';
	import { prefs } from '$lib/prefs.svelte';
	import { lore } from '$lib/lore';
	import { currentGuild, GUILDS, ME, to } from '$lib/guild';
	import { shortId, stateLabel } from '$lib/format';
	import GuildAvatar from '$lib/ui/GuildAvatar.svelte';
	import Vial from '$lib/ui/Vial.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import type { Run } from '$lib/domain';

	let {
		onsearch,
		/** Mobile drawer mode: always full width, closes on navigation. */
		drawer = false,
		onnavigate
	}: { onsearch: () => void; drawer?: boolean; onnavigate?: () => void } = $props();

	const rail = $derived(prefs.collapsed && !drawer);
	const guild = $derived(currentGuild());

	let orgOpen = $state(false);
	let orgBtn = $state<HTMLElement | null>(null);

	const path = $derived(page.url.pathname);
	const base = $derived(to.desk());
	const nav = $derived([
		{ id: 'desk', icon: '◈', label: 'Desk', href: base, on: path === base, count: 0, hot: false },
		{
			id: 'inbox',
			icon: '✉',
			label: 'Inbox',
			href: to.inbox(),
			on: path.startsWith(to.inbox()),
			count: fleet.pending.length,
			hot: fleet.pending.length > 0
		},
		{
			id: 'runs',
			icon: '▶',
			label: 'Runs',
			href: to.runs(),
			on: path.startsWith(`${base}/runs`),
			count: fleet.active.length,
			hot: false
		},
		{
			id: 'tickets',
			icon: '☰',
			label: 'Tickets',
			href: to.tickets(),
			on: path.startsWith(`${base}/tickets`),
			count: 0,
			hot: false
		},
		{
			id: 'system',
			icon: '⚙',
			label: 'System',
			href: to.system(),
			on: path.startsWith(`${base}/system`),
			count: 0,
			hot: false
		}
	]);

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

	const runDot = (r: Run) =>
		r.state === 'waiting_on_human'
			? 'var(--amb)'
			: r.state === 'running' || r.state === 'provisioning'
				? 'var(--az)'
				: r.state === 'in_review'
					? 'var(--vio)'
					: 'var(--mut)';

	const maxRuns = $derived(fleet.settings?.maxConcurrentRuns ?? 0);
	const maxEnvs = $derived(fleet.settings?.maxEnvironments ?? 0);
	const pct = (n: number, d: number) => (d > 0 ? Math.min(100, Math.round((n / d) * 100)) : 0);
</script>

{#if rail}
	<aside class="rail lg">
		<div class="logo" title="Apothecary">℞</div>
		<button class="rb exp" title="Expand sidebar  ⌘\" onclick={() => prefs.toggleCollapsed()}
			>»</button
		>
		<button
			bind:this={orgBtn}
			class="rguild"
			title={guild.name}
			onclick={() => (orgOpen = !orgOpen)}
		>
			<GuildAvatar {guild} size={24} />
		</button>
		<button class="rb" title="Search  ⌘K" onclick={onsearch}>⌕</button>
		{#each nav as n (n.id)}
			<a class="rnav" class:on={n.on} href={n.href} title={n.label}
				>{n.icon}{'︎'}{#if n.hot}<span class="rbadge">{n.count}</span>{/if}</a
			>
		{/each}
		<span class="rdiv"></span>
		<div class="rws">
			{#each workshops as p (p.id)}
				{@const pings = fleet.pingsByProject.get(p.id) ?? 0}
				<a
					class="rnav ws"
					class:on={currentWorkshop === p.id}
					href={to.workshop(p.id)}
					title={p.name}
				>
					<Vial projectId={p.id} idle={!fleet.liveByProject.get(p.id)?.length} />
					{#if pings}<span class="rbadge">{pings}</span>{/if}
				</a>
			{/each}
		</div>
		<span class="grow"></span>
		<button
			class="rb"
			title="Theme: {prefs.theme === 'dark' ? 'Dark' : 'Light'}"
			onclick={() => prefs.toggleTheme()}>{prefs.theme === 'dark' ? '☾' : '☀'}</button
		>
		<span class="avatar" title={ME.name}>{ME.initials}</span>
	</aside>
{:else}
	<aside class="side lg" class:drawer>
		<div class="brand">
			<div class="logo" title="Apothecary">℞</div>
			<div class="bt">
				<span class="name">Apothecary</span>
				<span class="tagline">{lore('tagline')}</span>
			</div>
			{#if !drawer}
				<button class="rb sm" title="Collapse sidebar  ⌘\" onclick={() => prefs.toggleCollapsed()}
					>«</button
				>
			{/if}
		</div>

		<button bind:this={orgBtn} class="guild" onclick={() => (orgOpen = !orgOpen)}>
			<GuildAvatar {guild} size={26} />
			<span class="gt">
				<span class="gn">{guild.name}</span>
				<span class="gs"
					>{fleet.projects.length}
					{fleet.projects.length === 1
						? lore('workshop').toLowerCase()
						: lore('workshops').toLowerCase()}</span
				>
			</span>
			<span class="sw">⇅</span>
		</button>

		<button class="search" onclick={onsearch}>
			<span>Search, jump, do…</span><span class="kc mono">⌘K</span>
		</button>

		<nav class="nav">
			{#each nav as n (n.id)}
				<a class="ni" class:on={n.on} href={n.href} onclick={onnavigate}>
					<span class="ic">{n.icon}{'︎'}</span>
					<span class="grow">{n.label}</span>
					{#if n.count}<span class="count" class:hot={n.hot}>{n.count}</span>{/if}
				</a>
			{/each}
		</nav>

		<div class="wh">
			<span class="whl">{lore('workshops')}</span>
			<span class="legend">
				<span title="live runs">▶{'︎'}</span>
				<span title="pending pings">✉{'︎'}</span>
				<a class="plus" href={to.newWorkshop()} title="New {lore('workshop').toLowerCase()}">+</a>
			</span>
		</div>
		<div class="wl">
			{#each workshops as p (p.id)}
				{@const live = fleet.liveByProject.get(p.id) ?? []}
				{@const pings = fleet.pingsByProject.get(p.id) ?? 0}
				{@const cur = currentWorkshop === p.id}
				<a
					class="wi"
					class:on={cur}
					class:quiet={!live.length && !cur}
					href={to.workshop(p.id)}
					onclick={onnavigate}
				>
					<Vial projectId={p.id} idle={!live.length} />
					<span class="grow ell">{p.name}</span>
					<span class="lc mono">{live.length || ''}</span>
					<span class="pc mono" class:hot={pings > 0}>{pings || ''}</span>
				</a>
				{#if cur && live.length}
					{#each live as r (r.id)}
						<a
							class="ri"
							class:on={currentRunId === r.id}
							href={to.run(r.id)}
							title="{r.title} — {stateLabel[r.state]}"
							onclick={onnavigate}
						>
							<span
								class="rd"
								class:pulse={r.state === 'waiting_on_human'}
								style:background={runDot(r)}
								style:box-shadow="0 0 8px {runDot(r)}"
							></span>
							<span class="grow ell">{r.title}</span>
							<span class="rid mono">{shortId(r.id)}</span>
						</a>
					{/each}
				{/if}
			{:else}
				<a class="wi quiet" href={to.newWorkshop()}
					><span class="grow">Set up your first {lore('workshop').toLowerCase()} →</span></a
				>
			{/each}
		</div>

		<div class="cap">
			<div class="cc" title="Runs holding a concurrency slot / max concurrent runs">
				<div class="row spread nowrap">
					<span>Runs</span><span class="mono v">{fleet.slots.length}/{maxRuns || '–'}</span>
				</div>
				<div class="bar">
					<div class="fill parch" style:width="{pct(fleet.slots.length, maxRuns)}%"></div>
				</div>
			</div>
			<div class="cc" title="Live environments / max environments">
				<div class="row spread nowrap">
					<span>Envs</span><span class="mono v">{fleet.envs.length}/{maxEnvs || '–'}</span>
				</div>
				<div class="bar">
					<div class="fill grn" style:width="{pct(fleet.envs.length, maxEnvs)}%"></div>
				</div>
			</div>
		</div>

		<div class="user">
			<span class="avatar">{ME.initials}<span class="presence"></span></span>
			<span class="ut">
				<span class="un">{ME.name}</span>
				<span class="ur">{guild.role} · {guild.name}</span>
			</span>
			<button class="btn-sel btn-sm theme" title="Theme" onclick={() => prefs.toggleTheme()}
				>{prefs.theme === 'dark' ? 'Dark' : 'Light'}</button
			>
		</div>
	</aside>
{/if}

<Popover
	anchor={orgBtn}
	bind:open={orgOpen}
	at={{ left: rail ? 72 : 248, top: 56 }}
	width={312}
	title={lore('guilds')}
	legend="▶ live  ✉ pings"
>
	{#if GUILDS.length > 1}
		<a class="mi" href={to.allGuilds()} onclick={() => (orgOpen = false)}>
			<span class="alltile">◈</span>
			<span class="grow col0"
				><span>All {lore('guilds').toLowerCase()}</span><span class="sub"
					>Your desk across every {lore('guild').toLowerCase()}</span
				></span
			>
		</a>
		<div class="div"></div>
	{/if}
	{#each GUILDS as g (g.slug)}
		<a
			class="mi"
			class:on={g.slug === guild.slug}
			href={to.desk(g.slug)}
			onclick={() => (orgOpen = false)}
		>
			<GuildAvatar guild={g} size={26} />
			<span class="grow col0"
				><span>{g.name}</span><span class="sub"
					>{g.role} · {fleet.projects.length}
					{lore('workshops').toLowerCase()}</span
				></span
			>
			<span class="mono hue-az" style="font-size:10.5px">{fleet.active.length || ''}</span>
			{#if fleet.pending.length}<span class="count hot">{fleet.pending.length}</span>{/if}
		</a>
	{/each}
	<div class="div"></div>
	<div class="row nowrap" style="padding:2px 4px; gap:4px">
		<button class="btn-ghost btn-sm" disabled title="Organisations arrive with multi-user support"
			>{lore('newGuild')}</button
		>
		<button class="btn-ghost btn-sm" disabled title="Organisations arrive with multi-user support"
			>Join with invite</button
		>
	</div>
</Popover>

<style>
	/* The sidebar is dark green in both themes: its text is fixed light. */
	.side,
	.rail {
		position: relative;
		z-index: 1;
		flex: none;
		margin: 6px 0 6px 6px;
		border-radius: 16px;
		background: var(--railBg);
		color: var(--railTx);
		display: flex;
		flex-direction: column;
		min-height: 0;
		overflow: hidden;
	}
	.side {
		width: 232px;
	}
	.side.drawer {
		margin: 0;
		height: 100%;
		border-radius: 0 16px 16px 0;
	}
	.rail {
		width: 60px;
		align-items: center;
		gap: 4px;
		padding: 12px 0 10px;
	}

	.logo {
		flex: none;
		width: 30px;
		height: 30px;
		border-radius: 12px;
		background: radial-gradient(
			circle at 30% 22%,
			rgba(220, 255, 230, 0.75),
			rgba(60, 160, 100, 0.55) 35%,
			#0f3a22 80%
		);
		border: 1px solid rgba(210, 255, 225, 0.35);
		box-shadow:
			0 0 18px rgba(80, 200, 130, 0.32),
			inset 0 1px 1px rgba(255, 255, 255, 0.6),
			inset 0 -4px 8px rgba(0, 0, 0, 0.35);
		display: flex;
		align-items: center;
		justify-content: center;
		font-family: var(--serif);
		font-size: 19px;
		color: #efe6cf;
		text-shadow: 0 1px 2px rgba(0, 0, 0, 0.5);
	}
	.rail .logo {
		width: 32px;
		height: 32px;
		font-size: 21px;
	}

	.rb {
		all: unset;
		cursor: pointer;
		width: 40px;
		height: 32px;
		border-radius: 11px;
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--railMut);
		font-size: 15px;
	}
	.rb.exp {
		height: 24px;
		border-radius: 9px;
		font-size: 13px;
	}
	.rb.sm {
		width: 28px;
		height: 28px;
		border-radius: 9px;
		font-size: 13px;
	}
	.rb:hover {
		background: rgba(255, 255, 255, 0.06);
		color: var(--railTx);
	}
	.rguild {
		all: unset;
		cursor: pointer;
		position: relative;
		width: 40px;
		height: 38px;
		border-radius: 11px;
		background: var(--railSel);
		border: 1px solid rgba(140, 230, 175, 0.26);
		box-shadow:
			inset 0 1px 0 rgba(255, 255, 255, 0.07),
			0 1px 3px rgba(0, 0, 0, 0.35);
		display: flex;
		align-items: center;
		justify-content: center;
	}
	.rnav {
		position: relative;
		width: 40px;
		height: 36px;
		border-radius: 11px;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 15px;
		color: var(--railMut);
		flex: none;
	}
	.rnav.ws {
		height: 34px;
	}
	.rnav:hover {
		background: rgba(255, 255, 255, 0.05);
		text-decoration: none;
		color: var(--railTx);
	}
	.rnav.on {
		background: var(--railSel);
		box-shadow:
			inset 0 0 0 1px rgba(140, 230, 175, 0.26),
			0 1px 3px rgba(0, 0, 0, 0.35);
		color: var(--railTx);
	}
	.rbadge {
		position: absolute;
		top: 2px;
		right: 1px;
		font-family: var(--mono);
		font-size: 9.5px;
		font-weight: 700;
		padding: 0 4px;
		border-radius: 9px;
		background: var(--amb);
		color: var(--ink);
		line-height: 14px;
	}
	.rdiv {
		width: 24px;
		height: 1px;
		margin: 6px 0;
		background: rgba(210, 255, 225, 0.14);
	}
	.rws {
		display: flex;
		flex-direction: column;
		gap: 4px;
		align-items: center;
		overflow: auto;
		min-height: 0;
	}

	.brand {
		height: 56px;
		flex: none;
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 0 10px 0 14px;
	}
	.bt {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		line-height: 1.1;
	}
	.name {
		font-weight: 600;
		font-size: 15px;
		letter-spacing: -0.01em;
	}
	.tagline {
		font-family: var(--serif);
		font-style: italic;
		font-size: 13px;
		color: var(--railMut);
	}

	.guild {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		flex: none;
		margin: 0 10px 8px;
		padding: 7px 9px;
		border-radius: 12px;
		background: var(--railSel);
		border: 1px solid rgba(140, 230, 175, 0.26);
		box-shadow:
			inset 0 1px 0 rgba(255, 255, 255, 0.07),
			0 1px 3px rgba(0, 0, 0, 0.35);
		display: flex;
		align-items: center;
		gap: 9px;
	}
	.gt {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		line-height: 1.2;
	}
	.gn {
		font-weight: 600;
		font-size: 13px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.gs {
		font-size: 11px;
		color: var(--railMut);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.sw {
		flex: none;
		color: var(--railMut);
		font-size: 11px;
	}

	.search {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		flex: none;
		margin: 0 10px 6px;
		padding: 6px 10px;
		border-radius: 11px;
		background: rgba(210, 255, 225, 0.05);
		border: 1px solid rgba(210, 255, 225, 0.1);
		display: flex;
		justify-content: space-between;
		align-items: center;
		color: var(--railMut);
		font-size: 12.5px;
	}
	.search:hover {
		background: rgba(210, 255, 225, 0.08);
	}
	.kc {
		font-size: 10.5px;
		padding: 1px 5px;
		border-radius: 5px;
		background: rgba(255, 255, 255, 0.06);
	}

	.nav {
		flex: none;
		display: flex;
		flex-direction: column;
		gap: 1px;
		padding: 2px 8px;
	}
	.ni,
	.wi {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 10px;
		border-radius: 10px;
		color: var(--railTx);
	}
	.ni:hover,
	.wi:hover,
	.ri:hover {
		background: rgba(255, 255, 255, 0.05);
		text-decoration: none;
	}
	.ni.on,
	.wi.on {
		background: var(--railSel);
		box-shadow:
			inset 0 0 0 1px rgba(140, 230, 175, 0.26),
			0 1px 3px rgba(0, 0, 0, 0.35);
		font-weight: 600;
	}
	.ic {
		width: 16px;
		text-align: center;
		font-size: 13px;
		color: var(--railMut);
	}
	.count {
		color: var(--railMut);
	}
	.count.hot {
		color: var(--ink);
	}

	.wh {
		flex: none;
		padding: 14px 18px 6px;
		font-size: 11px;
		color: var(--railFnt);
		display: flex;
		justify-content: space-between;
		align-items: baseline;
	}
	.whl {
		font-family: var(--serif);
		font-style: italic;
		font-size: 14px;
		color: var(--railMut);
	}
	.legend {
		display: flex;
		gap: 10px;
		align-items: center;
	}
	.plus {
		color: var(--railMut);
		font-size: 14px;
		line-height: 1;
	}
	.plus:hover {
		color: var(--railTx);
		text-decoration: none;
	}
	.wl {
		flex: 1;
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
		gap: 1px;
		padding: 0 8px 8px;
	}
	.wi.quiet {
		color: var(--railMut);
	}
	.lc {
		font-size: 10.5px;
		color: var(--az);
		min-width: 12px;
		text-align: right;
	}
	.pc {
		font-size: 10.5px;
		font-weight: 700;
		min-width: 18px;
		text-align: center;
		border-radius: 9px;
	}
	.pc.hot {
		background: var(--amb);
		color: var(--ink);
	}
	.ri {
		margin-left: 15px;
		padding: 4px 10px 4px 14px;
		border-left: 1px solid rgba(210, 255, 225, 0.12);
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		color: var(--railMut);
		border-radius: 0 9px 9px 0;
	}
	.ri.on {
		background: rgba(255, 255, 255, 0.06);
		color: var(--railTx);
	}
	.rd {
		flex: none;
		width: 6px;
		height: 6px;
		border-radius: 50%;
	}
	.rid {
		font-size: 10px;
		color: var(--railFnt);
	}

	.cap {
		flex: none;
		margin: 0 10px 6px;
		padding: 8px 10px;
		border-radius: 11px;
		background: rgba(210, 255, 225, 0.04);
		border: 1px solid rgba(210, 255, 225, 0.08);
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
		font-size: 11px;
		color: var(--railMut);
	}
	.cc {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.cc .v {
		color: var(--railTx);
		font-size: 11px;
	}
	.bar {
		height: 5px;
		border-radius: 4px;
		background: rgba(0, 0, 0, 0.3);
		overflow: hidden;
		display: flex;
	}
	.fill.parch {
		background: linear-gradient(90deg, #cbbd98, #efe6cf);
	}
	.fill.grn {
		background: linear-gradient(90deg, #3fae72, #7ee2a8);
	}

	.user {
		flex: none;
		border-top: 1px solid rgba(210, 255, 225, 0.1);
		padding: 10px 10px 10px 12px;
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.avatar {
		flex: none;
		position: relative;
		width: 30px;
		height: 30px;
		border-radius: 50%;
		background: linear-gradient(160deg, #efe6cf, #b9a77a);
		color: #2a2210;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 11.5px;
		font-weight: 700;
		box-shadow:
			inset 0 1px 0 rgba(255, 255, 255, 0.5),
			0 1px 3px rgba(0, 0, 0, 0.4);
	}
	.presence {
		position: absolute;
		right: -1px;
		bottom: -1px;
		width: 9px;
		height: 9px;
		border-radius: 50%;
		background: #7ee2a8;
		border: 2px solid #0a1a10;
	}
	.ut {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		line-height: 1.2;
	}
	.un {
		font-size: 12.5px;
		font-weight: 600;
	}
	.ur {
		font-size: 11px;
		color: var(--railMut);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.theme {
		font-size: 11px;
	}

	.alltile {
		flex: none;
		width: 26px;
		height: 26px;
		border-radius: 9px;
		border: 1px dashed var(--ln2);
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--mut);
	}
	.col0 {
		display: flex;
		flex-direction: column;
		line-height: 1.25;
	}
</style>
