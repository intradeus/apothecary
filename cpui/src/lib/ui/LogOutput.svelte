<script lang="ts" module>
	/** How many of the newest lines are drawn, and how many more each step
	 *  up adds. A log is DOM a line at a time: 54,000 lines drawn at once were
	 *  five seconds of a frozen page (docs/notes, 2026-11-25). */
	export const LOG_WINDOW = 2000;
	/** Within this of the top, scrolling up draws the lines above. */
	const EARLIER_PX = 300;
</script>

<script lang="ts">
	// A program's output as lines (a run's logs, a service's tail): its URLs
	// open on a click, it follows the bottom as it grows, and its own toolbar
	// finds in it, copies all of it and jumps to either end. The parent's
	// controls go first in that toolbar (`bar`); `head` sits above the lines,
	// `placeholder` stands in while there is no text.
	//
	// It follows the bottom unless the reader scrolled up, holds a selection
	// in it (a tail landing mid-drag would carry the selection away) or is on
	// a match of the find.
	import { tick, untrack, type Snippet } from 'svelte';
	import Glyph from './Glyph.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import { linkify } from '$lib/core/links';
	import SearchInput from './SearchInput.svelte';
	import { toast } from './toast.svelte';
	import { findMatches, matchFrom, segmentLine, type Segment } from './logSegments';

	let {
		text,
		lineClass,
		fill = false,
		label,
		viewport = $bindable(null),
		bar,
		head,
		placeholder
	}: {
		text: string | null;
		/** A class for a line, a tint by its source say. */
		lineClass?: (line: string) => string;
		/** Fill the pane; otherwise a bounded height (a card). */
		fill?: boolean;
		/** The scroll box's accessible name. */
		label?: string;
		/** The scroll box, for a parent that keeps the reader's place. */
		viewport?: HTMLDivElement | null;
		bar?: Snippet;
		head?: Snippet;
		placeholder?: Snippet;
	} = $props();

	/** Within this of the bottom counts as at the bottom. */
	const FOLLOW_PX = 40;
	let stick = true;

	const lines = $derived((text ?? '').replace(/\n$/, '').split('\n'));

	// --- the drawn window -----------------------------------------------------
	//
	// Only the newest lines are drawn (`shown` of them, counted from the end).
	// Scrolling to the top, or the button there, draws the ones above and keeps
	// the reader's place. A tail that grows slides the window while the box
	// follows the bottom, and widens it while the reader is up the log, so
	// their lines stay put. Text put on top (earlier output a parent loaded)
	// is drawn: it was asked for. Anything else is a new log: its newest
	// window. Find draws out to the match it moves to; copy takes it all.
	let shown = $state(LOG_WINDOW);
	let seenText = '';
	let seenLines = 0;
	$effect.pre(() => {
		const now = text ?? '';
		const count = lines.length;
		untrack(() => {
			const before = seenText;
			const added = count - seenLines;
			seenText = now;
			seenLines = count;
			if (!before || !now || added < 0) shown = LOG_WINDOW;
			else if (now.length > before.length && now.endsWith(before)) shown += added;
			else if (now.startsWith(before)) {
				if (!stick) shown += added;
			} else shown = LOG_WINDOW;
		});
	});

	// A row is a line and its segments. A row nothing changed stays the same
	// object, so the list re-renders only the rows that did: a tail its last
	// rows, a step of the find the two lines its current match left and
	// reached. On a long log, re-rendering every row is seconds.
	interface Row {
		text: string;
		segs: Segment[];
	}
	/** A line's links are found once, while it stays the same. */
	let seen: Row[] = [];
	const base = $derived.by(() => {
		const out = lines.map((l, i) =>
			seen[i]?.text === l ? seen[i] : { text: l, segs: linkify(l) }
		);
		seen = out;
		return out;
	});

	// --- find ----------------------------------------------------------------

	let finding = $state(false);
	/** What the find field holds. */
	let query = $state('');
	/** What is searched for: the query once typing pauses, as a search of a long log is slow. */
	let sought = $state('');
	const SEEK_MS = 200;
	/** Where the current match starts: it stays that match while the tail grows. */
	let at = $state<{ line: number; start: number } | null>(null);
	let findRow = $state<HTMLDivElement | null>(null);

	$effect(() => {
		const q = query;
		const timer = setTimeout(() => (sought = q), SEEK_MS);
		return () => clearTimeout(timer);
	});

	const matches = $derived(finding ? findMatches(lines, sought) : []);
	const cur = $derived(matches.length ? (at ? matchFrom(matches, at.line, at.start) : 0) : -1);

	/**
	 * The matches either side of the current one that are drawn. The count
	 * covers every match; drawing them all is a DOM node each, and a broad
	 * query on a long log (or one long line) is seconds of that (docs/notes,
	 * 2026-11-14).
	 */
	const MARK_SPAN = 300;

	/** The drawn matches: [lo, hi), around the current one. */
	const span = $derived<[number, number]>(
		cur < 0 ? [0, 0] : [Math.max(0, cur - MARK_SPAN), Math.min(matches.length, cur + MARK_SPAN + 1)]
	);

	/** The drawn matches on the line of matches[i], from i on: its end. */
	function lineEnd(i: number): number {
		const line = matches[i].line;
		let end = i;
		while (end < span[1] && matches[end].line === line) end++;
		return end;
	}

	/** A marked row, by its line's row and where its marks are. */
	let marking = new Map<Row, { key: string; row: Row }>();
	const marked = $derived.by(() => {
		const next = new Map<Row, { key: string; row: Row }>();
		if (cur < 0) {
			marking = next;
			return base;
		}
		const out = base.slice();
		for (let i = span[0]; i < span[1];) {
			const end = lineEnd(i);
			const plain = base[matches[i].line];
			const marks = matches.slice(i, end);
			const key = marks.map((m) => `${m.start}:${m.end}`).join(',');
			const hit = marking.get(plain);
			const row =
				hit?.key === key ? hit.row : { text: plain.text, segs: segmentLine(plain.segs, marks) };
			next.set(plain, { key, row });
			out[matches[i].line] = row;
			i = end;
		}
		marking = next;
		return out;
	});

	/** The first line drawn. */
	const first = $derived(Math.max(0, lines.length - shown));

	/** Draw from `line` on, at least. */
	function draw(line: number) {
		if (line < first) shown = lines.length - Math.max(0, line - 50);
	}

	let extending = false;
	/** Draw the lines above, keeping what the reader sees where it is. */
	async function showEarlier() {
		if (extending || first === 0) return;
		extending = true;
		const height = viewport?.scrollHeight ?? 0;
		const top = viewport?.scrollTop ?? 0;
		shown += LOG_WINDOW;
		await tick();
		if (viewport) viewport.scrollTop = top + (viewport.scrollHeight - height);
		extending = false;
	}

	const rows = $derived.by(() => {
		if (cur < 0) return marked;
		const line = matches[cur].line;
		let first = cur;
		while (first > span[0] && matches[first - 1].line === line) first--;
		const out = marked.slice();
		out[line] = {
			text: base[line].text,
			segs: segmentLine(base[line].segs, matches.slice(first, lineEnd(cur)), cur - first)
		};
		return out;
	});

	async function reveal() {
		if (cur >= 0) draw(matches[cur].line);
		await tick();
		viewport?.querySelector('mark.current')?.scrollIntoView({ block: 'center' });
	}

	// A new search starts from its first match.
	$effect(() => {
		void sought;
		untrack(() => {
			const m = matches[0];
			at = m ? { line: m.line, start: m.start } : null;
			if (m) void reveal();
		});
	});

	function step(by: 1 | -1) {
		if (!matches.length) return;
		const m = matches[(cur + by + matches.length) % matches.length];
		at = { line: m.line, start: m.start };
		void reveal();
	}

	async function openFind() {
		finding = true;
		await tick();
		const input = findRow?.querySelector('input');
		input?.focus();
		input?.select();
	}

	function closeFind() {
		finding = false;
		query = sought = '';
		viewport?.focus();
	}

	function onkeydown(e: KeyboardEvent) {
		if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'f') {
			e.preventDefault();
			void openFind();
		} else if (finding && e.key === 'Escape') {
			// From anywhere inside, the scroll box included; handled here, so
			// the desk's Esc does not also restore the window.
			e.preventDefault();
			closeFind();
		} else if (
			finding &&
			e.key === 'Enter' &&
			e.target instanceof HTMLInputElement &&
			findRow?.contains(e.target)
		) {
			e.preventDefault();
			// Enter before the pause searches at once.
			if (sought !== query) sought = query;
			else step(e.shiftKey ? -1 : 1);
		}
	}

	// --- copy ----------------------------------------------------------------

	let copied = $state(false);
	let copyTimer: ReturnType<typeof setTimeout> | undefined;

	async function copy() {
		clearTimeout(copyTimer);
		copied = false;
		try {
			await navigator.clipboard.writeText(text ?? '');
			copied = true;
			copyTimer = setTimeout(() => (copied = false), 1500);
		} catch {
			toast(t('ui.copy.failed'), { tone: 'bad' });
		}
	}

	// --- following the bottom -------------------------------------------------

	/** Whether the reader holds a selection inside the box. */
	function selecting() {
		const sel = window.getSelection();
		return (
			!!sel &&
			!sel.isCollapsed &&
			!!viewport &&
			(viewport.contains(sel.anchorNode) || viewport.contains(sel.focusNode))
		);
	}

	$effect(() => {
		void text;
		untrack(() => {
			if (viewport && stick && cur < 0 && !selecting()) viewport.scrollTop = viewport.scrollHeight;
		});
	});

	function onscroll() {
		if (!viewport) return;
		stick = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight < FOLLOW_PX;
		if (viewport.scrollTop < EARLIER_PX && first > 0) void showEarlier();
	}

	function toTop() {
		stick = false;
		if (viewport) viewport.scrollTop = 0;
	}

	/** Follow the bottom again, from there. */
	export function follow() {
		stick = true;
		if (viewport) viewport.scrollTop = viewport.scrollHeight;
	}
