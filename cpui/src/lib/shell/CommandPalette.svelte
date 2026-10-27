<script lang="ts">
	// ⌘K: jump to a run, ticket, workshop or page, or do something. It
	// materialises from the top centre over a fading scrim and leaves the same
	// way; the keyboard-selected row carries a sliding bead. The filter runs on
	// every keystroke (no debounce): typing never waits for the list.
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { prefs } from '$lib/prefs.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { THEMES } from '$lib/themes';
	import { to } from '$lib/urls';
	import { shortId, stateLabel, stateTab } from '$lib/format';
	import type { Ticket } from '$lib/domain';
	import { NAV } from '$lib/nav';
	import { trap } from '$lib/ui/trap';
	import { I } from '$lib/ui/icons';
	import { indicator, materialize, scrim } from '$lib/ui/motion';
	import Vial from '$lib/ui/Vial.svelte';

	let { open = $bindable(false) }: { open: boolean } = $props();

	type Group = 'go' | 'do' | 'workshops' | 'runs' | 'tickets';
	const groupLabel = (g: Group) =>
		g === 'go'
			? t('shell.palette.goTo')
			: g === 'do'
				? t('shell.palette.do')
				: g === 'workshops'
					? t('shell.palette.workshops')
					: g === 'runs'
						? t('shell.palette.runs')
						: t('shell.palette.tickets');

	interface Item {
		group: Group;
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
			.then((list) => (tickets = list))
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
				group: 'go' as const,
				label: n.label,
				glyph: n.icon,
				sub:
					n.id === 'inbox'
						? t('shell.palette.pending', { count: fleet.pending.length })
						: n.chord
							? `G ${n.chord.toUpperCase()}`
							: undefined,
				run: go(n.href())
			})),
			{
				group: 'go' as const,
				label: t('shell.palette.integrations'),
				glyph: I.integrations,
				run: go(to.system('integrations'))
			},
			{
				group: 'go' as const,
				label: t('shell.palette.channels'),
				glyph: I.channels,
				run: go(to.system('channels'))
			},
			{
				group: 'go' as const,
				label: t('shell.palette.limits'),
				glyph: I.limits,
				run: go(to.system('limits'))
			},
			{
				group: 'do' as const,
				label: t('shell.palette.newTicket'),
				glyph: I.add,
				run: go(to.newTicket())
			},
			{
				group: 'do' as const,
				label: t('shell.palette.newWorkshop'),
				glyph: I.add,
				run: go(to.newWorkshop())
			},
			...THEMES.filter((th) => th.id !== prefs.theme).map((th) => ({
				group: 'do' as const,
				label: t('shell.palette.useTheme', { theme: th.label.toLowerCase() }),
				glyph: th.icon,
				run: act(() => prefs.setTheme(th.id))
			})),
			{
				group: 'do' as const,
				label: prefs.collapsed ? t('shell.sidebar.expand') : t('shell.sidebar.collapse'),
				sub: '⌘\\',
				glyph: prefs.collapsed ? I.expand : I.collapse,
				run: act(() => prefs.toggleCollapsed())
			}
		];
		for (const p of fleet.projects)
			out.push({
				group: 'workshops' as const,
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
				group: 'runs' as const,
				label: r.title,
				sub: `${shortId(r.id)} · ${stateLabel[r.state]}`,
				glyph: I.runs,
				run: go(to.run(r.id))
			});
		for (const tk of tickets.slice(0, 80))
			out.push({
				group: 'tickets' as const,
				label: tk.title,
				sub: tk.source?.key ?? fleet.projectName(tk.projectId),
				glyph: I.tickets,
				run: go(to.ticket(tk.id))
			});
		return out;
	});

	const shown = $derived.by(() => {
		const q = query.trim().toLowerCase();
		if (!q)
			return items
				.filter((i) => i.group === 'go' || i.group === 'do' || i.group === 'runs')
				.slice(0, 24);
		const words = q.split(/\s+/);
		return items
			.filter((i) => {
				const hay = `${i.label} ${i.sub ?? ''} ${groupLabel(i.group)}`.toLowerCase();
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
			aria-label={t('shell.palette.aria')}
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
					placeholder={t('shell.palette.placeholder')}
					aria-label={t('shell.palette.search')}
				/>
				<span class="kbd">esc</span>
			</div>
			<div class="list" role="listbox" use:indicator>
				{#each shown as it, i (it.group + it.label + i)}
					{#if i === 0 || shown[i - 1].group !== it.group}
						<div class="gh">{groupLabel(it.group)}</div>
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
					<div class="empty-line none">{t('shell.palette.none', { query })}</div>
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
