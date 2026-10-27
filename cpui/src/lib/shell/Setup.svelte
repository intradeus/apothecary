<script lang="ts">
	// First boot: no account exists, so whoever reaches the page claims the
	// instance and becomes its admin.
	import { session } from '$lib/session.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import Logo from '$lib/ui/Logo.svelte';

	let handle = $state('');
	let email = $state('');
	let name = $state('');
	let password = $state('');
	let again = $state('');
	let busy = $state(false);
	let error = $state('');

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (password !== again) {
			error = t('auth.setup.mismatch');
			return;
		}
		busy = true;
		error = '';
		try {
			await session.setup({
				handle: handle.trim(),
				email: email.trim(),
				name: name.trim(),
				password
			});
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>{t('auth.setup.docTitle')}</title></svelte:head>

<div class="gate">
	<form class="card c" onsubmit={submit}>
		<div class="brand">
			<Logo size={38} />
			<div>
				<div class="name">Apothecary</div>
				<div class="tagline">{t('shell.sidebar.tagline')}</div>
			</div>
		</div>
		<h1>{t('auth.setup.title')}</h1>
		<p class="mut">{t('auth.setup.lede')}</p>
		<div class="grid2">
			<div class="field">
				<label for="su-handle">{t('auth.handle')}</label>
				<input
					id="su-handle"
					class="mono"
					bind:value={handle}
					required
					autocomplete="username"
					placeholder="lu"
					pattern="[a-z0-9][a-z0-9-]{'{'}1,31{'}'}"
				/>
				<div class="help">{t('auth.handleHelp')}</div>
			</div>
			<div class="field">
				<label for="su-name">{t('auth.name')}</label>
				<input id="su-name" bind:value={name} autocomplete="name" />
			</div>
		</div>
		<div class="field">
			<label for="su-email">{t('auth.email')}</label>
			<input id="su-email" type="email" bind:value={email} required autocomplete="email" />
		</div>
		<div class="grid2">
			<div class="field">
				<label for="su-pw">{t('auth.password')}</label>
				<input
					id="su-pw"
					type="password"
					bind:value={password}
					required
					minlength="8"
					autocomplete="new-password"
				/>
				<div class="help">{t('auth.passwordHelp')}</div>
			</div>
			<div class="field">
				<label for="su-pw2">{t('auth.passwordAgain')}</label>
				<input id="su-pw2" type="password" bind:value={again} required autocomplete="new-password" />
			</div>
		</div>
		{#if error}<div class="err">{error}</div>{/if}
		<div class="form-actions">
			<button class="btn-primary btn-lg" type="submit" disabled={busy} aria-busy={busy}
				>{t('auth.setup.action')}</button
			>
		</div>
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
		background: var(--scene);
		color: var(--tx);
		overflow: auto;
	}
	.card {
		width: 100%;
		max-width: 520px;
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
		font-family: var(--serif);
		font-style: italic;
		font-size: var(--fs-body);
		color: var(--mut);
	}
	h1 {
		margin: 0;
		font-family: var(--serif);
		font-weight: 400;
		font-size: var(--fs-display);
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
