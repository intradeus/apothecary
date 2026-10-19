<!-- Inbox (DESIGN 7.5). ?ping=<id> preselects a ping; ?tab=answered opens the history. -->
<script lang="ts">
	import { page } from '$app/state';
	import { t } from '$lib/i18n/index.svelte';
	import Inbox from '$lib/inbox/Inbox.svelte';
</script>

<svelte:head><title>{t('page.inbox.title')}</title></svelte:head>

<Inbox
	initial={page.url.searchParams.get('ping') ?? ''}
	initialTab={page.url.searchParams.get('tab') === 'answered' ? 'answered' : 'pending'}
/>
