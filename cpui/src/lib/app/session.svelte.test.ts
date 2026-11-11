import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { client } from '$lib/api/generated/client.gen';
import type { Me } from '$lib/domain';
import { session } from './session.svelte';

vi.mock('$lib/api', () => ({
	api: {
		auth: {
			status: vi.fn(),
			me: vi.fn(),
			setup: vi.fn(),
			login: vi.fn(),
			logout: vi.fn()
		}
	}
}));

const me = (over: Partial<Me['user']> = {}) =>
	({ user: { id: 'u1', handle: 'lu', name: 'Lu', admin: false, ...over } }) as Me;

beforeEach(() => {
	vi.resetAllMocks();
	session.ready = false;
	session.setupRequired = false;
	session.methods = [];
	session.me = null;
	session.error = '';
});

describe('session.load', () => {
	it('signs a browser with a session in', async () => {
		vi.mocked(api.auth.status).mockResolvedValue({
			setupRequired: false,
			methods: ['password'],
			version: 'v1'
		});
		vi.mocked(api.auth.me).mockResolvedValue(me({ name: 'Lucas' }));

		await session.load();

		expect(session.ready).toBe(true);
		expect(session.methods).toEqual(['password']);
		expect(session.user?.name).toBe('Lucas');
	});

	it('treats a failed me as signed out, not as an outage', async () => {
		vi.mocked(api.auth.status).mockResolvedValue({
			setupRequired: false,
			methods: ['password'],
			version: 'v1'
		});
		vi.mocked(api.auth.me).mockRejectedValue(new Error('401 Unauthorized'));

		await session.load();

		expect(session.ready).toBe(true);
		expect(session.me).toBeNull();
		expect(session.error).toBe('');
	});

	it('does not ask who is signed in when the instance has no account yet', async () => {
		vi.mocked(api.auth.status).mockResolvedValue({
			setupRequired: true,
			methods: [],
			version: 'v1'
		});

		await session.load();

		expect(session.setupRequired).toBe(true);
		expect(session.ready).toBe(true);
		expect(api.auth.me).not.toHaveBeenCalled();
	});

	it('stays not ready and says why when the engine is down', async () => {
		vi.mocked(api.auth.status).mockRejectedValue(new Error('engine unreachable'));

		await session.load();

		expect(session.ready).toBe(false);
		expect(session.error).toBe('engine unreachable');
	});

	it('clears the outage on a retry that works', async () => {
		vi.mocked(api.auth.status).mockRejectedValueOnce(new Error('engine unreachable'));
		await session.load();

		vi.mocked(api.auth.status).mockResolvedValue({
			setupRequired: false,
			methods: [],
			version: 'v1'
		});
		vi.mocked(api.auth.me).mockResolvedValue(me());
		await session.load();

		expect(session.error).toBe('');
		expect(session.ready).toBe(true);
	});
});

describe('session.user and admin', () => {
	it('derives both from me', () => {
		expect(session.user).toBeNull();
		expect(session.admin).toBe(false);

		session.me = me({ admin: true, handle: 'root' });
		expect(session.user?.handle).toBe('root');
		expect(session.admin).toBe(true);
	});
});

describe('signing in and out', () => {
	it('signs in with what the page sends', async () => {
		vi.mocked(api.auth.login).mockResolvedValue(me());
		await session.login('lu', 'hunter22');
		expect(api.auth.login).toHaveBeenCalledWith('lu', 'hunter22');
		expect(session.user?.handle).toBe('lu');
	});

	it('lets a refused sign-in through to the page', async () => {
		vi.mocked(api.auth.login).mockRejectedValue(new Error('invalid credentials'));
		await expect(session.login('lu', 'nope')).rejects.toThrow('invalid credentials');
		expect(session.me).toBeNull();
	});

	it('ends the setup requirement once the first account exists', async () => {
		session.setupRequired = true;
		vi.mocked(api.auth.setup).mockResolvedValue(me({ admin: true }));
		const account = { handle: 'lu', email: 'l@test', name: 'Lu', password: 'hunter22' };

		await session.setup(account);

		expect(api.auth.setup).toHaveBeenCalledWith(account);
		expect(session.setupRequired).toBe(false);
		expect(session.admin).toBe(true);
	});

	it('keeps the setup page up when the account is refused', async () => {
		session.setupRequired = true;
		vi.mocked(api.auth.setup).mockRejectedValue(new Error('handle taken'));
		await expect(
			session.setup({ handle: 'lu', email: 'l@test', name: '', password: 'hunter22' })
		).rejects.toThrow('handle taken');
		expect(session.setupRequired).toBe(true);
	});

	it('signs out', async () => {
		session.me = me();
		vi.mocked(api.auth.logout).mockResolvedValue(undefined);
		await session.logout();
		expect(session.me).toBeNull();
	});

	it('signs this browser out even when the engine cannot be told', async () => {
		session.me = me();
		vi.mocked(api.auth.logout).mockRejectedValue(new Error('engine down'));
		await expect(session.logout()).rejects.toThrow('engine down');
		expect(session.me).toBeNull();
	});

	it('picks up a profile edit', async () => {
		session.me = me({ name: 'Old' });
		vi.mocked(api.auth.me).mockResolvedValue(me({ name: 'New' }));
		await session.refresh();
		expect(session.user?.name).toBe('New');
	});
});

describe('a 401 from any later call', () => {
	const answer = (status: number) =>
		client.get({
			baseUrl: 'http://engine.test',
			url: '/api/anything',
			fetch: async () => new Response('{}', { status })
		});

	it('drops the shell back to sign-in, through the real transport', async () => {
		session.ready = true;
		session.me = me();

		await answer(401);

		expect(session.user).toBeNull();
		expect(session.ready).toBe(true);
		expect(session.error).toBe('');
	});

	it('leaves the session alone on any other failure', async () => {
		session.me = me();
		await answer(403);
		await answer(500);
		expect(session.user).not.toBeNull();
	});

	it('is harmless while signed out', () => {
		session.setupRequired = true;
		session.expire();
		expect(session.me).toBeNull();
		expect(session.setupRequired).toBe(true);
	});
});
