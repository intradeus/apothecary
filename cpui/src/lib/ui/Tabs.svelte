<script lang="ts" module>
	import type { Hue } from './hues';

	export interface TabItem<V extends string> {
		value: V;
		label: string;
		/** A count after the label. */
		count?: number | string;
		/** A link tab: navigates instead of setting `value`. */
		href?: string;
		title?: string;
		/** A small dot in this hue before the label. */
		dot?: Hue;
	}
</script>

<script lang="ts" generics="T extends string">
	// Tabs (DESIGN 4.4): the sections of a page or a pane, on a hairline
	// strip with the selection's bead springing between them. Link tabs
	// (href) navigate and mark the current page; button tabs set `value`,
	// with ← → Home End moving between them. A filter or a view switch on
	// glass is Segmented, not this.
	//
	//   <Tabs items={sections} value={tab} label="Workshop sections" />
	import { indicator } from './motion';
	import { keepInView } from './inview';

	let {
		items,
		value = $bindable(),
		onchange,
		label,
		/** Keep the scroll position when a link tab navigates. */
		noscroll = true
	}: {
		items: TabItem<T>[];
		value: T;
		onchange?: (v: T) => void;
		/** Accessible name of the strip. */
		label: string;
		noscroll?: boolean;
	} = $props();

	const links = $derived(items.some((i) => i.href));

	function select(v: T) {
		if (v === value) return;
		value = v;
		onchange?.(v);
	}

	function keys(e: KeyboardEvent) {
		const at = items.findIndex((i) => i.value === value);
		const n = items.length;
		const to =
			e.key === 'ArrowRight'
				? (at + 1) % n
				: e.key === 'ArrowLeft'
					? (at - 1 + n) % n
					: e.key === 'Home'
						? 0
						: e.key === 'End'
							? n - 1
							: -1;
		if (to < 0) return;
		e.preventDefault();
		select(items[to].value);
		(e.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="tab"]')[to]?.focus();
	}
</script>

{#snippet inner(it: TabItem<string>)}
	{#if it.dot}<span class="ldot sm flat hue-{it.dot}"
		></span>{/if}{it.label}{#if it.count !== undefined}<span class="count">{it.count}</span>{/if}
{/snippet}

{#if links}
	<nav class="tabs" aria-label={label} use:indicator use:keepInView>
		{#each items as it (it.value)}
			<a
				href={it.href}
				title={it.title}
				class:on={it.value === value}
				aria-current={it.value === value ? 'page' : undefined}
				data-sveltekit-noscroll={noscroll || undefined}>{@render inner(it)}</a
			>
		{/each}
	</nav>
{:else}
	<div
		class="tabs"
		role="tablist"
		aria-label={label}
		tabindex="-1"
		use:indicator
		use:keepInView
		onkeydown={keys}
	>
		{#each items as it (it.value)}
			<button
				type="button"
				role="tab"
				title={it.title}
				class:on={it.value === value}
				aria-selected={it.value === value}
				tabindex={it.value === value ? 0 : -1}
				onclick={() => select(it.value)}>{@render inner(it)}</button
			>
		{/each}
	</div>
{/if}
