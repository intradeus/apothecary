<script lang="ts">
	// First boot: no account exists, so whoever reaches the page claims the
	// instance and becomes its admin.
	import { errorMessage } from '$lib/core/errors';
	import { session } from '$lib/app/session.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import AuthGate from './AuthGate.svelte';

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
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>{t('auth.setup.docTitle')}</title></svelte:head>

<AuthGate
	title={t('auth.setup.title')}
	lede={t('auth.setup.lede')}
	action={t('auth.setup.action')}
	width={520}
	{busy}
	{error}
	onsubmit={submit}
>
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
				pattern="[a-z0-9][a-z0-9\-]{'{'}1,31{'}'}"
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
</AuthGate>

<style>
	.help {
		font-size: var(--fs-meta);
		color: var(--text-faint);
	}
</style>
