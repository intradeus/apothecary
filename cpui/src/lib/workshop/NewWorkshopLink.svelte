<script lang="ts">
	// "New workshop", for an account that may create one: a guest only works
	// in workshops shared with it (the engine answers 403), so it gets none.
	import { session } from '$lib/app/session.svelte';
	import { to } from '$lib/core/urls';

	let { label, class: cls = 'btn btn-primary' }: { label: string; class?: string } = $props();
</script>

{#if !session.guest}<a class={cls} href={to.newWorkshop()}>{label}</a>{/if}