</script>

<!-- A part's text, cut into its matches when the line has any. -->
{#snippet body(s: Segment)}{#if s.pieces}{#each s.pieces as p, k (k)}{#if p.mark}<mark
					class:current={p.current}>{p.text}</mark
				>{:else}{p.text}{/if}{/each}{:else}{s.text}{/if}{/snippet}

<!-- svelte-ignore a11y_no_static_element_interactions (Ctrl+F and the find keys, from anywhere inside) -->
<div class="logout" class:fill class:bounded={!fill} {onkeydown}>
	<div class="vbar">
		{@render bar?.()}
		<span class="tools" class:end={!bar}>
			<button
				class="btn-icon"
				class:btn-sel={finding}
				aria-pressed={finding}
				title={t('ui.log.find')}
				aria-label={t('ui.log.find')}
				onclick={() => (finding ? closeFind() : openFind())}><Glyph name="search" /></button
			>
			<button
				class="btn-icon"
				class:hue-success={copied}
				disabled={!text}
				title={copied ? t('ui.log.copied') : t('ui.log.copy')}
				aria-label={copied ? t('ui.log.copied') : t('ui.log.copy')}
				onclick={copy}
				>{#if copied}{I.ok}{:else}<Glyph name="copy" />{/if}</button
			>
			<button class="btn-icon" title={t('ui.log.top')} aria-label={t('ui.log.top')} onclick={toTop}
				><Glyph name="top" /></button
			>
			<button
				class="btn-icon"
				title={t('ui.log.bottom')}
				aria-label={t('ui.log.bottom')}
				onclick={follow}><Glyph name="bottom" /></button
			>
		</span>
	</div>
	{#if finding}
		<div class="vbar find" bind:this={findRow}>
			<SearchInput
				size="sm"
				width={240}
				bind:value={query}
				label={t('ui.log.find')}
				placeholder={t('ui.log.find_placeholder')}
			/>
			<span class="count" aria-live="polite"
				>{matches.length
					? t('ui.log.count', { n: cur + 1, m: matches.length })
					: sought.trim()
						? t('ui.log.no_matches')
						: ''}</span
			>
			<button
				class="btn-icon"
				disabled={!matches.length}
				title={t('ui.log.prev')}
				aria-label={t('ui.log.prev')}
				onclick={() => step(-1)}>{I.up}</button
			>
			<button
				class="btn-icon"
				disabled={!matches.length}
				title={t('ui.log.next')}
				aria-label={t('ui.log.next')}
				onclick={() => step(1)}>{I.down}</button
			>
			<button
				class="btn-icon"
				title={t('ui.log.close_find')}
				aria-label={t('ui.log.close_find')}
				onclick={closeFind}>{I.close}</button
			>
		</div>
	{/if}
	<!-- svelte-ignore a11y_no_noninteractive_tabindex (a scroll box: the keyboard scrolls it and finds in it) -->
	<div
		class="log out"
		role="region"
		aria-label={label ?? t('ui.log.label')}
		tabindex="0"
		bind:this={viewport}
		{onscroll}
	>
		{#if !text}
			{@render placeholder?.()}
		{:else}
			{@render head?.()}
			{#if first > 0}
				<button class="btn-sm earlier-lines" onclick={showEarlier}
					>{I.up}
					{t('ui.log.earlier', { count: Math.min(LOG_WINDOW, first) })}
					<span class="muted">{t('ui.log.hidden', { count: first })}</span></button
				>
			{/if}
			<div class="lines">
				{#each rows.slice(first) as r, k (first + k)}<div class="l {lineClass?.(r.text) ?? ''}">
						{#each r.segs as s, j (j)}{#if s.href}<a
									href={s.href}
									target="_blank"
									rel="noreferrer"
									draggable="false">{@render body(s)}</a
								>{:else}{@render body(s)}{/if}{:else}{' '}{/each}
					</div>{/each}
			</div>
		{/if}
	</div>
</div>

<style>
	.logout {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.fill {
		flex: 1;
		min-height: 0;
	}
	.bounded > .vbar {
		padding: 0;
		margin: 2px 0 6px;
	}
	.tools {
		display: inline-flex;
		gap: 2px;
	}
	.tools.end {
		margin-left: auto;
	}
	.find {
		padding-top: 0;
	}
	.bounded > .find {
		margin-top: 0;
	}
	.count {
		font-size: var(--fs-meta);
		color: var(--text-muted);
		min-width: 64px;
	}
	.hue-success {
		color: var(--success-text);
	}
	.out {
		line-height: 1.65;
	}
	.fill > .out {
		flex: 1;
		min-height: 0;
		max-height: none;
		margin: 0 8px 8px;
		line-height: 1.75;
	}
	.bounded > .out {
		max-height: 260px;
	}
	.out > :global(.empty-line) {
		font-family: var(--font-body);
		padding: 0;
	}
	.earlier-lines {
		margin: 4px 0 8px;
		font-family: var(--font-body);
	}
	.l {
		white-space: pre-wrap;
		word-break: break-all;
	}
	a {
		color: var(--link);
		text-decoration: underline;
	}
	mark {
		color: inherit;
		background: var(--warning-bg);
		border-radius: var(--r-tag);
	}
	mark.current {
		background: var(--attention-bg);
		outline: 1px solid var(--attention);
	}
</style>
