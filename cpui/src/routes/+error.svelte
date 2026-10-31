<script lang="ts">
	import { page } from '$app/state';
	import { to } from '$lib/urls';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
</script>

<div class="page c">
	<EmptyState
		title={page.status === 404 ? t('page.error.notFound') : t('page.error.spilled')}
		text={page.error?.message ?? t('page.error.unexpected')}
	>
		<a class="btn btn-primary" href={to.desk()}>{t('page.error.backToDesk')}</a>
	</EmptyState>
</div>
