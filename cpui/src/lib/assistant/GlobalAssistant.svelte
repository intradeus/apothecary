<script lang="ts">
	// The global assistant (docs/assistant-plan.md): the sparkle button at the
	// viewport's top right on every signed-in page, and the drawer it opens
	// over the page with the person's general conversation. The conversation
	// lives here, outside the Sheet (which unmounts its content when closed),
	// so closing the drawer loses neither the session nor what streams in.
	import { onDestroy, onMount, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { to } from '$lib/core/urls';
	import { I, SPARKLE_PATH } from '$lib/design/icons';
	import { t } from '$lib/i18n/index.svelte';
	import Sheet from '$lib/ui/Sheet.svelte';
	import AssistantPanel from './AssistantPanel.svelte';
	import { Conversation } from './conversation.svelte';
	import { drawer } from './drawer.svelte';

	const convo = new Conversation({
		kind: 'general',
		subject: 'palette',
		workshop: untrack(() => page.params.workshop ?? ''),
		// Where the person is, so "this run" means the one on screen.
		pageState: () => ({ page: page.url.pathname })
	});

	/** Loaded and off: the button looks it, the drawer says why. */
	const off = $derived(!!convo.status && !convo.status.enabled);

	onMount(() => {
		void convo.load();
		convo.connect();
	});
	onDestroy(() => convo.disconnect());

	// Another workshop's pages: its own conversation.
	$effect(() => {
		const w = page.params.workshop ?? '';
		untrack(() => void convo.retarget(w));
	});

	function settings() {
		drawer.open = false;
		void goto(to.system('assistant'));
	}
</script>

<button
	type="button"
	class="ga glass"
	class:off
	aria-label={t('assistant.button')}
	title={off ? t('assistant.buttonOff') : t('assistant.buttonTip')}
	aria-describedby={off ? 'ga-off' : undefined}
	onclick={() => (drawer.open = true)}
>
	<svg viewBox="0 0 24 24" aria-hidden="true"><path d={SPARKLE_PATH} /></svg>
</button>
{#if off}<span id="ga-off" hidden>{t('assistant.buttonOff')}</span>{/if}

<Sheet bind:open={drawer.open} side="right" quick label={t('assistant.title')}>
	<div class="gd island col">
		<div class="head row nowrap">
			<h2>{t('assistant.title')}</h2>
			<button type="button" class="btn-sm" onclick={() => convo.load({ fresh: true })}
				>{t('assistant.newConversation')}</button
			>
			<button
				type="button"
				class="btn-icon"
				aria-label={t('assistant.settings')}
				title={t('assistant.settings')}
				onclick={settings}>{I.system}</button
			>
			<button
				type="button"
				class="btn-icon"
				aria-label={t('assistant.close')}
				title={t('assistant.close')}
				onclick={() => (drawer.open = false)}>{I.close}</button
			>
		</div>
		<AssistantPanel {convo} />
	</div>
</Sheet>

<style>
	.ga {
		position: fixed;
		top: var(--shell-gap);
		right: var(--shell-gap);
		z-index: var(--z-bar);
		width: var(--assistant-btn);
		height: var(--assistant-btn);
		padding: 0;
		display: grid;
		place-items: center;
		border-radius: var(--r-pane);
		transition: box-shadow var(--dur-fade) var(--ease-out);
	}
	.ga:hover,
	.ga:focus-visible {
		box-shadow: 0 0 var(--glow-sm) var(--accent-glow);
	}
	.ga svg {
		width: 20px;
		height: 20px;
		fill: var(--accent);
	}
	.ga.off svg {
		fill: var(--text-faint);
	}
	.ga.off:hover,
	.ga.off:focus-visible {
		box-shadow: none;
	}
	@media (prefers-reduced-motion: reduce) {
		.ga {
			transition: none;
		}
		.ga:hover,
		.ga:focus-visible {
			box-shadow: none;
		}
	}
	.gd {
		width: min(440px, 100vw);
		height: 100%;
		min-height: 0;
		padding: 12px;
		gap: 10px;
	}
	.head {
		gap: 8px;
		align-items: center;
	}
	.head h2 {
		margin: 0 auto 0 0;
	}
</style>
