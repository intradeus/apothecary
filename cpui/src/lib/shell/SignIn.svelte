<script lang="ts">
	// The sign-in page: the one thing a browser without a session sees. A
	// handle or an email, and a password. The engine throttles guessing; the
	// page only relays what it says.
	import { errorMessage } from '$lib/core/errors';
	import { session } from '$lib/app/session.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import AuthGate from './AuthGate.svelte';

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
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>{t('auth.signIn.docTitle')}</title></svelte:head>

<AuthGate
	title={t('auth.signIn.title')}
	lede={t('auth.signIn.lede')}
	action={t('auth.signIn.action')}
	{busy}
	{error}
	onsubmit={submit}
>
	<div class="field">
		<label for="si-login">{t('auth.signIn.login')}</label>
		<input id="si-login" bind:value={login} required autocomplete="username" />
	</div>
	<div class="field">
		<label for="si-pw">{t('auth.password')}</label>
		<input
			id="si-pw"
			type="password"
			bind:value={password}
			required
			autocomplete="current-password"
		/>
	</div>

	{#snippet foot()}
		<p class="help">{t('auth.signIn.lockedOut')}</p>
	{/snippet}
</AuthGate>

<style>
	.help {
		margin: 0;
		font-size: var(--fs-meta);
		color: var(--text-faint);
	}
</style>
