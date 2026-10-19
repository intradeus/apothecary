<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// Mounted once in the shell. Toasts rise from the bottom edge and leave
	// the way they came; swipe one right to dismiss it (it follows the finger
	// and projects the flick). Neighbours slide into place on the spring
	// (animate:flip), so the stack never jumps. Hover pauses the timer.
	import { flip } from 'svelte/animate';
	import { toasts, type Toast } from './toast.svelte';
	import { dragDismiss, ms, slide, spring } from './motion';
	import { I } from './icons';

	const GLYPH = { ok: I.ok, info: I.info, warn: I.warn, bad: I.bad } as const;

	function timer(node: HTMLElement, tst: Toast) {
		let left = tst.ms;
		let started = Date.now();
		let h = setTimeout(() => toasts.dismiss(tst.id), left);
		const pause = () => {
			clearTimeout(h);
			left -= Date.now() - started;
		};
		const resume = () => {
			started = Date.now();
			h = setTimeout(() => toasts.dismiss(tst.id), Math.max(800, left));
		};
		node.addEventListener('pointerenter', pause);
		node.addEventListener('pointerleave', resume);
		return {
			destroy() {
				clearTimeout(h);
				node.removeEventListener('pointerenter', pause);
				node.removeEventListener('pointerleave', resume);
			}
		};
	}
</script>

<div class="toaster" aria-live="polite">
	{#each toasts.list as tst (tst.id)}
		<div
			class="toast lg h-{tst.tone === 'ok'
				? 'grn'
				: tst.tone === 'bad'
					? 'ros'
					: tst.tone === 'warn'
						? 'amb'
						: 'az'}"
			role="status"
			animate:flip={{ duration: ms('--dur-spring', 450), easing: spring }}
			transition:slide={{ from: 'bottom', distance: 40 }}
			use:timer={tst}
			use:dragDismiss={{
				direction: 'right',
				ondismiss: () => toasts.dismiss(tst.id),
				threshold: 0.35
			}}
		>
			<span class="g">{GLYPH[tst.tone]}</span>
			<span class="grow">{tst.text}</span>
			{#if tst.action}
				<button
					class="btn-sm"
					onclick={() => {
						tst.action?.run();
						toasts.dismiss(tst.id);
					}}>{tst.action.label}</button
				>
			{/if}
			<button class="btn-icon" aria-label={t('ui.dismiss')} onclick={() => toasts.dismiss(tst.id)}
				>{I.close}</button
			>
		</div>
	{/each}
</div>

<style>
	.toaster {
		position: fixed;
		right: 16px;
		bottom: 16px;
		z-index: var(--z-palette);
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 8px;
		pointer-events: none;
	}
	.toast {
		pointer-events: auto;
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 260px;
		max-width: min(420px, calc(100vw - 32px));
		padding: 7px 7px 7px 12px;
		border-radius: var(--r-btn);
		background: var(--lgBg), var(--popBg);
		box-shadow: var(--lgRim), var(--popSh);
		font-size: var(--fs-ui);
		cursor: grab;
		user-select: none;
	}
	.g {
		color: var(--h);
		font-weight: 700;
	}
	@media (max-width: 760px) {
		.toaster {
			left: 8px;
			right: 8px;
			bottom: 76px;
			align-items: stretch;
		}
	}
</style>
