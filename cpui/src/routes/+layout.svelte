<script lang="ts">
	// The app shell (DESIGN §5): the scene and its sigils behind everything,
	// the sidebar, the main column the pages fill, and the cursor light on top.
	import '../app.css';
	import { goto, afterNavigate } from '$app/navigation';
	import { onMount } from 'svelte';
	import { fleet } from '$lib/fleet.svelte';
	import { prefs } from '$lib/prefs.svelte';
	import { to } from '$lib/guild';
	import Sigils from '$lib/ui/Sigils.svelte';
	import ConfirmHost from '$lib/ui/ConfirmHost.svelte';
	import Sidebar from '$lib/shell/Sidebar.svelte';
	import CommandPalette from '$lib/shell/CommandPalette.svelte';
	import { page } from '$app/state';

	let { children } = $props();

	let root = $state<HTMLDivElement | null>(null);
	let paletteOpen = $state(false);
	let drawerOpen = $state(false);

	onMount(() => {
		document.documentElement.dataset.theme = prefs.theme;
		const stop = fleet.use();

		// "G then I/R/T/D" chords: the G arms for a second.
		let gArmed = 0;
		const key = (e: KeyboardEvent) => {
			const mod = e.metaKey || e.ctrlKey;
			if (mod && e.key.toLowerCase() === 'k') {
				e.preventDefault();
				paletteOpen = !paletteOpen;
				return;
			}
			if (mod && e.key === '\\') {
				e.preventDefault();
				prefs.toggleCollapsed();
				return;
			}
			const t = e.target as HTMLElement | null;
			if (mod || e.altKey || t?.closest('input, textarea, select, [contenteditable], .xterm'))
				return;
			if (e.key === 'g' || e.key === 'G') {
				gArmed = Date.now();
				return;
			}
			if (gArmed && Date.now() - gArmed < 1000) {
				const dest = { i: to.inbox(), r: to.runs(), t: to.tickets(), d: to.desk() }[
					e.key.toLowerCase()
				];
				gArmed = 0;
				if (dest) {
					e.preventDefault();
					void goto(dest);
				}
			}
		};
		window.addEventListener('keydown', key);
		return () => {
			stop();
			window.removeEventListener('keydown', key);
		};
	});

	afterNavigate(() => (drawerOpen = false));

	function light(e: MouseEvent) {
		root?.style.setProperty('--lx', `${e.clientX}px`);
		root?.style.setProperty('--ly', `${e.clientY}px`);
	}

	const tabs = $derived([
		{ label: 'Desk', icon: '◈', href: to.desk(), on: page.url.pathname === to.desk() },
		{
			label: 'Inbox',
			icon: '✉',
			href: to.inbox(),
			on: page.url.pathname.startsWith(to.inbox()),
			n: fleet.pending.length
		},
		{ label: 'Runs', icon: '▶', href: to.runs(), on: page.url.pathname.includes('/runs') },
		{ label: 'Tickets', icon: '☰', href: to.tickets(), on: page.url.pathname.includes('/tickets') }
	]);
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="app" bind:this={root} onmousemove={light}>
	<Sigils />
	<div class="shell">
		<div class="side-slot">
			<Sidebar onsearch={() => (paletteOpen = true)} />
		</div>
		<main class="main">
			{@render children()}
		</main>
	</div>
	<div class="cursor-light" aria-hidden="true"></div>

	<nav class="mtabs lg" aria-label="Primary">
		{#each tabs as t (t.label)}
			<a class:on={t.on} href={t.href}
				><span class="mi">{t.icon}{'︎'}</span>{t.label}{#if t.n}<span class="count hot">{t.n}</span
					>{/if}</a
			>
		{/each}
		<button onclick={() => (drawerOpen = true)}><span class="mi">≡</span>More</button>
	</nav>
	{#if drawerOpen}
		<!-- svelte-ignore a11y_click_events_have_key_events -->
		<div class="drawer-scrim" onclick={(e) => e.target === e.currentTarget && (drawerOpen = false)}>
			<Sidebar
				drawer
				onsearch={() => (paletteOpen = true)}
				onnavigate={() => (drawerOpen = false)}
			/>
		</div>
	{/if}

	<CommandPalette bind:open={paletteOpen} />
	<ConfirmHost />
</div>

<style>
	.app {
		position: fixed;
		inset: 0;
		display: flex;
		flex-direction: column;
		overflow: hidden;
		color: var(--tx);
		background: var(--scene);
	}
	.shell {
		position: relative;
		z-index: 1;
		flex: 1;
		min-height: 0;
		display: flex;
	}
	.side-slot {
		display: flex;
		min-height: 0;
	}
	.main {
		position: relative;
		flex: 1;
		min-width: 0;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	.cursor-light {
		position: absolute;
		inset: 0;
		z-index: 40;
		pointer-events: none;
		mix-blend-mode: screen;
		background: radial-gradient(
			360px circle at var(--lx, 62%) var(--ly, 18%),
			var(--light),
			transparent 65%
		);
	}

	/* Mobile: the sidebar becomes a drawer and the rail a bottom tab bar. */
	.mtabs {
		display: none;
	}
	.drawer-scrim {
		position: fixed;
		inset: 0;
		z-index: 55;
		background: var(--scrim);
		display: flex;
	}
	@media (max-width: 760px) {
		.side-slot {
			display: none;
		}
		.main {
			padding-bottom: 64px;
		}
		.mtabs {
			display: flex;
			position: fixed;
			z-index: 20;
			left: 8px;
			right: 8px;
			bottom: 8px;
			border-radius: 18px;
			padding: 4px;
			justify-content: space-around;
		}
		.mtabs > a,
		.mtabs > button {
			all: unset;
			cursor: pointer;
			min-height: 44px;
			flex: 1;
			display: flex;
			flex-direction: column;
			align-items: center;
			justify-content: center;
			gap: 1px;
			font-size: 10.5px;
			color: var(--mut);
			border-radius: 13px;
			position: relative;
		}
		.mtabs > a.on {
			background: var(--sel);
			box-shadow:
				inset 0 0 0 1px var(--selLn),
				var(--selSh);
			color: var(--tx);
		}
		.mtabs .mi {
			font-size: 16px;
		}
		.mtabs .count {
			position: absolute;
			top: 3px;
			right: 18%;
			font-size: 9.5px;
			padding: 0 5px;
		}
	}
</style>
