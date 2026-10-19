<!-- All-guilds desk: only reachable with more than one guild (see +page.ts). -->
<script lang="ts">
	import { to } from '$lib/guild';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
</script>

<div class="page">
	<EmptyState title={t('page.allGuilds.title')} text={t('page.allGuilds.text')}>
		<a class="btn btn-primary" href={to.desk()}>{t('page.allGuilds.open')}</a>
	</EmptyState>
</div>
