<script lang="ts">
	// The app shell (DESIGN §5): the scene and its sigils behind everything,
	// the sidebar, the main column the pages fill, and the cursor light on top.
	// On a phone the sidebar becomes a sheet (flick it away) and the nav a
	// bottom tab bar whose bead slides between tabs.
	import '../app.css';
	import { goto, afterNavigate } from '$app/navigation';
	import { onMount } from 'svelte';
	import { fleet } from '$lib/fleet.svelte';
	import { prefs } from '$lib/prefs.svelte';
	import { NAV, navByChord } from '$lib/nav';
	import Sigils from '$lib/ui/Sigils.svelte';
	import ConfirmHost from '$lib/ui/ConfirmHost.svelte';
	import Toaster from '$lib/ui/Toaster.svelte';
	import Sheet from '$lib/ui/Sheet.svelte';
	import { I } from '$lib/ui/icons';
	import { bump, indicator } from '$lib/ui/motion';
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

		// "G then I/R/T/D" chords (from $lib/nav): the G arms for a second.
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
				const dest = navByChord(e.key);
				gArmed = 0;
				if (dest) {
					e.preventDefault();
					void goto(dest.href());
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

	const tabs = NAV.filter((n) => n.mobile);
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

	<nav class="mtabs lg" aria-label="Primary" use:indicator>
		{#each tabs as t (t.id)}
			{@const on = t.match(page.url.pathname)}
			{@const n = t.id === 'inbox' ? fleet.pending.length : 0}
			<a class:on href={t.href()} aria-current={on ? 'page' : undefined}
				><span class="ti">{t.icon}</span>{t.label}{#if t.id === 'inbox'}<span
						class="count hot"
						class:gone={!n}
						use:bump={n}>{n}</span
					>{/if}</a
			>
		{/each}
		<button onclick={() => (drawerOpen = true)}><span class="ti">{I.more}</span>More</button>
	</nav>
	<Sheet bind:open={drawerOpen} side="left" label="Navigation">
		<Sidebar drawer onsearch={() => (paletteOpen = true)} onnavigate={() => (drawerOpen = false)} />
	</Sheet>

	<CommandPalette bind:open={paletteOpen} />
	<ConfirmHost />
	<Toaster />
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
		z-index: var(--z-raise);
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
		z-index: var(--z-light);
		pointer-events: none;
		mix-blend-mode: screen;
		background: radial-gradient(
			360px circle at var(--lx, 62%) var(--ly, 18%),
			var(--light),
			transparent 65%
		);
	}

	/* Mobile: the sidebar becomes a sheet and the rail a bottom tab bar. */
	.mtabs {
		display: none;
	}
	.gone {
		display: none;
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
			z-index: var(--z-float);
			left: 8px;
			right: 8px;
			bottom: 8px;
			border-radius: var(--r-group);
			padding: 4px;
			justify-content: space-around;
		}
		.mtabs > :global(.ind) {
			border-radius: var(--r-card);
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
			font-size: var(--fs-id);
			color: var(--mut);
			border-radius: var(--r-card);
			position: relative;
			-webkit-tap-highlight-color: transparent;
		}
		.mtabs > a:active,
		.mtabs > button:active {
			opacity: 0.7;
		}
		.mtabs > a.on {
			color: var(--tx);
			font-weight: 600;
		}
		.ti {
			font-size: var(--fs-title);
		}
		.mtabs .count {
			position: absolute;
			top: 3px;
			right: 18%;
			min-width: 0;
			font-size: var(--fs-badge);
			padding: 0 5px;
		}
	}
</style>
