// The guild segment is gone (TODO 25: no organisation level). A bookmark
// from /g/<guild>/… lands on the same page without it; /g/<guild> itself
// on the desk.
import { redirect } from '@sveltejs/kit';

export const load = ({ params, url }) => {
	const rest = params.rest.split('/').slice(1).join('/');
	redirect(308, `/${rest}${url.search}${url.hash}`);
};
