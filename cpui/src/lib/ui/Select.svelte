<script lang="ts" module>
	import type { Hue } from '$lib/design/hues';

	export interface SelectOption<V extends string = string> {
		value: V;
		label: string;
		/** A quieter second part (a description, a count, a path). */
		detail?: string;
		disabled?: boolean;
		/** Options sharing a group are listed under its heading, in order of
		 *  first appearance. */
		group?: string;
		/** A small dot in this hue before the label. */
		dot?: Hue;
	}

	let seq = 0;
</script>

<script lang="ts" generics="T extends string">
	// Dropdown (DESIGN 4.14): the field shows the choice; the list opens on
	// popover glass beside it (portaled, so a pane never clips it), with the
	// selected row beaded like every other menu. Keyboard as a native select:
	// ↑ ↓ / Enter / Space open it, ↑ ↓ Home End move, Enter picks, Esc and
	// Tab close, typing jumps to the next label starting so. Long lists get a
	// filter field.
	//
	//   <label for="phase">Chain</label>
	//   <Select id="phase" bind:value {options} onchange={save} />
	import Popover from './Popover.svelte';
	import { I } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';

	let {
		options,
		value = $bindable(),
		onchange,
		id,
		name,
		placeholder,
		disabled = false,
		required = false,
		invalid = false,
		/** md: a form field; sm: inline in a toolbar or a row. */
		size = 'md',
		mono = false,
		/** As wide as its content instead of the whole row. */
		inline = false,
		/** Borderless, inside another control (a number's unit). */
		bare = false,
		/** Show a filter field; default: when there are more than 10 options. */
		searchable,
		label,
		title
	}: {
		options: SelectOption<T>[];
		value: T | undefined;
		onchange?: (v: T) => void;
		id?: string;
		/** Submits the value with a form under this name. */
		name?: string;
		/** Shown while nothing is chosen. */
		placeholder?: string;
		disabled?: boolean;
		required?: boolean;
		invalid?: boolean;
		size?: 'sm' | 'md';
		mono?: boolean;
		inline?: boolean;
		bare?: boolean;
		searchable?: boolean;
		/** Accessible name, when no <label for> names it. */
		label?: string;
		title?: string;
	} = $props();

	const uid = `sel-${++seq}`;
	const listId = `${uid}-list`;
	let trigger = $state<HTMLButtonElement | null>(null);
	let search = $state<HTMLInputElement | null>(null);
	let open = $state(false);
	let query = $state('');
	/** The highlighted row (keyboard or pointer), by index in `shown`. */
	let active = $state(-1);

	const current = $derived(options.find((o) => o.value === value));
	const filter = $derived(searchable ?? options.length > 10);
	// In the order they are drawn, groups gathered at their first sight, so the
	// keys walk the rows as they read.
	const shown = $derived.by(() => {
		const q = query.trim().toLowerCase();
		const hits = q
			? options.filter((o) => `${o.label} ${o.detail ?? ''} ${o.value}`.toLowerCase().includes(q))
			: options;
		return [...new Set(hits.map((o) => o.group))].flatMap((g) => hits.filter((o) => o.group === g));
	});
	// Rows with their group headings, groups kept in order of first sight.
	const rows = $derived.by(() => {
		const order: (string | undefined)[] = [];
		const by = new Map<string | undefined, { o: SelectOption<T>; i: number }[]>();
		shown.forEach((o, i) => {
			if (!by.has(o.group)) {
				by.set(o.group, []);
				order.push(o.group);
			}
			by.get(o.group)!.push({ o, i });
		});
		return order.map((g) => ({ group: g, items: by.get(g)! }));
	});

	const optId = (i: number) => `${uid}-o${i}`;
	const enabled = (i: number) => i >= 0 && i < shown.length && !shown[i].disabled;

	function show() {
		if (disabled || open) return;
		query = '';
		open = true;
		active = Math.max(
			0,
			shown.findIndex((o) => o.value === value)
		);
		if (!enabled(active)) active = step(active, 1);
		if (filter) requestAnimationFrame(() => search?.focus());
	}

	function hide(refocus = true) {
		if (!open) return;
		open = false;
		query = '';
		if (refocus) trigger?.focus();
	}

	function pick(i: number) {
		if (!enabled(i)) return;
		const v = shown[i].value;
		hide();
		if (v === value) return;
		value = v;
		onchange?.(v);
	}

	/** The next enabled row from i in direction d (wrapping), or i. */
	function step(i: number, d: 1 | -1): number {
		const n = shown.length;
		for (let k = 1; k <= n; k++) {
			const j = (((i + d * k) % n) + n) % n;
			if (enabled(j)) return j;
		}
		return i;
	}

	function edge(d: 1 | -1) {
		active = step(d === 1 ? -1 : shown.length, d);
	}

	// Type-ahead: letters typed within half a second build a prefix.
	let typed = '';
	let typedAt = 0;
	function typeahead(ch: string) {
		const now = Date.now();
		typed = now - typedAt > 500 ? ch : typed + ch;
		typedAt = now;
		const from = open ? active : shown.findIndex((o) => o.value === value);
		const n = shown.length;
		for (let k = typed.length > 1 ? 0 : 1; k <= n; k++) {
			const j = (((from + k) % n) + n) % n;
			if (enabled(j) && shown[j].label.toLowerCase().startsWith(typed)) {
				if (open) active = j;
				else pick(j);
				return;
			}
		}
	}

	function keys(e: KeyboardEvent) {
		const fromSearch = e.currentTarget === search;
		switch (e.key) {
			case 'ArrowDown':
			case 'ArrowUp': {
				e.preventDefault();
				const d = e.key === 'ArrowDown' ? 1 : -1;
				if (!open) show();
				else active = step(active, d);
				return;
			}
			case 'Home':
			case 'End':
				if (!open || fromSearch) return;
				e.preventDefault();
				edge(e.key === 'Home' ? 1 : -1);
				return;
			case 'Enter':
				e.preventDefault();
				if (open) pick(active);
				else show();
				return;
			case ' ':
				if (fromSearch) return;
				e.preventDefault();
				if (open) pick(active);
				else show();
				return;
			case 'Tab':
				hide(false);
				return;
		}
		if (!fromSearch && e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey)
			typeahead(e.key.toLowerCase());
	}

	// The highlighted row stays in view as the keys move it.
	$effect(() => {
		if (!open || active < 0) return;
		document.getElementById(optId(active))?.scrollIntoView({ block: 'nearest' });
	});
	// Filtering keeps a row highlighted.
	$effect(() => {
		void query;
		if (open && !enabled(active)) active = step(-1, 1);
	});
