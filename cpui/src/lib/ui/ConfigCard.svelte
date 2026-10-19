<script lang="ts" module>
	export interface ConfigRow {
		k: string;
		v: string;
		/** A write-only secret: shown muted, never as a value to copy. */
		secret?: boolean;
		title?: string;
	}
</script>

<script lang="ts">
	// One configured thing in a System grid (DESIGN 7.10: an integration, a
	// channel): its name and kind chip, a status on the right (a test result),
	// a quiet line under the name, the config summary boxed as `dl.kv.code`,
	// then its actions. `open` expands an inline editor (children) under the
	// actions: it grows on the spring and collapses the same way, reversing
	// from wherever it is when toggled mid-flight.
	import type { Snippet } from 'svelte';
	import { slide } from 'svelte/transition';
	import type { Hue } from './hues';
	import { ms, spring } from './motion';

	let {
		title,
		kind,
		glyph,
		hue = 'mut',
		sub,
		status,
		summary = [],
		error = '',
		actions,
		open = false,
		children
	}: {
		title: string;
		/** The kind chip beside the name ("GitLab", "Matrix"). */
		kind?: string;
		/** A glyph tile before the name (channels). */
		glyph?: string;
		hue?: Hue;
		/** A quieter line under the name ("used by api, web"). */
		sub?: string | Snippet;
		status?: Snippet;
		summary?: ConfigRow[];
		/** An inline error next to its cause. */
		error?: string;
		actions?: Snippet;
		/** The inline editor is expanded. */
		open?: boolean;
		/** The inline editor. */
		children?: Snippet;
	} = $props();

	const grow = (node: Element) =>
		slide(node, { duration: ms('--dur-spring', 450), easing: spring });
</script>

<article class="cc c" class:open>
	<div class="body">
		<div class="top">
			{#if glyph}<span class="gl h-{hue}" aria-hidden="true">{glyph}</span>{/if}
			<div class="who">
				<div class="nmrow">
					<span class="nm ell">{title}</span>
					{#if kind}<span class="pill sm t-mut">{kind}</span>{/if}
				</div>
				{#if sub}
					<div class="sub ell">
						{#if typeof sub === 'string'}{sub}{:else}{@render sub()}{/if}
					</div>
				{/if}
			</div>
			{#if status}<div class="st">{@render status()}</div>{/if}
		</div>

		{#if summary.length}
			<dl class="kv code">
				{#each summary as s (s.k)}
					<dt class="ell" title={s.k}>{s.k}</dt>
					<dd
						class="ell"
						class:secret={s.secret}
						title={s.title ?? (s.secret ? 'write-only secret' : s.v)}
					>
						{s.v || '—'}
					</dd>
				{/each}
			</dl>
		{/if}

		{#if error}<div class="err">{error}</div>{/if}

		{#if actions}<div class="acts">{@render actions()}</div>{/if}
	</div>

	{#if open && children}
		<div class="edit" transition:grow>
			<div class="edit-in">{@render children()}</div>
		</div>
	{/if}
</article>

<style>
	.cc {
		border-radius: var(--r-pane);
		padding: 12px 14px 10px;
		min-width: 0;
		transition: border-color var(--dur-fade) var(--ease-out);
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: 10px;
		min-width: 0;
	}
	.cc.open {
		border-color: var(--brandLn);
	}
	.top {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		min-width: 0;
	}
	.gl {
		flex: none;
		width: 30px;
		height: 30px;
		border-radius: var(--r-tab);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: var(--fs-body);
		color: var(--h);
		background: color-mix(in oklch, var(--h) var(--hueBg), transparent);
		border: 1px solid var(--ln);
	}
	.who {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.nmrow {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.nm {
		font-weight: 600;
		font-size: var(--fs-read);
		letter-spacing: var(--tr-heading);
	}
	.sub {
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.st {
		flex: none;
		max-width: 55%;
		display: flex;
		justify-content: flex-end;
		min-width: 0;
	}
	/* A long field label must not starve its value: labels shrink first. */
	dl.kv {
		--k: minmax(0, max-content);
		grid-template-columns: var(--k) minmax(40%, 1fr);
	}
	dd.secret {
		color: var(--fnt);
	}
	.acts {
		display: flex;
		gap: 6px;
		align-items: center;
	}
	/* Spacing and hairline sit inside the sliding box, so nothing jumps
	 * when it opens: the whole of it grows from zero. */
	.edit-in {
		margin-top: 10px;
		border-top: 1px solid var(--cLn);
		padding-top: 12px;
	}
</style>
