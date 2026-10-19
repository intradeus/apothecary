<script lang="ts">
	// ⌘K: jump to a run, ticket, workshop or page, or do something.
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { prefs } from '$lib/prefs.svelte';
	import { lore } from '$lib/lore';
	import { to } from '$lib/guild';
	import { shortId, stateLabel, stateTab } from '$lib/format';
	import type { Ticket } from '$lib/domain';
	import { trap } from '$lib/ui/trap';

	let { open = $bindable(false) }: { open: boolean } = $props();

	interface Item {
		group: string;
		label: string;
		sub?: string;
		glyph: string;
		run: () => void;
	}

	let query = $state('');
	let sel = $state(0);
	let input = $state<HTMLInputElement | null>(null);
	let tickets = $state<Ticket[]>([]);

	$effect(() => {
		if (!open) return;
		query = '';
		sel = 0;
		api.tickets
			.list()
			.then((t) => (tickets = t))
			.catch(() => {});
	});

	const go = (href: string) => () => {
		open = false;
		void goto(href);
	};
	const act = (fn: () => void) => () => {
		open = false;
		fn();
	};

	const items = $derived.by<Item[]>(() => {
		const out: Item[] = [
			{ group: 'Go to', label: 'Desk', glyph: '◈', run: go(to.desk()) },
			{
				group: 'Go to',
				label: 'Inbox',
				glyph: '✉',
				sub: `${fleet.pending.length} pending`,
				run: go(to.inbox())
			},
			{ group: 'Go to', label: 'Runs', glyph: '▶', run: go(to.runs()) },
			{ group: 'Go to', label: 'Tickets', glyph: '☰', run: go(to.tickets()) },
			{ group: 'Go to', label: 'Integrations', glyph: '⚙', run: go(to.system('integrations')) },
			{ group: 'Go to', label: 'Channels', glyph: '⚙', run: go(to.system('channels')) },
			{ group: 'Go to', label: 'Limits', glyph: '⚙', run: go(to.system('limits')) },
			{ group: 'Do', label: 'New ticket', glyph: '+', run: go(to.newTicket()) },
			{
				group: 'Do',
				label: `New ${lore('workshop').toLowerCase()}`,
				glyph: '+',
				run: go(to.newWorkshop())
			},
			{
				group: 'Do',
				label: `Switch to ${prefs.theme === 'dark' ? 'light' : 'dark'} theme`,
				glyph: prefs.theme === 'dark' ? '☀' : '☾',
				run: act(() => prefs.toggleTheme())
			},
			{
				group: 'Do',
				label: prefs.flavor === 'lore' ? 'Use plain labels' : 'Use lore labels',
				sub: prefs.flavor === 'lore' ? 'Organisation, Project, Run' : 'Guild, Workshop, Batch',
				glyph: '℞',
				run: act(() => prefs.setFlavor(prefs.flavor === 'lore' ? 'plain' : 'lore'))
			},
			{
				group: 'Do',
				label: prefs.collapsed ? 'Expand sidebar' : 'Collapse sidebar',
				sub: '⌘\\',
				glyph: prefs.collapsed ? '»' : '«',
				run: act(() => prefs.toggleCollapsed())
			}
		];
		for (const p of fleet.projects)
			out.push({
				group: lore('workshops'),
				label: p.name,
				sub: p.repo.url,
				glyph: '⚗',
				run: go(to.workshop(p.id))
			});
		const runs = [...fleet.runs]
			.filter((r) => r.state !== 'archived')
			.sort((a, b) =>
				stateTab[a.state] !== stateTab[b.state]
					? stateTab[a.state] === 'active'
						? -1
						: 1
					: b.updatedAt.localeCompare(a.updatedAt)
			)
			.slice(0, 60);
		for (const r of runs)
			out.push({
				group: 'Runs',
				label: r.title,
				sub: `${shortId(r.id)} · ${stateLabel[r.state]}`,
				glyph: '▶',
				run: go(to.run(r.id))
			});
		for (const t of tickets.slice(0, 80))
			out.push({
				group: 'Tickets',
				label: t.title,
				sub: t.source?.key ?? fleet.projectById.get(t.projectId)?.name,
				glyph: '☰',
				run: go(to.ticket(t.id))
			});
		return out;
	});

	const shown = $derived.by(() => {
		const q = query.trim().toLowerCase();
		if (!q)
			return items
				.filter((i) => i.group === 'Go to' || i.group === 'Do' || i.group === 'Runs')
				.slice(0, 24);
		const words = q.split(/\s+/);
		return items
			.filter((i) => {
				const hay = `${i.label} ${i.sub ?? ''} ${i.group}`.toLowerCase();
				return words.every((w) => hay.includes(w));
			})
			.slice(0, 40);
	});

	$effect(() => {
		void shown;
		sel = 0;
	});

	function key(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			sel = Math.min(shown.length - 1, sel + 1);
			document.getElementById(`cp-${sel}`)?.scrollIntoView({ block: 'nearest' });
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			sel = Math.max(0, sel - 1);
			document.getElementById(`cp-${sel}`)?.scrollIntoView({ block: 'nearest' });
		} else if (e.key === 'Enter') {
			e.preventDefault();
			shown[sel]?.run();
		}
	}
