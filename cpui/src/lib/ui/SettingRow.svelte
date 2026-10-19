<script lang="ts">
	// One setting inside a System pane: what it is and what it does on the
	// left, the control on the right.
	import type { Snippet } from 'svelte';

	let {
		label,
		forId,
		help,
		note,
		children
	}: {
		label: string;
		forId?: string;
		help?: string;
		/** A second, quieter line (a consequence, a current value). */
		note?: Snippet;
		children: Snippet;
	} = $props();
</script>

<div class="sr">
	<div class="txt">
		<label for={forId}>{label}</label>
		{#if help}<div class="help">{help}</div>{/if}
		{#if note}<div class="sr-note">{@render note()}</div>{/if}
	</div>
	<div class="ctl">{@render children()}</div>
</div>

<style>
	.sr {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		gap: 10px 24px;
		align-items: center;
		padding: 14px 16px;
		border-top: 1px solid var(--cLn);
	}
	.sr:first-child {
		border-top: 0;
	}
	.txt {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
	}
	label {
		margin: 0;
		font-size: var(--fs-body);
		font-weight: 600;
	}
	.help {
		max-width: 62ch;
		text-wrap: pretty;
	}
	.sr-note {
		font-size: var(--fs-help);
		color: var(--fnt);
	}
	.ctl {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 4px;
	}
	@media (max-width: 720px) {
		.sr {
			grid-template-columns: minmax(0, 1fr);
		}
		.ctl {
			align-items: flex-start;
		}
	}
</style>