</script>

<button
	bind:this={trigger}
	{id}
	type="button"
	class="select {size}"
	class:mono
	class:inline
	class:bare
	class:invalid
	class:open
	class:empty={!current}
	role="combobox"
	aria-haspopup="listbox"
	aria-expanded={open}
	aria-controls={open ? listId : undefined}
	aria-activedescendant={open && !filter && active >= 0 ? optId(active) : undefined}
	aria-label={label}
	aria-required={required || undefined}
	aria-invalid={invalid || undefined}
	{disabled}
	title={title ?? current?.label}
	onclick={() => (open ? hide() : show())}
	onkeydown={keys}
>
	{#if current?.dot}<span class="live-dot sm flat hue-{current.dot}"></span>{/if}
	<span class="v truncate">{current?.label ?? placeholder ?? ''}</span>
	<span class="chev" aria-hidden="true">{I.menu}</span>
</button>
{#if name || required}
	<!-- Takes part in the form: its value, and the browser's "required". -->
	<input
		class="proxy"
		tabindex="-1"
		aria-hidden="true"
		{name}
		{required}
		value={value ?? ''}
		onfocus={() => trigger?.focus()}
	/>
{/if}

<Popover
	anchor={trigger}
	bind:open
	id={listId}
	role="listbox"
	portal
	matchWidth
	flip
	placement="below-start"
	onclose={() => (query = '')}
>
	{#if filter}
		<input
			bind:this={search}
			bind:value={query}
			class="find"
			type="search"
			placeholder={t('ui.select.filter')}
			aria-label={t('ui.select.filter')}
			aria-controls={listId}
			aria-activedescendant={active >= 0 ? optId(active) : undefined}
			onkeydown={keys}
		/>
	{/if}
	{#each rows as r (r.group ?? '')}
		{#if r.group}<div class="grp" role="presentation">{r.group}</div>{/if}
		{#each r.items as { o, i } (i)}
			<!-- Focus stays on the field (aria-activedescendant); rows only
			     take the pointer. -->
			<div
				id={optId(i)}
				class="menu-item"
				class:on={o.value === value}
				class:hl={i === active}
				class:mono
				class:off={o.disabled}
				role="option"
				tabindex="-1"
				aria-selected={o.value === value}
				aria-disabled={o.disabled || undefined}
				onpointermove={() => o.disabled || (active = i)}
				onmousedown={(e) => e.preventDefault()}
				onclick={() => pick(i)}
				onkeydown={keys}
			>
				{#if o.dot}<span class="live-dot sm flat hue-{o.dot}"></span>{/if}
				<span class="truncate">{o.label}</span>
				{#if o.detail}<span class="sub truncate">{o.detail}</span>{/if}
				{#if o.value === value}<span class="ck" aria-hidden="true">{I.ok}</span>{/if}
			</div>
		{/each}
	{:else}
		<div class="none">{t('ui.select.none')}</div>
	{/each}
</Popover>

<style>
	/* The field: an input's box (forms) with a chevron, so a row of fields
	 * reads as one set. */
	.select {
		box-sizing: border-box;
		display: flex;
		align-items: center;
		justify-content: flex-start;
		gap: 7px;
		width: 100%;
		min-width: 0;
		text-align: left;
		font-family: var(--font-body);
		font-size: var(--fs-body);
		font-weight: 400;
		color: var(--text);
		padding: 7px 10px;
		border-radius: var(--r-row);
		background: var(--fill-1);
		border: 1px solid var(--line-strong);
		box-shadow: none;
	}
	.select:hover {
		background: var(--fill-2);
	}
	/* A field, not a button: it does not sink on press. */
	.select:active {
		transform: none;
	}
	.select:focus-visible,
	.select.open {
		outline: none;
		border-color: var(--brand);
		box-shadow: 0 0 0 3px var(--brand-bg);
	}
	.select.invalid {
		border-color: var(--danger);
	}
	.select:disabled {
		opacity: var(--o-disabled);
	}
	.select.sm {
		font-size: var(--fs-label);
		padding: 3px 8px;
		border-radius: var(--r-tab);
	}
	/* In a dense form (.form-dense) a field takes an input's metrics there. */
	:global(.form-dense) .select:not(.sm) {
		padding: 5px 8px;
		font-size: var(--fs-label);
		border-radius: var(--r-tab);
	}
	.select.inline {
		width: auto;
		max-width: 100%;
	}
	.select.bare {
		width: auto;
		border-color: transparent;
		background: transparent;
		padding-left: 6px;
		padding-right: 6px;
	}
	.select.bare:focus-visible,
	.select.bare.open {
		box-shadow: none;
		border-color: transparent;
		background: var(--fill-3);
	}
	.mono,
	.mono .v {
		font-family: var(--font-code);
		font-size: var(--fs-label);
	}
	.v {
		flex: 1;
		min-width: 0;
	}
	.empty .v {
		color: var(--text-faint);
	}
	.chev {
		flex: none;
		margin-left: auto;
		color: var(--text-muted);
		font-size: var(--fs-meta);
		transition: transform var(--dur-spring) var(--spring);
	}
	.open .chev {
		transform: rotate(180deg);
	}
	/* Invisible, but where the browser points when "required" is unmet. */
	.proxy {
		position: absolute;
		width: 1px;
		height: 1px;
		padding: 0;
		margin-top: -1px;
		opacity: 0;
		pointer-events: none;
	}

	/* The list. Rows are the shared menu rows (.menu-item); .hl is the keyboard's
	 * place, drawn like the pointer's hover. */
	.find {
		margin: 0 0 4px;
		padding: 5px 8px;
		font-size: var(--fs-label);
		flex: none;
	}
	.grp {
		padding: 8px 9px 3px;
		font-size: var(--fs-meta);
		font-weight: 600;
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.menu-item {
		flex: none;
	}
	.menu-item.hl:not(.on) {
		background: var(--fill-3);
	}
	.menu-item.off {
		opacity: var(--o-disabled);
		cursor: default;
	}
	.menu-item .sub {
		margin-left: auto;
		padding-left: 10px;
		max-width: 50%;
	}
	.ck {
		flex: none;
		margin-left: auto;
		padding-left: 8px;
		color: var(--success-text);
		font-weight: 700;
	}
	.sub + .ck {
		margin-left: 0;
	}
	.none {
		padding: 8px 9px;
		font-size: var(--fs-label);
		color: var(--text-faint);
	}
</style>
