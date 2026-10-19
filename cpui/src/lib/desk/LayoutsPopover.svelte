<script lang="ts">
	// The Layouts popover (DESIGN §6): presets, the Windows checklist and how
	// to arrange things by hand. Saved in this browser.
	import Popover from '$lib/ui/Popover.svelte';
	import type { DeskState } from './desk.svelte';
	import { PRESETS, PRESET_IDS, VIEWS, VIEW_LABEL } from './tree';

	let { desk, anchor }: { desk: DeskState; anchor: HTMLElement | null } = $props();
</script>

<Popover {anchor} bind:open={desk.menuOpen} placement="below-end" width={300}>
	<div class="in">
		<div class="head">
			<span class="t">Layouts</span>
			<span class="lg2">saved in this browser</span>
		</div>
		<div class="presets">
			{#each PRESET_IDS as id (id)}
				<button class="pre" class:on={desk.preset === id} onclick={() => desk.applyPreset(id)}
					>{PRESETS[id].label}</button
				>
			{/each}
			<button
				class="pre"
				class:on={desk.preset === 'custom'}
				title="Your own arrangement — drag tabs and gaps to make one"
				disabled={desk.preset !== 'custom'}>Custom</button
			>
		</div>
		<div class="sub">
			<span>Windows</span><span>{desk.visible.size} of {VIEWS.length} shown</span>
		</div>
		<div class="wins">
			{#each VIEWS as v (v)}
				{@const on = desk.visible.has(v)}
				<button class="win" class:on onclick={() => desk.toggleView(v)}>
					<span class="box">{on ? '✓' : ''}</span>{VIEW_LABEL[v]}
				</button>
			{/each}
		</div>
		<div class="help">
			Drag a tab onto another window to stack it, or onto an edge to split. Drag the gaps to resize.
		</div>
	</div>
</Popover>

<style>
	.in {
		padding: 4px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		padding: 0 4px;
	}
	.t {
		font-family: var(--serif);
		font-size: 19px;
	}
	.lg2 {
		font-size: 11px;
		color: var(--fnt);
	}
	.presets {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 4px;
	}
	.pre,
	.win {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		padding: 5px 8px;
		border-radius: 9px;
		font-size: 12px;
		text-align: center;
		white-space: nowrap;
		color: var(--mut);
		font-weight: 500;
	}
	.pre:hover,
	.win:hover {
		background: var(--p3);
		color: var(--tx);
	}
	.pre.on {
		background: var(--sel);
		box-shadow:
			inset 0 0 0 1px var(--selLn),
			var(--selSh);
		color: var(--tx);
		font-weight: 600;
	}
	.pre:disabled {
		cursor: default;
	}
	.pre:disabled:not(.on) {
		opacity: 0.6;
	}
	.sub {
		padding: 4px 4px 0;
		font-size: 11px;
		color: var(--mut);
		font-weight: 500;
		display: flex;
		justify-content: space-between;
	}
	.wins {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 2px;
	}
	.win {
		display: flex;
		align-items: center;
		gap: 8px;
		text-align: left;
		font-size: 12.5px;
	}
	.win.on {
		color: var(--tx);
	}
	.box {
		width: 14px;
		height: 14px;
		border-radius: 5px;
		flex: none;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 9px;
		font-weight: 800;
		border: 1px solid var(--ln2);
		color: #0b1a12;
	}
	.win.on .box {
		background: var(--grn);
		border-color: var(--grn);
	}
	.help {
		padding: 6px 4px 2px;
		border-top: 1px solid var(--ln);
		font-size: 11px;
		color: var(--mut);
		line-height: 1.45;
	}
</style>
