<script lang="ts">
	// A one-glyph button that copies a shell command, for a row that already
	// shows what it is about (a port, a service's address): the command is in
	// its tooltip and its accessible name, never on screen. A tick says it
	// worked; a refused clipboard says so in a toast, the row having no room.
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import { toast } from './toast.svelte';

	let { command }: { command: string } = $props();

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
	class="btn-icon btn-sm"
	class:hue-success={copied}
	title={command}
	aria-label={t('ui.copy.aria', { command })}
	onclick={copy}>{copied ? I.ok : I.copy}</button
>

<style>
	.hue-success {
		color: var(--success-text);
	}
</style>
