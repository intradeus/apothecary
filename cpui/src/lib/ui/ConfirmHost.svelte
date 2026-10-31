<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// Mounted once in the shell; renders whatever confirmDialog() asked. It
	// keeps the last dialog's content after it settles, so the close can play
	// out instead of the dialog vanishing mid-frame.
	import Modal from './Modal.svelte';
	import { confirmState, type ConfirmOptions } from './confirm.svelte';

	let shown = $state<ConfirmOptions | null>(null);
	let open = $state(false);
	$effect(() => {
		const c = confirmState.current;
		if (c) {
			shown = c;
			open = true;
		} else open = false;
	});
</script>

{#if shown}
	<Modal
		bind:open
		title={shown.title}
		icon={shown.tone === 'warn' ? '!' : '■'}
		tone={shown.tone ?? 'danger'}
		onclose={() => confirmState.settle(false)}
	>
		<!-- trusted copy, see confirm.svelte.ts -->
		<!-- eslint-disable-next-line no-restricted-syntax -- bodies are trusted markup: see html.ts -->
		<p class="body">{@html shown.body}</p>
		{#snippet footer()}
			<button class="btn-ghost" onclick={() => confirmState.settle(false)}
				>{shown?.cancel ?? t('common.cancel')}</button
			>
			<button class="btn-danger-solid" onclick={() => confirmState.settle(true)}
				>{shown?.action}</button
			>
		{/snippet}
	</Modal>
{/if}

<style>
	.body {
		margin: 0;
	}
</style>
