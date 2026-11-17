<script lang="ts">
	// A button that copies a shell command, for a row that already shows
	// what it is about (a port, a service's address): the command is in its
	// tooltip and its accessible name, never on screen. Bare, it is one
	// glyph; with a label, the glyph and a word saying what the command is
	// for. A tick says it worked; a refused clipboard says so in a toast,
	// the row having no room.
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import { toast } from './toast.svelte';

	let { command, label }: { command: string; label?: string } = $props();

	let copied = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;

	async function copy() {
		clearTimeout(timer);
		try {
			await navigator.clipboard.writeText(command);
			copied = true;
			timer = setTimeout(() => (copied = false), 1500);
		} catch {
			toast(t('ui.copy.failed'), { tone: 'bad' });
		}
	}
</script>

<button
	class={label ? 'btn-sm' : 'btn-icon btn-sm'}
	class:hue-success={copied}
	title={command}
	aria-label={label
		? t('ui.copy.aria_labelled', { label, command })
		: t('ui.copy.aria', { command })}
	onclick={copy}
	>{copied ? I.ok : I.copy}{#if label}&nbsp;{label}{/if}</button
>

<style>
	.hue-success {
		color: var(--success-text);
	}
</style>
