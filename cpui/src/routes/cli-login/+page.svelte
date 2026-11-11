<!-- Approving a terminal sign-in: the link `apothecary login` prints. -->
<script lang="ts">
	import { page } from '$app/state';
	import { t } from '$lib/i18n/index.svelte';
	import CliLogin from '$lib/shell/CliLogin.svelte';
</script>

<svelte:head><title>{t('auth.cli.docTitle')}</title></svelte:head>

<CliLogin code={page.url.searchParams.get('code') ?? ''} />
