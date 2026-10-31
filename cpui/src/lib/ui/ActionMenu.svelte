<script lang="ts" module>
	let seq = 0;
</script>

<script lang="ts">
	// A page's main action when there are several kinds to start from (a new
	// integration, a new provider): one button, the kinds in a menu under it.
	// Keyboard as the ARIA menu button: opening moves focus to the first item,
	// ↑ ↓ Home End move, Esc and Tab close back to the button.
	import { tick } from 'svelte';
	import Popover from './Popover.svelte';
	import { I } from '$lib/design/icons';

	let {
		label,
		groups
	}: {
		label: string;
		groups: { title?: string; items: { label: string; run: () => void }[] }[];
	} = $props();

	const menuId = `action-menu-${++seq}`;
	let anchor = $state<HTMLButtonElement | null>(null);
	let open = $state(false);

	const items = () =>
		Array.from(
			document.getElementById(menuId)?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []
		);

	async function toggle(e: MouseEvent) {
		open = !open;
		// detail 0: the keyboard's click. A press leaves the focus where it is.
		if (!open || e.detail !== 0) return;
		await tick();
		items()[0]?.focus();
	}

	function close() {
		open = false;
		anchor?.focus();
	}

	function keys(e: KeyboardEvent) {
		if (e.key === 'Tab') return close();
		const all = items();
		const i = all.indexOf(e.currentTarget as HTMLElement);
		const to = new Map([
			['ArrowDown', i + 1],
			['ArrowUp', i - 1],
			['Home', 0],
			['End', all.length - 1]
		]).get(e.key);
		if (to === undefined) return;
		e.preventDefault();
		all[(to + all.length) % all.length].focus();
	}
</script>

<button
	bind:this={anchor}
	class="btn-primary btn-lg"
	aria-haspopup="menu"
	aria-expanded={open}
	onclick={toggle}>{I.add} {label}</button
>

<Popover {anchor} bind:open id={menuId} placement="below-end" width={260} role="menu" portal flip>
	{#each groups.filter((g) => g.items.length) as g, i (g.title ?? i)}
		{#if i}<div class="menu-div"></div>{/if}
		{#if g.title}<div class="gt">{g.title}</div>{/if}
		{#each g.items as it (it.label)}
			<button
				class="menu-item"
				role="menuitem"
				onkeydown={keys}
				onclick={() => {
					close();
					it.run();
				}}>{it.label}</button
			>
		{/each}
	{/each}
</Popover>

<style>
	.gt {
		font-size: var(--fs-meta);
		color: var(--text-muted);
		padding: 4px 9px 2px;
	}
</style>
