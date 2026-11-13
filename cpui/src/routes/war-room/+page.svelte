<script lang="ts">
	// The war room: every sandbox in flight, across the person's workshops.
	import { t } from '$lib/i18n/index.svelte';
	import WarRoom from '$lib/swarm/WarRoom.svelte';
</script>

<svelte:head><title>{t('page.warRoom.title')}</title></svelte:head>

<WarRoom />
