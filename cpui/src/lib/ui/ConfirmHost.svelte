<script lang="ts">
	// Mounted once in the shell; renders whatever confirmDialog() asked.
	import Modal from './Modal.svelte';
	import { confirmState } from './confirm.svelte';

	let open = $state(false);
	$effect(() => {
		open = !!confirmState.current;
	});
</script>

{#if confirmState.current}
	{@const c = confirmState.current}
	<Modal
		bind:open
		title={c.title}
		icon={c.tone === 'warn' ? '!' : '■'}
		tone={c.tone ?? 'danger'}
		onclose={() => confirmState.settle(false)}
	>
		<!-- trusted copy, see confirm.svelte.ts -->
		<p style="margin:0">{@html c.body}</p>
		{#snippet footer()}
			<button class="btn-ghost" onclick={() => confirmState.settle(false)}
				>{c.cancel ?? 'Cancel'}</button
			>
			<button class="btn-danger-solid" onclick={() => confirmState.settle(true)}>{c.action}</button>
		{/snippet}
	</Modal>
{/if}
