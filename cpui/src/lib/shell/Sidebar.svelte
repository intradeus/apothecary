<script lang="ts">
	// The sidebar (DESIGN §5): brand, guild switcher, search, guild-scoped nav,
	// workshops (the current one expanded into its live runs), capacity and
	// the user row with the theme toggle. Collapses to a 60px rail.
	//
	// Collapsing is one surface changing shape, not a swap: the aside's width
	// rides the spring while the full and rail contents cross-fade. Each
	// layer keeps its own width and is clipped by the aside, so nothing inside
	// reflows mid-flight; flipping it again retargets from wherever it is.
	// Selection slides: one bead springs between nav items and workshops.
	import { fade } from 'svelte/transition';
	import { page } from '$app/state';
	import { fleet } from '$lib/fleet.svelte';
	import { prefs } from '$lib/prefs.svelte';
	import { i18n, LOCALES, t, type Locale } from '$lib/i18n/index.svelte';
	import { NAV, type NavItem } from '$lib/nav';
	import { currentGuild, GUILDS, ME, to } from '$lib/guild';
	import { shortId, stateLabel } from '$lib/format';
	import GuildAvatar from '$lib/ui/GuildAvatar.svelte';
	import UserAvatar from '$lib/ui/UserAvatar.svelte';
	import Logo from '$lib/ui/Logo.svelte';
	import Meter from '$lib/ui/Meter.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import Vial from '$lib/ui/Vial.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import Select from '$lib/ui/Select.svelte';
	import { I } from '$lib/ui/icons';
	import { bump, indicator, ms } from '$lib/ui/motion';

	let {
		onsearch,
		/** Mobile drawer mode: always full width, closes on navigation. */
		drawer = false,
		onnavigate
	}: { onsearch: () => void; drawer?: boolean; onnavigate?: () => void } = $props();

	const localeIds = Object.keys(LOCALES) as Locale[];
	const multiLocale = localeIds.length > 1;
	const pickLocale = (v: Locale) => void i18n.set(v);
	const localeCodes = localeIds.map((id) => ({ value: id, label: id.toUpperCase() }));
	const localeNames = localeIds.map((id) => ({ value: id, label: LOCALES[id].label }));

	const rail = $derived(prefs.collapsed && !drawer);
	const guild = $derived(currentGuild());
	const xfade = { duration: ms('--dur-fade', 160) };

	let orgOpen = $state(false);
	let orgBtnFull = $state<HTMLElement | null>(null);
	let orgBtnRail = $state<HTMLElement | null>(null);

	const path = $derived(page.url.pathname);
	/** A destination's count: pending pings on the inbox, live runs on runs. */
	const count = (n: NavItem) =>
		n.id === 'inbox' ? fleet.pending.length : n.id === 'runs' ? fleet.active.length : 0;

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

