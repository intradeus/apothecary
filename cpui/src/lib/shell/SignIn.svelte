<script lang="ts">
	// The sign-in page: the one thing a browser without a session sees. A
	// handle or an email, and a password. The engine throttles guessing; the
	// page only relays what it says.
	import { session } from '$lib/session.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import Logo from '$lib/ui/Logo.svelte';

	let login = $state('');
	let password = $state('');
	let busy = $state(false);
	let error = $state('');

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			await session.login(login.trim(), password);
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>{t('auth.signIn.docTitle')}</title></svelte:head>

<div class="gate">
	<form class="card c" onsubmit={submit}>
		<div class="brand">
			<Logo size={38} />
			<div>
				<div class="name">Apothecary</div>
				<div class="tagline">{t('shell.sidebar.tagline')}</div>
			</div>
		</div>
		<h1>{t('auth.signIn.title')}</h1>
		<p class="mut">{t('auth.signIn.lede')}</p>
		<div class="field">
			<label for="si-login">{t('auth.signIn.login')}</label>
			<input id="si-login" bind:value={login} required autocomplete="username" />
		</div>
		<div class="field">
			<label for="si-pw">{t('auth.password')}</label>
			<input id="si-pw" type="password" bind:value={password} required autocomplete="current-password" />
		</div>
		{#if error}<div class="err">{error}</div>{/if}
		<div class="form-actions">
			<button class="btn-primary btn-lg" type="submit" disabled={busy} aria-busy={busy}
				>{t('auth.signIn.action')}</button
			>
		</div>
		<p class="help">{t('auth.signIn.lockedOut')}</p>
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
		max-width: 400px;
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
	.help {
		font-size: var(--fs-meta);
		color: var(--fnt);
	}
</style>
