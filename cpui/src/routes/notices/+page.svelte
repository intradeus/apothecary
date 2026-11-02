<!-- System notices: setup problems to fix, kept until dismissed. -->
<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	import Notices from '$lib/notices/Notices.svelte';
</script>

<svelte:head><title>{t('page.notices.title')}</title></svelte:head>

<Notices />
