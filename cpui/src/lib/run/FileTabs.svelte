<script lang="ts">
	// The open files of the Files pane, as a tab strip (DESIGN §7.1): click to
	// show one, × or a middle click to close it, drag it or Alt+Shift+←/→ to
	// move it. A dot marks a tab whose file changed while it was hidden; a
	// tab whose file is gone is struck through. More tabs than room scroll
	// sideways. Presentational: the caller owns the tabs and their order.
	import { tick } from 'svelte';
	import type { FileLoc, RunCheckout } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import { indicator } from '$lib/ui/motion';
	import { keepInView } from '$lib/ui/inview';

	let {
		tabs,
		active,
		checkouts,
		onactivate,
		onclose,
		onmove
	}: {
		tabs: { key: string; loc: FileLoc; stale: boolean; deleted: boolean }[];
		active: string | null;
		checkouts: RunCheckout[];
		onactivate: (key: string) => void;
		onclose: (key: string) => void;
		onmove: (from: number, to: number) => void;
	} = $props();

	let strip = $state<HTMLElement>();
	/** The tab being dragged, and the one it would land on. */
	let from = $state<number | null>(null);
	let over = $state<number | null>(null);

	const multi = $derived(checkouts.length > 1);
	const primary = $derived(checkouts.find((c) => c.primary)?.name ?? '');
	/** The tab a keyboard user lands on: the active one, else the first. */
	const stop = $derived(
		Math.max(
			0,
			tabs.findIndex((tab) => tab.key === active)
		)
	);

	/** What the tab says, and the full reference its tooltip gives. Checkout
	 *  files show their name, after the checkout's on a multi-repo run;
	 *  absolute files show the whole path. */
	function names(loc: FileLoc): { label: string; ref: string } {
		if ('abs' in loc) return { label: loc.abs, ref: loc.abs };
		const base = loc.path.slice(loc.path.lastIndexOf('/') + 1);
		if (!multi) return { label: base, ref: loc.path };
		const repo = loc.repo || primary;
		return { label: `${repo}:${base}`, ref: `${repo}:${loc.path}` };
	}

	const tabEls = () => [...(strip?.querySelectorAll<HTMLElement>('[role="tab"]') ?? [])];

	function keys(e: KeyboardEvent, i: number) {
		const key = tabs[i].key;
		if (e.key === 'Enter' || e.key === ' ') {
			e.preventDefault();
			onactivate(key);
			return;
		}
		const step = e.key === 'ArrowLeft' ? -1 : e.key === 'ArrowRight' ? 1 : 0;
		if (e.altKey && e.shiftKey) {
			if (!step) return;
			e.preventDefault();
			const to = i + step;
			if (to < 0 || to >= tabs.length) return;
			onmove(i, to);
			// The moved tab keeps the focus, wherever it lands.
			void tick().then(() =>
				tabEls()
					.find((el) => el.dataset.key === key)
					?.focus()
			);
			return;
		}
		const n = tabs.length;
		const to = step ? (i + step + n) % n : e.key === 'Home' ? 0 : e.key === 'End' ? n - 1 : -1;
		if (to < 0) return;
		e.preventDefault();
		onactivate(tabs[to].key);
		tabEls()[to]?.focus();
	}

	function dragStart(e: DragEvent, i: number) {
		from = i;
		if (e.dataTransfer) {
			e.dataTransfer.effectAllowed = 'move';
			// Firefox starts a drag only with some data set.
			e.dataTransfer.setData('text/plain', tabs[i].key);
		}
	}

	function dragOver(e: DragEvent, i: number) {
		if (from === null) return;
		e.preventDefault();
		over = i;
	}

	function drop(e: DragEvent, i: number) {
		e.preventDefault();
		if (from !== null && from !== i) onmove(from, i);
		from = over = null;
	}

	function close(e: MouseEvent, key: string) {
		e.stopPropagation();
		onclose(key);
	}
</script>

<div
	class="tabrow"
	role="tablist"
	aria-label={t('run.files.tabs.aria')}
	bind:this={strip}
	use:indicator
	use:keepInView
>
	{#each tabs as tab, i (tab.key)}
		{@const n = names(tab.loc)}
		<div
			class="tab mono"
			class:on={tab.key === active}
			class:gone={tab.deleted}
			class:ghosted={from === i}
			class:before={over === i && from !== null && from > i}
			class:after={over === i && from !== null && from < i}
			role="tab"
			tabindex={i === stop ? 0 : -1}
			aria-selected={tab.key === active}
			aria-label={[
				n.label,
				tab.stale && t('run.files.tabs.changed'),
				tab.deleted && t('run.files.tabs.deleted')
			]
				.filter(Boolean)
				.join(', ')}
			title={`${n.ref}\n${t('run.files.tabs.hint')}`}
			data-key={tab.key}
			draggable="true"
			onclick={() => onactivate(tab.key)}
			onkeydown={(e) => keys(e, i)}
			onmousedown={(e) => e.button === 1 && e.preventDefault()}
			onauxclick={(e) => e.button === 1 && close(e, tab.key)}
			ondragstart={(e) => dragStart(e, i)}
			ondragover={(e) => dragOver(e, i)}
			ondrop={(e) => drop(e, i)}
			ondragend={() => (from = over = null)}
		>
			<span class="label">{n.label}</span>
			{#if tab.stale}<span class="dot" aria-hidden="true">{I.dot}</span>{/if}
			<button
				type="button"
				class="btn-icon btn-sm x"
				tabindex="-1"
				aria-label={t('run.files.tabs.close', { name: n.label })}
				title={t('run.files.tabs.close', { name: n.label })}
				onkeydown={(e) => e.stopPropagation()}
				onclick={(e) => close(e, tab.key)}>{I.close}</button
			>
		</div>
	{/each}
</div>

<style>
	/* More tabs than room: the row scrolls sideways, without a bar. */
	.tabrow {
		flex: none;
		min-width: 0;
		display: flex;
		align-items: center;
		gap: 2px;
		padding: 4px 6px;
		border-bottom: 1px solid var(--line);
		overflow-x: auto;
		overflow-y: hidden;
		scrollbar-width: none;
		overscroll-behavior-x: contain;
	}
	.tabrow::-webkit-scrollbar {
		display: none;
	}
	.tab {
		flex: none;
		cursor: pointer;
		user-select: none;
		white-space: nowrap;
		padding: 3px 4px 3px 10px;
		border-radius: var(--r-tab);
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: var(--fs-ui);
		color: var(--text-muted);
		outline-offset: -2px;
		/* Room for the drop marker either side. */
		border-inline: 2px solid transparent;
	}
	.tab:hover {
		color: var(--text);
	}
	.tab.on {
		color: var(--text);
		font-weight: 600;
	}
	.tab.gone .label {
		text-decoration: line-through;
		opacity: var(--o-disabled);
	}
	.tab.ghosted {
		opacity: var(--o-ghost);
	}
	/* Where a dragged tab would land. */
	.tab.before {
		border-inline-start-color: var(--accent);
	}
	.tab.after {
		border-inline-end-color: var(--accent);
	}
	.dot {
		color: var(--attention);
		font-size: var(--fs-meta);
	}
	/* A small .btn-icon, a size down to sit in the tab. */
	.x {
		width: 17px;
		height: 17px;
		font-size: var(--fs-body);
		color: var(--text-faint);
	}
</style>
