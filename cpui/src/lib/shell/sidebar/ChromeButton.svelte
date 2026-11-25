<script lang="ts">
	// Chrome buttons on the sidebar: the shared icon button, a size up.
	import Glyph from '$lib/ui/Glyph.svelte';
	import type { GlyphName } from '$lib/design/glyphs';

	let {
		icon,
		title,
		label,
		size,
		onclick
	}: {
		icon: GlyphName;
		title: string;
		label: string;
		/** The rail's slim expand, the full sidebar's square collapse. */
		size?: 'exp' | 'sm';
		onclick: () => void;
	} = $props();
</script>

<button
	class="btn-icon rb"
	class:exp={size === 'exp'}
	class:sm={size === 'sm'}
	{title}
	aria-label={label}
	{onclick}><Glyph name={icon} size={size === 'exp' ? '14px' : '16px'} /></button
>

<style>
	.rb {
		width: 40px;
		height: 32px;
		border-radius: var(--r-row);
		color: var(--text-muted);
		font-size: var(--fs-lead);
	}
	.rb.exp {
		height: 24px;
		border-radius: var(--r-tab);
		font-size: var(--fs-body);
	}
	.rb.sm {
		width: 28px;
		height: 28px;
		border-radius: var(--r-tab);
		font-size: var(--fs-body);
	}
	.rb:hover {
		background: var(--fill-3);
		color: var(--text);
	}
</style>
