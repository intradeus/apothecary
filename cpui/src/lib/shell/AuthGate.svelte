<script lang="ts">
	// The frame of the pages a browser sees before there is a session: the
	// brand, a title, the form, its one action.
	import type { Snippet } from 'svelte';
	import { t } from '$lib/i18n/index.svelte';
	import Logo from '$lib/ui/Logo.svelte';

	let {
		title,
		lede,
		action,
		width = 400,
		busy = false,
		error = '',
		onsubmit,
		children,
		foot
	}: {
		title: string;
		lede: string;
		/** The submit button's label. */
		action: string;
		width?: number;
		busy?: boolean;
		error?: string;
		onsubmit: (e: SubmitEvent) => void;
		children: Snippet;
		foot?: Snippet;
	} = $props();
</script>

<div class="gate">
	<form class="card glass" style:--w="{width}px" {onsubmit}>
		<div class="brand">
			<Logo size={38} />
			<div>
				<div class="name">Apothecary</div>
				<div class="tagline">{t('shell.sidebar.tagline')}</div>
			</div>
		</div>
		<h1>{title}</h1>
		<p class="muted">{lede}</p>
		{@render children()}
		{#if error}<div class="error">{error}</div>{/if}
		<div class="form-actions">
			<button class="btn-primary btn-lg" type="submit" disabled={busy} aria-busy={busy}
				>{action}</button
			>
		</div>
		{@render foot?.()}
	</form>
</div>

<style>
	.gate {
		position: fixed;
		inset: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 16px;
		color: var(--text);
		overflow: auto;
	}
	.card {
		width: 100%;
		max-width: var(--w);
		padding: 26px 26px 22px;
		border-radius: var(--r-pane);
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.brand {
		display: flex;
		align-items: center;
		gap: 12px;
		margin-bottom: 6px;
	}
	.name {
		font-weight: 600;
		font-size: var(--fs-lead);
		letter-spacing: var(--tr-heading);
	}
	.tagline {
		font-family: var(--font-display);
		font-weight: 600;
		font-size: var(--fs-body);
		color: var(--text-muted);
	}
	h1 {
		margin: 0;
		font-family: var(--font-display);
		font-weight: 600;
		font-size: var(--fs-empty);
	}
	p {
		margin: 0;
	}
	.form-actions {
		justify-content: stretch;
	}
	.form-actions > button {
		flex: 1;
	}
</style>