<aside class="sb lg" class:rail class:drawer>
	{#if rail}
		<div class="layer rl" transition:fade={xfade}>
			<Logo size={32} />
			<button
				class="btn-icon rb exp"
				title={t('shell.sidebar.expandTitle')}
				aria-label={t('shell.sidebar.expand')}
				onclick={() => prefs.toggleCollapsed()}>{I.expand}</button
			>
			<button
				bind:this={orgBtnRail}
				class="btn-sel rguild"
				title={guild.name}
				aria-label={t('shell.sidebar.switchGuild')}
				onclick={() => (orgOpen = !orgOpen)}
			>
				<GuildAvatar {guild} size={24} />
			</button>
			<button
				class="btn-icon rb"
				title={t('shell.sidebar.searchTitle')}
				aria-label={t('shell.sidebar.search')}
				onclick={onsearch}>{I.search}</button
			>
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
						>{n.icon}{#if n.id === 'inbox'}<span
								class="count hot sm rbadge"
								class:gone={!c}
								use:bump={c}>{c}</span
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
			{#if multiLocale}
				<span class="lang rlang">
					<Select
						size="sm"
						inline
						label={t('shell.sidebar.language')}
						title={t('shell.sidebar.language')}
						options={localeCodes}
						value={i18n.locale}
						onchange={pickLocale}
					/>
				</span>
			{/if}
			<button
				class="btn-icon rb"
				title={t('shell.sidebar.themeTitle', { name: prefs.themeDef.label })}
				aria-label={t('shell.sidebar.changeTheme')}
				onclick={() => prefs.cycleTheme()}>{prefs.themeDef.icon}</button
			>
			<UserAvatar name={ME.name} initials={ME.initials} />
		</div>
	{:else}
		<div class="layer fl" transition:fade={xfade}>
			<div class="brand">
				<Logo size={30} />
				<div class="bt">
					<span class="name">Apothecary</span>
					<span class="tagline">{t('shell.sidebar.tagline')}</span>
				</div>
				{#if !drawer}
					<button
						class="btn-icon rb sm"
						title={t('shell.sidebar.collapseTitle')}
						aria-label={t('shell.sidebar.collapse')}
						onclick={() => prefs.toggleCollapsed()}>{I.collapse}</button
					>
				{/if}
			</div>

			<button bind:this={orgBtnFull} class="btn-sel guild" onclick={() => (orgOpen = !orgOpen)}>
				<GuildAvatar {guild} size={26} />
				<span class="gt">
					<span class="gn ell">{guild.name}</span>
					<span class="gs ell"
						>{t('shell.sidebar.workshopCount', { count: fleet.projects.length })}</span
					>
				</span>
				<span class="sw">{I.switcher}</span>
			</button>

			<button class="search" onclick={onsearch}>
				<span>{t('shell.sidebar.searchPlaceholder')}</span><span class="kbd">⌘K</span>
			</button>

			<nav class="nav on-rail" aria-label={t('shell.sidebar.primary')} use:indicator>
				{#each NAV as n (n.id)}
					{@const c = count(n)}
					<a
						class="ni"
						class:on={n.match(path)}
						href={n.href()}
						aria-current={n.match(path) ? 'page' : undefined}
						onclick={onnavigate}
					>
						<span class="ic">{n.icon}</span>
						<span class="grow">{n.label}</span>
						<span
							class="count"
							class:hot={n.id === 'inbox' && c > 0}
							use:bump={n.id === 'inbox' ? c : 0}>{c || ''}</span
						>
					</a>
				{/each}
			</nav>

			<div class="wh">
				<span class="whl">{t('shell.sidebar.workshops')}</span>
				<span class="legend">
					<span title={t('shell.sidebar.liveRuns')}>{I.runs}</span>
					<span title={t('shell.sidebar.pendingPings')}>{I.inbox}</span>
					<a class="plus" href={to.newWorkshop()} title={t('shell.sidebar.newWorkshop')}>{I.add}</a>
				</span>
			</div>
			<div class="wl on-rail" use:indicator={{ selector: '.wi.on' }}>
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
						<span class="grow ell">{fleet.projectName(p.id)}</span>
						<span class="lc mono">{live.length || ''}</span>
						<span class="count" class:hot={pings > 0} use:bump={pings}>{pings || ''}</span>
					</a>
					{#if cur && live.length}
						{#each live as r (r.id)}
							<a
								class="ri"
								class:cur={currentRunId === r.id}
								href={to.run(r.id)}
								title="{r.title} — {stateLabel[r.state]}"
								onclick={onnavigate}
							>
								<StatusChip state={r.state} compact />
								<span class="grow ell">{r.title}</span>
								<span class="rid mono">{shortId(r.id)}</span>
							</a>
						{/each}
					{/if}
				{:else}
					<a class="wi quiet" href={to.newWorkshop()}
						><span class="grow">{t('shell.sidebar.firstWorkshop', { arrow: I.forward })}</span></a
					>
				{/each}
			</div>

			<div class="cap">
				<Meter
					rail
					label={t('shell.sidebar.runs')}
					value={fleet.capacity.runs.used}
					max={fleet.capacity.runs.max}
				/>
				<Meter
					rail
					label={t('shell.sidebar.envs')}
					tone="grn"
					value={fleet.capacity.envs.used}
					max={fleet.capacity.envs.max}
				/>
			</div>

			<div class="user">
				<UserAvatar name={ME.name} initials={ME.initials} presence />
				<span class="ut">
					<span class="un">{ME.name}</span>
					<span class="ur ell">{guild.role} · {guild.name}</span>
				</span>
				{#if multiLocale}
					<span class="lang">
						<Select
							size="sm"
							inline
							label={t('shell.sidebar.language')}
							title={t('shell.sidebar.language')}
							options={localeNames}
							value={i18n.locale}
							onchange={pickLocale}
						/>
					</span>
				{/if}
				<button
					class="btn-sel btn-sm theme"
					title={t('shell.sidebar.theme')}
					onclick={() => prefs.cycleTheme()}>{prefs.themeDef.label}</button
				>
			</div>
		</div>
	{/if}
</aside>

<Popover
	anchor={rail ? orgBtnRail : orgBtnFull}
	bind:open={orgOpen}
	at={{ left: rail ? 72 : 248, top: 56 }}
	width={312}
	title={t('shell.sidebar.guilds')}
	legend="{I.runs} {t('shell.sidebar.legendLive')}  {I.inbox} {t('shell.sidebar.legendPings')}"
>
	{#if GUILDS.length > 1}
		<a class="mi" href={to.allGuilds()} onclick={() => (orgOpen = false)}>
			<span class="alltile">{I.desk}</span>
			<span class="col"
				><span>{t('shell.sidebar.allGuilds')}</span><span class="sub"
					>{t('shell.sidebar.allGuildsSub')}</span
				></span
			>
		</a>
		<div class="menu-div"></div>
	{/if}
	{#each GUILDS as g (g.slug)}
		<a
			class="mi"
			class:on={g.slug === guild.slug}
			href={to.desk(g.slug)}
			onclick={() => (orgOpen = false)}
		>
			<GuildAvatar guild={g} size={26} />
			<span class="col"
				><span>{g.name}</span><span class="sub"
					>{t('shell.sidebar.guildSub', {
						role: g.role,
						workshops: t('shell.sidebar.workshopCount', { count: fleet.projects.length })
					})}</span
				></span
			>
			<span class="mono hue-az live-n">{fleet.active.length || ''}</span>
			{#if fleet.pending.length}<span class="count hot">{fleet.pending.length}</span>{/if}
		</a>
	{/each}
	<div class="menu-div"></div>
	<div class="row nowrap foot">
		<button class="btn-ghost btn-sm" disabled title={t('shell.sidebar.orgSoon')}
			>{t('shell.sidebar.foundGuild')}</button
		>
		<button class="btn-ghost btn-sm" disabled title={t('shell.sidebar.orgSoon')}
			>{t('shell.sidebar.joinGuild')}</button
		>
	</div>
</Popover>

<style>
	/* The sidebar is dark green in both themes: its text is fixed light. */
	.sb {
		position: relative;
		z-index: var(--z-raise);
		flex: none;
		width: var(--sidebar-w);
		margin: var(--shell-gap) 0 var(--shell-gap) var(--shell-gap);
		border-radius: var(--r-pane);
		background: var(--railBg);
		color: var(--railTx);
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

	/* Chrome buttons on the dark rail: the shared icon button, in rail ink. */
	.rb {
		width: 40px;
		height: 32px;
		border-radius: var(--r-row);
		color: var(--railMut);
		font-size: var(--fs-lead);
	}
	.rb.exp {
		height: 24px;
		border-radius: var(--r-tab);
		font-size: var(--fs-body);
	}
	.rb.sm {
		width: 28px;
		height: 28px;
		border-radius: var(--r-tab);
		font-size: var(--fs-body);
	}
	.rb:hover {
		background: var(--railActive);
		color: var(--railTx);
	}
	.rguild {
		width: 40px;
		height: 38px;
		padding: 0;
		border-radius: var(--r-row);
	}
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
		color: var(--railMut);
		flex: none;
	}
	.rnav.ws {
		height: 34px;
	}
	.rnav:hover {
		background: var(--railHover);
		text-decoration: none;
		color: var(--railTx);
	}
	.rnav.on {
		color: var(--railTx);
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
		background: var(--railRule);
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
		font-size: var(--fs-lead);
		letter-spacing: var(--tr-heading);
	}
	.tagline {
		font-family: var(--serif);
		font-style: italic;
		font-size: var(--fs-body);
		color: var(--railMut);
	}

	.guild {
		flex: none;
		display: flex;
		justify-content: flex-start;
		gap: 9px;
		margin: 0 10px 8px;
		padding: 7px 9px;
		text-align: left;
		font-weight: 400;
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
		font-size: var(--fs-body);
	}
	.gs {
		font-size: var(--fs-meta);
		color: var(--railMut);
	}
	.sw {
		flex: none;
		color: var(--railMut);
		font-size: var(--fs-meta);
	}

	/* The command bar: a field in rail ink that opens the palette. */
	.search {
		flex: none;
		display: flex;
		justify-content: space-between;
		margin: 0 10px 6px;
		padding: 6px 10px;
		border-radius: var(--r-row);
		background: var(--railField);
		border: 1px solid var(--railLn);
		box-shadow: none;
		color: var(--railMut);
		font-size: var(--fs-ui);
		font-weight: 400;
	}
	.search:hover {
		background: var(--railFieldHover);
	}
	.search .kbd {
		background: var(--railActive);
		color: var(--railMut);
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
		border-radius: var(--r-row);
		color: var(--railTx);
	}
	.ni:hover,
	.wi:hover,
	.ri:hover {
		background: var(--railHover);
		text-decoration: none;
	}
	.ni:active,
	.wi:active,
	.rnav:active {
		opacity: 0.75;
	}
	.ni.on,
	.wi.on {
		font-weight: 600;
	}
	.ic {
		width: 16px;
		text-align: center;
		font-size: var(--fs-body);
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
		font-size: var(--fs-meta);
		color: var(--railFnt);
		display: flex;
		justify-content: space-between;
		align-items: baseline;
	}
	.whl {
		font-family: var(--serif);
		font-style: italic;
		font-size: var(--fs-aside);
		color: var(--railMut);
	}
	.legend {
		display: flex;
		gap: 10px;
		align-items: center;
	}
	.plus {
		color: var(--railMut);
		font-size: var(--fs-aside);
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
		font-size: var(--fs-id);
		color: var(--az);
		min-width: 12px;
		text-align: right;
	}
	.ri {
		margin-left: 15px;
		padding: 4px 10px 4px 14px;
		border-left: 1px solid var(--railRule);
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: var(--fs-label);
		color: var(--railMut);
		border-radius: 0 var(--r-tab) var(--r-tab) 0;
	}
	.ri.cur {
		background: var(--railActive);
		color: var(--railTx);
	}
	.rid {
		font-size: var(--fs-id);
		color: var(--railFnt);
	}

	.cap {
		flex: none;
		margin: 0 10px 6px;
		padding: 8px 10px;
		border-radius: var(--r-row);
		background: var(--railCard);
		border: 1px solid var(--railCardLn);
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
	}

	.user {
		flex: none;
		border-top: 1px solid var(--railLn);
		padding: 10px 10px 10px 12px;
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.ut {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		line-height: 1.2;
	}
	.un {
		font-size: var(--fs-ui);
		font-weight: 600;
	}
	.ur {
		font-size: var(--fs-meta);
		color: var(--railMut);
	}
	.lang {
		display: inline-flex;
		max-width: 96px;
	}
	/* The rail is 60px: room for the code and the chevron. */
	.rlang {
		max-width: 52px;
	}
	.theme {
		font-size: var(--fs-meta);
	}

	.alltile {
		flex: none;
		width: 26px;
		height: 26px;
		border-radius: var(--r-tab);
		border: 1px dashed var(--ln2);
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--mut);
	}
	.live-n {
		font-size: var(--fs-id);
	}
	/* A two-line menu row: base .col's gap is for page columns. */
	.col {
		gap: 0;
	}
	.foot {
		padding: 2px 4px;
		gap: 4px;
	}
</style>
