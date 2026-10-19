<script lang="ts" module>
	/** A flow name: a lowercase slug (it is the flow's id). */
	export const slugOk = (s: string) => /^[a-z][a-z0-9_-]*$/.test(s.trim());
</script>

<script lang="ts">
	// The flow-name field (New flow, Clone, Copy): mono, slug-checked as you
	// type, with the caller's own conflict (a name already taken) taking the
	// error line when the slug itself is fine.
	let {
		id,
		value = $bindable(''),
		label = 'Name',
		conflict = '',
		help,
		placeholder = ''
	}: {
		id: string;
		value: string;
		label?: string;
		/** Why this name can't be used here (taken, same workshop); '' when fine. */
		conflict?: string;
		help: string;
		placeholder?: string;
	} = $props();

	const bad = $derived(!!value.trim() && !slugOk(value));
</script>

<div class="field">
	<label for={id}>{label}</label>
	<input
		{id}
		class="mono"
		bind:value
		{placeholder}
		required
		autocomplete="off"
		spellcheck="false"
		class:invalid={bad || !!conflict}
	/>
	{#if bad}
		<div class="err">A lowercase slug: a letter, then letters, digits, - or _.</div>
	{:else if conflict}
		<div class="err">{conflict}</div>
	{:else}
		<div class="help">{help}</div>
	{/if}
</div>
