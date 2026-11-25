<script lang="ts">
	// The assistant: its squircle in the corner at the bottom right
	// (FabCorner), on every page; what it opens floats over the page and never
	// takes its room. It unfolds from the same spot:
	//
	//   - closed, the button, in the page mode's accent while the page on
	//     screen can be filled; a pill while it has something to say (an
	//     answer being written, a fill that just landed);
	//   - open, a card beside it as tall as what it shows: the mode, the latest
	//     exchange, the box to ask in;
	//   - expanded, a drawer with the whole conversation.
	//
	// Clicking into the page folds it back to the button, so what a fill
	// changed is always in view; what was typed stays. Two modes that never
	// mix:
	//
	//   - This page: about the page on screen, when it offers itself
	//     (offerPage). It fills the page, unsaved and undoable, and never
	//     acts. The accent colour, the page's name and its own conversation.
	//   - Everywhere: the person's general conversation about the workshop
	//     on screen. It reads everything and acts once they confirm; it never
	//     touches the page. Neutral, and it follows them from page to page.
	//
	// Each mode keeps its own conversation and its own draft: switching
	// carries nothing across but what the person carries (Ask in Everywhere).
	// ⌘J opens it, switches modes, and closes it with one mode; Esc closes it.
	import { onDestroy, onMount, tick, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { to } from '$lib/core/urls';
	import { I, SPARKLE_PATH } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import Segmented, { type SegOption } from '$lib/ui/Segmented.svelte';
	import AssistantPanel from './AssistantPanel.svelte';
	import { assistant, pageKey, type AssistantMode } from './assistant.svelte';
	import { Conversation } from './conversation.svelte';
	import { fillHook } from './fillGuard.svelte';

	const general = new Conversation({
		kind: 'general',
		subject: 'palette',
		workshop: untrack(() => page.params.workshop ?? ''),
		// Where the person is, so "this run" means the one on screen.
		pageState: () => ({ page: page.url.pathname })
	});

	/** The page's conversation: opened the first time its mode shows, kept
	 *  while the page stays (the panel closed or not), ended when it goes. */
	let paged = $state<{ key: string; convo: Conversation; end: () => void } | null>(null);
	const key = $derived(assistant.page ? pageKey(assistant.page) : '');
	const wanted = $derived(assistant.open && assistant.mode === 'page');
	$effect(() => {
		const k = key;
		const want = wanted;
		untrack(() => {
			if (paged && paged.key !== k) {
				paged.end();
				paged = null;
			}
			const target = assistant.page;
			if (!k || !want || paged || !target) return;
			const hook = fillHook(target.guard);
			const convo = new Conversation({
				kind: target.kind,
				subject: target.subject,
				workshop: target.workshop,
				pageState: () => assistant.page?.pageState(),
				fill: hook
			});
			convo.connect();
			void convo.load();
			paged = {
				key: k,
				convo,
				end: () => {
					// The page is gone: an answer still on its way has nowhere to land.
					if (convo.busy) void convo.cancel();
					convo.disconnect();
					hook.end();
				}
			};
		});
	});

	/** Loaded and off: the button looks it, the panel says why. */
	const off = $derived(!!general.status && !general.status.enabled);
	const pageWord = $derived(
		assistant.page ? t(`assistant.page.${assistant.page.kind}`).toLocaleLowerCase() : ''
	);
	/** A page's name as the mode's tab holds it. */
	const short = (name: string) => (name.length > 22 ? `${name.slice(0, 21)}…` : name);
	const modes = $derived<SegOption<AssistantMode>[]>([
		...(assistant.page
			? [
					{
						value: 'page' as const,
						label: `${I.thisPage} ${t('assistant.mode.page', { page: pageWord })}${assistant.page.name ? ` · ${short(assistant.page.name)}` : ''}`,
						title: t('assistant.mode.pageTip', { page: pageWord }),
						tone: 'accent' as const
					}
				]
			: []),
		{
			value: 'general' as const,
			label: `${I.sparkle} ${t('assistant.mode.general')}`,
			title: t('assistant.mode.generalTip')
		}
	]);
	const convo = $derived(assistant.mode === 'page' && paged ? paged.convo : general);

	let button = $state<HTMLButtonElement | null>(null);
	let dock = $state<HTMLElement | null>(null);

	/** Once opened, the panel stays mounted, hidden while closed: what was
	 *  typed and where it was scrolled stay. */
	let opened = $state(false);
	$effect(() => {
		if (assistant.open) opened = true;
	});

	/** How long the pill says what a fill changed. */
	const FLASH_MS = 8000;
	let flash = $state('');
	let flashTimer: ReturnType<typeof setTimeout> | undefined;
	$effect(() => {
		const changed = assistant.page?.guard.changed ?? [];
		clearTimeout(flashTimer);
		if (!changed.length) {
			flash = '';
			return;
		}
		flash = t('assistant.changed', { fields: changed.join(', ') });
		flashTimer = setTimeout(() => (flash = ''), FLASH_MS);
	});
	onDestroy(() => clearTimeout(flashTimer));
	/** What the closed button says: nothing, or what is going on. */
	const pill = $derived.by(() => {
		if (off) return '';
		if (paged?.convo.busy) return t('assistant.brewing', { page: pageWord });
		if (general.busy) return t('assistant.thinking');
		return flash;
	});

	/** A click on the page folds the panel back: the page is the person's
	 *  again. Not one in it, on the button, or in what it opened over the
	 *  page (a dropdown list, a menu, a dialog). */
	function outside(e: PointerEvent) {
		if (!assistant.open) return;
		const el = e.target instanceof Element ? e.target : null;
		if (!el || dock?.contains(el) || button?.contains(el)) return;
		if (el.closest('[role=listbox], [role=option], [role=menu], [role=dialog], [role=alertdialog]'))
			return;
		assistant.close();
	}

	onMount(() => {
		// The stream first: the session opens once it follows.
		general.connect();
		void general.load();
		const key = (e: KeyboardEvent) => {
			if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'j') {
				e.preventDefault();
				assistant.toggle();
				return;
			}
			// Esc closes it from inside it, or from nowhere in particular; an
			// open menu or dialog takes its own Esc first.
			const at = document.activeElement;
			if (
				e.key === 'Escape' &&
				!e.defaultPrevented &&
				assistant.open &&
				(!at || at === document.body || dock?.contains(at))
			) {
				e.preventDefault();
				close();
			}
		};
		window.addEventListener('keydown', key);
		window.addEventListener('pointerdown', outside, true);
		return () => {
			window.removeEventListener('keydown', key);
			window.removeEventListener('pointerdown', outside, true);
		};
	});
	onDestroy(() => {
		general.disconnect();
		paged?.end();
	});

	// Another workshop's pages: its own general conversation.
	$effect(() => {
		const w = page.params.workshop ?? '';
		untrack(() => void general.retarget(w));
	});

	// Opened, or switched: the person writes at once.
	$effect(() => {
		if (!assistant.open) return;
		void assistant.mode;
		void tick().then(() =>
			dock?.querySelector<HTMLTextAreaElement>('.mode-on textarea:not([disabled])')?.focus()
		);
	});

	function close() {
		assistant.close();
		void tick().then(() => button?.focus());
	}

	function settings() {
		assistant.close();
		void goto(to.system('assistant'));
	}
