import { error } from '@sveltejs/kit';
import { INTEGRATION_PORTS, type IntegrationPort } from '$lib/core/urls';

export const load = ({ params }: { params: { port: string } }) => {
	const port = INTEGRATION_PORTS.find((p) => p === params.port);
	if (!port) error(404, 'No such kind of integration');
	return { port: port as IntegrationPort };
};
