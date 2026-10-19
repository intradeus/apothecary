<script lang="ts">
	// ⌘K: jump to a run, ticket, workshop or page, or do something. It
	// materialises from the top centre over a fading scrim and leaves the same
	// way; the keyboard-selected row carries a sliding bead. The filter runs on
	// every keystroke (no debounce): typing never waits for the list.
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { prefs } from '$lib/prefs.svelte';
	import { THEMES } from '$lib/themes';
	import { to } from '$lib/guild';
	import { shortId, stateLabel, stateTab } from '$lib/format';
	import type { Ticket } from '$lib/domain';
	import { NAV } from '$lib/nav';
	import { trap } from '$lib/ui/trap';
	import { I } from '$lib/ui/icons';
	import { indicator, materialize, scrim } from '$lib/ui/motion';
	import Vial from '$lib/ui/Vial.svelte';

	let { open = $bindable(false) }: { open: boolean } = $props();

	interface Item {
		group: string;
		label: string;
		sub?: string;
		glyph: string;
		/** A workshop row shows its vial instead of a glyph. */
		vial?: string;
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
			...NAV.filter((n) => n.id !== 'system').map((n) => ({
				group: 'Go to',
				label: n.label,
				glyph: n.icon,
				sub:
					n.id === 'inbox'
						? `${fleet.pending.length} pending`
						: n.chord
							? `G ${n.chord.toUpperCase()}`
							: undefined,
				run: go(n.href())
			})),
			{
				group: 'Go to',
				label: 'Integrations',
				glyph: I.integrations,
				run: go(to.system('integrations'))
			},
			{ group: 'Go to', label: 'Channels', glyph: I.channels, run: go(to.system('channels')) },
			{ group: 'Go to', label: 'Limits', glyph: I.limits, run: go(to.system('limits')) },
			{ group: 'Do', label: 'New ticket', glyph: I.add, run: go(to.newTicket()) },
			{
				group: 'Do',
				label: `New workshop`,
				glyph: I.add,
				run: go(to.newWorkshop())
			},
			...THEMES.filter((t) => t.id !== prefs.theme).map((t) => ({
				group: 'Do',
				label: `Use the ${t.label.toLowerCase()} theme`,
				glyph: t.icon,
				run: act(() => prefs.setTheme(t.id))
			})),
			{
				group: 'Do',
				label: prefs.collapsed ? 'Expand sidebar' : 'Collapse sidebar',
				sub: '⌘\\',
				glyph: prefs.collapsed ? I.expand : I.collapse,
				run: act(() => prefs.toggleCollapsed())
			}
		];
		for (const p of fleet.projects)
			out.push({
				group: 'Workshops',
				label: p.name,
				sub: p.repo.url,
				glyph: '',
				vial: p.id,
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
				glyph: I.runs,
				run: go(to.run(r.id))
			});
		for (const t of tickets.slice(0, 80))
			out.push({
				group: 'Tickets',
				label: t.title,
				sub: t.source?.key ?? fleet.projectName(t.projectId),
				glyph: I.tickets,
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

	// A new query starts at the top; a poll that reshapes the list (runs,
	// pings) keeps the keyboard where it was, clamped to what is left.
	$effect(() => {
		void query;
		sel = 0;
	});
	$effect(() => {
		if (sel > shown.length - 1) sel = Math.max(0, shown.length - 1);
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
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="scrim cp-scrim"
		transition:scrim
		onpointerdown={(e) => e.target === e.currentTarget && (open = false)}
	></div>
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="cp-wrap" onpointerdown={(e) => e.target === e.currentTarget && (open = false)}>
		<div
			class="cp lg-pop"
			role="dialog"
			aria-modal="true"
			aria-label="Command palette"
			transition:materialize={{ origin: 'top center', y: -10 }}
			use:trap={{ onescape: () => (open = false), initial: () => input }}
		>
			<div class="qrow">
				<span class="glass" aria-hidden="true">{I.search}</span>
				<input
					class="bare q"
					bind:this={input}
					bind:value={query}
					onkeydown={key}
					placeholder="Search runs, tickets, workshops, actions…"
					aria-label="Search"
				/>
				<span class="kbd">esc</span>
			</div>
			<div class="list" role="listbox" use:indicator>
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
						<span class="g"
							>{#if it.vial}<Vial projectId={it.vial} />{:else}{it.glyph}{/if}</span
						>
						<span class="ell grow">{it.label}</span>
						{#if it.sub}<span class="sub ell">{it.sub}</span>{/if}
					</button>
				{:else}
					<div class="empty-line none">Nothing matches “{query}”.</div>
				{/each}
			</div>
		</div>
	</div>
{/if}

<style>
	.cp-scrim {
		z-index: var(--z-palette);
	}
	.cp-wrap {
		position: fixed;
		inset: 0;
		z-index: var(--z-palette);
		display: flex;
		justify-content: center;
		align-items: flex-start;
		padding: 12vh 16px 16px;
	}
	.cp {
		width: min(620px, 100%);
		border-radius: var(--r-float);
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
		font-size: var(--fs-title);
	}
	.q {
		font-size: var(--fs-aside);
		padding: 4px 0;
	}
	/* The palette's one field is plainly focused; no ring around it. */
	.q:focus-visible {
		outline: none;
	}
	.list {
		overflow: auto;
		padding: 6px;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.list > :global(.ind) {
		border-radius: var(--r-row);
	}
	.gh {
		font-size: var(--fs-meta);
		color: var(--fnt);
		padding: 8px 10px 4px;
	}
	.mi {
		font-size: var(--fs-body);
		flex: none;
	}
	/* The bead marks the selection; hover only moves it (onmouseenter). */
	.mi:hover {
		background: transparent;
	}
	.sub {
		max-width: 40%;
	}
	.none {
		align-self: center;
		text-align: center;
		padding: 18px;
	}
</style>
