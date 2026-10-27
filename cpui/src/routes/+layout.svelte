<script lang="ts">
	// The app shell (DESIGN §5): the scene and its sigils behind everything,
	// the sidebar, the main column the pages fill, and the cursor light on top.
	// On a phone the sidebar becomes a sheet (flick it away) and the nav a
	// bottom tab bar whose bead slides between tabs.
	//
	// Before any of it: who is here (TODO 25). The session check decides
	// between the setup page (no account yet), the sign-in page, and the app;
	// a 401 later drops back to sign-in in place, so a deep link survives.
	import '../app.css';
	import { goto, afterNavigate } from '$app/navigation';
	import { onMount } from 'svelte';
	import { fleet } from '$lib/fleet.svelte';
	import { prefs } from '$lib/prefs.svelte';
	import { session } from '$lib/session.svelte';
	import { i18n, t } from '$lib/i18n/index.svelte';
	import { NAV, navByChord } from '$lib/nav';
	import Sigils from '$lib/ui/Sigils.svelte';
	import ConfirmHost from '$lib/ui/ConfirmHost.svelte';
	import Toaster from '$lib/ui/Toaster.svelte';
	import Sheet from '$lib/ui/Sheet.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import { I } from '$lib/ui/icons';
	import { bump, indicator } from '$lib/ui/motion';
	import Sidebar from '$lib/shell/Sidebar.svelte';
	import CommandPalette from '$lib/shell/CommandPalette.svelte';
	import SignIn from '$lib/shell/SignIn.svelte';
	import Setup from '$lib/shell/Setup.svelte';
	import { page } from '$app/state';

	let { children } = $props();

	let root = $state<HTMLDivElement | null>(null);
	let paletteOpen = $state(false);
	let drawerOpen = $state(false);

	onMount(() => {
		document.documentElement.dataset.theme = prefs.theme;
		void i18n.init();
		void session.load();
	});

	// The fleet polls only while someone is signed in, and starts over for
	// whoever signs in next.
	$effect(() => {
		if (!session.user) return;
		fleet.reset();
		const stop = fleet.use();
		return stop;
	});

	onMount(() => {
		// "G then I/R/T/D" chords (from $lib/nav): the G arms for a second.
		let gArmed = 0;
		const key = (e: KeyboardEvent) => {
			if (!session.user) return;
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
			const el = e.target as HTMLElement | null;
			if (mod || e.altKey || el?.closest('input, textarea, select, [contenteditable], .xterm'))
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
		return () => window.removeEventListener('keydown', key);
	});

	afterNavigate(() => (drawerOpen = false));

	function light(e: MouseEvent) {
		root?.style.setProperty('--lx', `${e.clientX}px`);
		root?.style.setProperty('--ly', `${e.clientY}px`);
	}

	const tabs = NAV.filter((n) => n.mobile);
</script>

{#if session.error}
	<div class="gate">
		<ErrorCard cause={t('auth.unreachable')} detail={session.error} onretry={() => session.load()} />
	</div>
{:else if !session.ready}
	<div class="gate"><Loading /></div>
{:else if session.setupRequired}
	<Setup />
{:else if !session.user}
	<SignIn />
{:else}
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

		<nav class="mtabs lg" aria-label={t('page.layout.primary')} use:indicator>
			{#each tabs as tab (tab.id)}
				{@const on = tab.match(page.url.pathname)}
				{@const n = tab.id === 'inbox' ? fleet.pending.length : 0}
				<a class:on href={tab.href()} aria-current={on ? 'page' : undefined}
					><span class="ti">{tab.icon}</span>{tab.label}{#if tab.id === 'inbox'}<span
							class="count hot sm"
							class:gone={!n}
							use:bump={n}>{n}</span
						>{/if}</a
				>
			{/each}
			<button onclick={() => (drawerOpen = true)}
				><span class="ti">{I.more}</span>{t('page.layout.more')}</button
			>
		</nav>
		<Sheet bind:open={drawerOpen} side="left" label={t('page.layout.navigation')}>
			<Sidebar drawer onsearch={() => (paletteOpen = true)} onnavigate={() => (drawerOpen = false)} />
		</Sheet>

		<CommandPalette bind:open={paletteOpen} />
	</div>
{/if}
<ConfirmHost />
<Toaster />

<style>
	.gate {
		position: fixed;
		inset: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 16px;
		background: var(--scene);
		color: var(--tx);
	}
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
		}
	}
</style>
