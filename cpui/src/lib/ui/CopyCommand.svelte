<script lang="ts">
	// A shell command to copy and run elsewhere: the command in mono, a copy
	// button beside it, and what it does on an optional line below.
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';

	let { command, help }: { command: string; help?: string } = $props();

	let copied = $state(false);
	let failed = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;

	async function copy() {
		clearTimeout(timer);
		try {
			await navigator.clipboard.writeText(command);
			copied = true;
			failed = false;
			timer = setTimeout(() => (copied = false), 1500);
		} catch {
			failed = true;
		}
	}
</script>

<div class="cc">
	<div class="row inset">
		<code class="mono grow" title={command}>{command}</code>
		<button
			class="btn-ghost btn-sm"
			class:hue-success={copied}
			aria-label={t('ui.copy.aria', { command })}
			onclick={copy}>{copied ? `${t('ui.copy.done')} ${I.ok}` : t('ui.copy.label')}</button
		>
	</div>
	{#if failed}<div class="help error">{t('ui.copy.failed')}</div>
	{:else if help}<div class="help">{help}</div>{/if}
</div>

<style>
	.cc {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.row {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 4px 4px 4px 10px;
		border-radius: var(--r-btn);
	}
	code {
		font-size: var(--fs-label);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.hue-success {
		color: var(--success-text);
	}
</style>
