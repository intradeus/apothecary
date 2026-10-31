<script lang="ts" module>
	export type CheckTone = 'ok' | 'bad' | 'warn' | 'live' | 'none';
</script>

<script lang="ts">
	// A check's result as a chip (DESIGN 7.7 health, 7.8 validation, 7.10
	// "persisted test result"): ✓ ok, ✕ an error, ! a warning, a pulsing dot
	// while it runs, a dashed outline when it never ran. `when` trails it
	// ("tested 3h ago"). A new result materialises in place of the old one,
	// so a re-run reads as an answer arriving rather than a repaint.
	import type { Hue } from './hues';
	import { I } from './icons';
	import { materialize } from './motion';

	let {
		tone,
		label,
		when,
		title,
		size = 'md'
	}: {
		tone: CheckTone;
		label: string;
		/** A quieter trailing note ("tested 3h ago"). */
		when?: string;
		title?: string;
		size?: 'sm' | 'md';
	} = $props();

	const HUE: Record<CheckTone, Hue> = {
		ok: 'grn',
		bad: 'ros',
		warn: 'gold',
		live: 'emb',
		none: 'mut'
	};
	const GLYPH: Record<CheckTone, string> = {
		ok: I.ok,
		bad: I.bad,
		warn: I.warn,
		live: '',
		none: ''
	};
</script>

{#key `${tone}|${label}`}
	<span
		class="pill cc t-{HUE[tone]} {tone}"
		class:sm={size === 'sm'}
		{title}
		in:materialize={{ origin: 'left center' }}
	>
		{#if tone === 'live'}<span class="dot pulse"></span>{:else if GLYPH[tone]}<span class="g"
				>{GLYPH[tone]}</span
			>{/if}<span class="ell">{label}</span>{#if when}<span class="when">· {when}</span>{/if}
	</span>
{/key}

<style>
	.cc {
		max-width: 100%;
		min-width: 0;
	}
	.g {
		flex: none;
		font-weight: 700;
	}
	.when {
		flex: none;
		font-weight: 500;
		opacity: 0.8;
	}
	/* Never run: an outline only, nothing to read as a verdict. */
	.none {
		border-style: dashed;
		background: transparent;
	}
</style>