</script>

<button
	type="button"
	class="fab ga"
	class:off
	class:paged={!!assistant.page && !off}
	class:pill={!!pill && !assistant.open}
	bind:this={button}
	aria-label={t('assistant.button')}
	aria-expanded={assistant.open}
	title={off
		? t('assistant.buttonOff')
		: assistant.page
			? t('assistant.buttonPage', { page: pageWord })
			: t('assistant.buttonTip')}
	aria-describedby={off ? 'ga-off' : pill && !assistant.open ? 'ga-says' : undefined}
	onclick={() => (assistant.open ? close() : assistant.show())}
>
	<!-- Keyed by the page: arriving on a page it can fill plays the
	     twinkle and the sheen once. -->
	{#key key}
		<svg class="star" viewBox="0 0 24 24" aria-hidden="true"><path d={SPARKLE_PATH} /></svg>
		{#if assistant.page && !off}<span class="sheen" aria-hidden="true"></span>{/if}
	{/key}
	{#if pill && !assistant.open}<span class="says" id="ga-says">{pill}</span>{/if}
</button>
{#if off}<span id="ga-off" hidden>{t('assistant.buttonOff')}</span>{/if}

{#if opened}
	<aside
		class="dock island col"
		class:paged={assistant.mode === 'page'}
		class:expanded={assistant.expanded}
		aria-label={t('assistant.title')}
		hidden={!assistant.open}
		bind:this={dock}
	>
		<div class="head row nowrap">
			<Segmented
				options={modes}
				value={assistant.mode}
				onchange={(m) => (assistant.chosen = m)}
				size="sm"
				glass={false}
				label={t('assistant.mode.label')}
			/>
			<span class="grow"></span>
			<button
				type="button"
				class="btn-icon"
				aria-label={assistant.expanded ? t('assistant.restore') : t('assistant.expand')}
				title={assistant.expanded ? t('assistant.restore') : t('assistant.expand')}
				aria-pressed={assistant.expanded}
				onclick={() => (assistant.expanded = !assistant.expanded)}
				>{assistant.expanded ? I.restore : I.maximise}</button
			>
			<!-- Not while an answer is written: its turn would go on unseen;
			     Stop ends it first. -->
			<button
				type="button"
				class="btn-icon"
				aria-label={t('assistant.newConversation')}
				title={t('assistant.newConversation')}
				onclick={() => convo.load({ fresh: true })}
				disabled={convo.busy || convo.switching}
				aria-busy={convo.switching}>{I.add}</button
			>
			<button
				type="button"
				class="btn-icon"
				aria-label={t('assistant.settings')}
				title={t('assistant.settings')}
				onclick={settings}>{I.system}</button
			>
			<button
				type="button"
				class="btn-icon"
				aria-label={t('assistant.close')}
				title={t('assistant.closeTip')}
				onclick={close}>{I.close}</button
			>
		</div>
		<p class="scope small">
			{assistant.mode === 'page'
				? t('assistant.mode.pageScope', { page: pageWord })
				: t('assistant.mode.generalScope')}
		</p>
		{#if paged && assistant.page}
			<div
				class="mode col"
				class:mode-on={assistant.mode === 'page'}
				hidden={assistant.mode !== 'page'}
			>
				<AssistantPanel
					convo={paged.convo}
					target={assistant.page}
					compact={!assistant.expanded}
					onexpand={() => (assistant.expanded = true)}
				/>
			</div>
		{/if}
		<div
			class="mode col"
			class:mode-on={assistant.mode === 'general'}
			hidden={assistant.mode !== 'general'}
		>
			<AssistantPanel
				convo={general}
				compact={!assistant.expanded}
				onexpand={() => (assistant.expanded = true)}
			/>
		</div>
	</aside>
{/if}

<style>
	/* The squircle of the corner (.fab), its glyph the star. It never moves:
	   open, it stays as the toggle beside the card. */
	.ga {
		overflow: hidden;
		transition:
			background var(--dur-fade) var(--ease-out),
			border-color var(--dur-fade) var(--ease-out),
			color var(--dur-fade) var(--ease-out);
	}
	.ga .star {
		width: 20px;
		height: 20px;
		fill: currentColor;
	}
	.ga.off {
		color: var(--text-faint);
	}
	/* The page on screen can be filled: the squircle wears the page mode's
	   accent, and arriving there the star twinkles and a sheen crosses it,
	   once. Everywhere is the corner's own sky. */
	.ga.paged {
		background: linear-gradient(var(--accent-bg), var(--accent-bg)), var(--island-bg);
		border-color: var(--accent-line);
		color: var(--accent);
	}
	.ga.paged:hover,
	.ga.paged[aria-expanded='true'] {
		box-shadow:
			var(--specular),
			var(--shadow-float),
			0 0 var(--glow-md) var(--accent-glow);
	}
	.ga.paged .star {
		animation: twinkle calc(var(--dur-spring) * 2) var(--ease-out) 1;
	}
	.sheen {
		position: absolute;
		inset: 0;
		border-radius: inherit;
		pointer-events: none;
		background: linear-gradient(
			115deg,
			transparent 30%,
			color-mix(in oklch, var(--accent) 45%, transparent) 50%,
			transparent 70%
		);
		transform: translateX(-120%);
		animation: sheen calc(var(--dur-spring) * 2.4) var(--ease-out) var(--dur-fade) 1 forwards;
	}
	@keyframes twinkle {
		35% {
			transform: scale(1.25) rotate(18deg);
		}
	}
	@keyframes sheen {
		to {
			transform: translateX(120%);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.ga {
			transition: none;
		}
		.ga.paged .star {
			animation: none;
		}
		.sheen {
			display: none;
		}
	}

	/* The pill: the button saying what is going on, growing leftwards from
	   the corner for a while. */
	.ga.pill {
		width: auto;
		max-width: min(320px, calc(100vw - 2 * var(--fab-inset)));
		grid-auto-flow: column;
		gap: 8px;
		padding: 0 14px 0 10px;
	}
	.says {
		font-size: var(--fs-meta);
		color: var(--text);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}

	/* Over the page, never in its room: it unfolds leftwards from the
	   button, beside the corner's column (whose buttons stay in reach), as
	   tall as what it shows; expanded, a drawer the screen's height. */
	.dock {
		position: fixed;
		right: calc(var(--fab-inset) + var(--fab-size) + var(--fab-gap));
		bottom: var(--fab-inset);
		width: min(420px, calc(100vw - 2 * var(--shell-gap) - var(--fab-size) - var(--fab-gap)));
		max-height: min(70vh, 640px);
		padding: 12px;
		gap: 8px;
		border-radius: var(--r-pane);
		border-top: 2px solid transparent;
		transform-origin: bottom right;
		animation: unfold var(--dur-fade) var(--ease-out);
	}
	.dock[hidden] {
		display: none;
	}
	.dock.expanded {
		top: var(--shell-gap);
		bottom: var(--shell-gap);
		width: min(460px, calc(100vw - 2 * var(--shell-gap) - var(--fab-size) - var(--fab-gap)));
		max-height: none;
	}
	@keyframes unfold {
		from {
			opacity: 0;
			transform: scale(0.96) translateX(4px);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.dock {
			animation: none;
		}
	}
	/* This page's mode wears the accent: what it does lands on the page. */
	.dock.paged {
		border-top-color: var(--accent-line);
		box-shadow:
			var(--specular),
			var(--shadow-pop),
			0 0 var(--glow-lg) var(--accent-glow);
	}
	.head {
		gap: 4px;
		align-items: center;
		min-width: 0;
	}
	.scope {
		margin: 0;
		color: var(--text-muted);
	}
	.dock.paged .scope {
		color: var(--accent-text);
	}
	.mode {
		flex: 1 1 auto;
		min-height: 0;
		display: flex;
	}
	.mode[hidden] {
		display: none;
	}
	/* Narrow screens: open, it is the screen, above the tab bar. */
	@media (max-width: 760px) {
		.dock,
		.dock.expanded {
			inset: 8px 8px 72px 8px;
			width: auto;
			max-height: none;
		}
	}
</style>