</script>

{#if open}
	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
	<div class="scrim" onmousedown={(e) => e.target === e.currentTarget && (open = false)}>
		<div
			class="cp lg-pop"
			role="dialog"
			aria-modal="true"
			aria-label="Command palette"
			use:trap={{ onescape: () => (open = false), initial: () => input }}
		>
			<div class="qrow">
				<span class="glass">⌕</span>
				<input
					bind:this={input}
					bind:value={query}
					onkeydown={key}
					placeholder="Search runs, tickets, {lore('workshops').toLowerCase()}, actions…"
					aria-label="Search"
				/>
				<span class="kc mono">esc</span>
			</div>
			<div class="list" role="listbox">
				{#each shown as it, i (it.group + it.label + i)}
					{#if i === 0 || shown[i - 1].group !== it.group}
						<div class="gh">{it.group}</div>
					{/if}
					<button
						id="cp-{i}"
						class="mi"
						class:on={i === sel}
						role="option"
						aria-selected={i === sel}
						onmouseenter={() => (sel = i)}
						onclick={it.run}
					>
						<span class="g">{it.glyph}{'︎'}</span>
						<span class="ell grow">{it.label}</span>
						{#if it.sub}<span class="sub ell">{it.sub}</span>{/if}
					</button>
				{:else}
					<div class="none">Nothing matches “{query}”.</div>
				{/each}
			</div>
		</div>
	</div>
{/if}

<style>
	.scrim {
		position: fixed;
		inset: 0;
		z-index: 60;
		background: var(--scrim);
		backdrop-filter: blur(4px);
		display: flex;
		justify-content: center;
		align-items: flex-start;
		padding: 12vh 16px 16px;
	}
	.cp {
		width: min(620px, 100%);
		border-radius: 20px;
		display: flex;
		flex-direction: column;
		max-height: 64vh;
		overflow: hidden;
	}
	.qrow {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 10px 14px;
		border-bottom: 1px solid var(--ln);
	}
	.glass {
		color: var(--mut);
		font-size: 16px;
	}
	.qrow input {
		border: 0 !important;
		background: transparent !important;
		box-shadow: none !important;
		font-size: 14px !important;
		padding: 4px 0 !important;
	}
	.kc {
		font-size: 10px;
		color: var(--fnt);
		padding: 1px 5px;
		border-radius: 5px;
		background: var(--p2);
	}
	.list {
		overflow: auto;
		padding: 6px;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.gh {
		font-size: 11px;
		color: var(--fnt);
		padding: 8px 10px 4px;
	}
	.mi {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 7px 10px;
		border-radius: 10px;
		font-size: 13px;
		min-width: 0;
	}
	.mi.on {
		background: var(--sel);
		box-shadow:
			inset 0 0 0 1px var(--selLn),
			var(--selSh);
	}
	.g {
		width: 16px;
		text-align: center;
		color: var(--mut);
		flex: none;
	}
	.sub {
		color: var(--mut);
		font-size: 11.5px;
		max-width: 40%;
	}
	.none {
		padding: 18px;
		color: var(--mut);
		text-align: center;
	}
</style>
