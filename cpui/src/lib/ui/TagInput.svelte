<script lang="ts">
	// Domain / tag list input (DESIGN 4.14): chips with ×; paste splits on
	// whitespace and commas; Enter or comma commits; Backspace on empty pops.
	let {
		values = $bindable([]),
		placeholder = 'Add…',
		mono = true,
		validate
	}: {
		values: string[];
		placeholder?: string;
		mono?: boolean;
		/** Return an error message to refuse a value. */
		validate?: (v: string) => string | null;
	} = $props();

	let draft = $state('');
	let err = $state('');

	function add(raw: string) {
		err = '';
		const next = [...values];
		for (const v of raw
			.split(/[\s,]+/)
			.map((s) => s.trim())
			.filter(Boolean)) {
			const bad = validate?.(v);
			if (bad) {
				err = `${v}: ${bad}`;
				continue;
			}
			if (!next.includes(v)) next.push(v);
		}
		values = next;
	}

	function key(e: KeyboardEvent) {
		if (e.key === 'Enter' || e.key === ',') {
			e.preventDefault();
			if (draft.trim()) add(draft);
			draft = '';
		} else if (e.key === 'Backspace' && !draft && values.length) {
			values = values.slice(0, -1);
		}
	}

	function paste(e: ClipboardEvent) {
		const t = e.clipboardData?.getData('text') ?? '';
		if (/[\s,]/.test(t.trim())) {
			e.preventDefault();
			add(t);
		}
	}
</script>

<div class="ti">
	{#each values as v (v)}
		<span class="chip" class:mono
			>{v}<button
				type="button"
				class="x"
				aria-label="Remove {v}"
				onclick={() => (values = values.filter((x) => x !== v))}>×</button
			></span
		>
	{/each}
	<input
		class="bare"
		bind:value={draft}
		{placeholder}
		onkeydown={key}
		onpaste={paste}
		onblur={() => {
			if (draft.trim()) add(draft);
			draft = '';
		}}
	/>
</div>
{#if err}<div class="err">{err}</div>{/if}

<style>
	.ti {
		display: flex;
		flex-wrap: wrap;
		gap: 5px;
		padding: 5px 6px;
		border-radius: var(--r-row);
		background: var(--p1);
		border: 1px solid var(--ln2);
		min-height: 36px;
		align-items: center;
	}
	.ti:focus-within {
		border-color: var(--brand);
		box-shadow: 0 0 0 3px var(--brandBg);
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 1px 4px 1px 9px;
		border-radius: var(--r-pill);
		background: var(--p2);
		border: 1px solid var(--ln);
		font-size: var(--fs-label);
		font-weight: 500;
	}
	.chip.mono {
		font-family: var(--mono);
		font-size: var(--fs-meta);
	}
	.x {
		all: unset;
		cursor: pointer;
		width: 16px;
		height: 16px;
		border-radius: 50%;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		color: var(--mut);
	}
	.x:hover {
		background: var(--p3);
		color: var(--tx);
	}
	.ti input {
		flex: 1;
		min-width: 120px;
		padding: 3px 4px;
	}
</style>
