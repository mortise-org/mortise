import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { App, AppSpec } from '$lib/types';
import VariablesTab from './VariablesTab.svelte';

type Row = { name: string; value: string; source?: string; bindingRef?: string; bindingKey?: string; secretRef?: string };

const managedRows: Row[] = [
	{ name: 'AUTO', value: 'automatic', source: 'binding' },
	{ name: 'DECL', value: 'projected', source: 'binding', bindingRef: 'database', bindingKey: 'url' },
	{ name: 'SECRET', value: 'resolved', source: 'user', secretRef: 'app-secrets' },
	{ name: 'GENERATED', value: 'generated', source: 'generated' },
	{ name: 'SHARED', value: 'shared', source: 'shared' }
];

const { getEnv, setEnv, updateApp, currentEnv } = vi.hoisted(() => ({
	getEnv: vi.fn(),
	setEnv: vi.fn(),
	updateApp: vi.fn(),
	currentEnv: vi.fn(() => 'production')
}));

vi.mock('$lib/store.svelte', () => ({ store: { currentEnv } }));
vi.mock('$lib/api', () => ({
	api: {
		getEnv,
		setEnv,
		updateApp,
		getSharedVars: vi.fn().mockResolvedValue([]),
		setSharedVars: vi.fn(),
		listApps: vi.fn().mockResolvedValue([]),
		redeploy: vi.fn()
	}
}));

function makeApp(): App {
	return {
		metadata: { name: 'consumer', generation: 4 },
		spec: {
			source: { type: 'image', image: 'nginx:1.27' },
			environments: [{
				name: 'production',
				env: [
					{ name: 'DECL', valueFrom: { fromBinding: { ref: 'database', key: 'url' } } },
					{ name: 'SECRET', valueFrom: { secretRef: 'app-secrets' } }
				]
			}]
		}
	};
}

function runtimePanel(): HTMLElement {
	const heading = screen.getByText('Runtime - production');
	const panel = heading.closest('.rounded-lg');
	if (!(panel instanceof HTMLElement)) throw new Error('runtime panel not found');
	return panel;
}

describe('VariablesTab runtime ownership', () => {
	let userRows: Row[];

	beforeEach(() => {
		vi.clearAllMocks();
		currentEnv.mockReturnValue('production');
		userRows = [{ name: 'USER', value: 'before', source: 'user' }];
		getEnv.mockImplementation(async () => [...userRows.map(row => ({ ...row })), ...managedRows.map(row => ({ ...row }))]);
		setEnv.mockImplementation(async (_project: string, _app: string, _env: string, vars: Record<string, string>) => {
			userRows = Object.entries(vars).map(([name, value]) => ({ name, value, source: 'user' }));
		});
		updateApp.mockImplementation(async (_project: string, _name: string, spec: AppSpec) => ({ ...makeApp(), spec }));
	});

	it('submits only user literals and keeps managed rows through table and raw saves', async () => {
		render(VariablesTab, { props: { project: 'demo', app: makeApp() } });
		await screen.findByText('AUTO');
		const panel = runtimePanel();

		const userRow = within(panel).getByText('USER').closest('.group') as HTMLElement;
		await fireEvent.click(within(userRow).getAllByRole('button')[0]);
		const userInput = within(userRow).getByRole('textbox') as HTMLInputElement;
		await fireEvent.input(userInput, { target: { value: 'after' } });
		await fireEvent.click(within(panel).getByRole('button', { name: 'Save 1 change' }));
		await waitFor(() => expect(setEnv).toHaveBeenCalledWith('demo', 'consumer', 'production', { USER: 'after' }));
		for (const row of managedRows) expect(within(panel).getByText(row.name)).toBeInTheDocument();

		await fireEvent.click(within(panel).getByRole('button', { name: 'Raw' }));
		const raw = within(panel).getByRole('textbox') as HTMLTextAreaElement;
		expect(raw.value).toBe('USER=after');
		await fireEvent.input(raw, { target: { value: 'RAW=one' } });
		await fireEvent.click(within(panel).getByRole('button', { name: 'Save' }));
		await waitFor(() => expect(setEnv).toHaveBeenLastCalledWith('demo', 'consumer', 'production', { RAW: 'one' }));
		for (const row of managedRows) expect(within(panel).getByText(row.name)).toBeInTheDocument();
	});

	it('deletes declarative rows through the App spec and never through the env API', async () => {
		render(VariablesTab, { props: { project: 'demo', app: makeApp() } });
		await screen.findByText('DECL');
		const panel = runtimePanel();
		const declarativeRow = within(panel).getByText('DECL').closest('.group') as HTMLElement;
		await fireEvent.click(within(declarativeRow).getAllByRole('button').at(-1) as HTMLButtonElement);

		await waitFor(() => expect(updateApp).toHaveBeenCalledOnce());
		const payload = updateApp.mock.calls[0][2] as AppSpec;
		const envVars = payload.environments?.[0].env ?? [];
		expect(envVars.some(row => row.name === 'DECL')).toBe(false);
		expect(envVars.some(row => row.name === 'SECRET')).toBe(true);
		expect(setEnv).not.toHaveBeenCalled();

		const secretRow = within(panel).getByText('SECRET').closest('.group') as HTMLElement;
		await fireEvent.click(within(secretRow).getAllByRole('button').at(-1) as HTMLButtonElement);
		await waitFor(() => expect(updateApp).toHaveBeenCalledTimes(2));
		const secondPayload = updateApp.mock.calls[1][2] as AppSpec;
		expect(secondPayload.environments?.[0].env).toEqual([]);

		const automaticRow = within(panel).getByText('AUTO').closest('.group') as HTMLElement;
		expect(within(automaticRow).queryAllByRole('button')).toHaveLength(2);
	});
});

describe('VariablesTab reveal state', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		currentEnv.mockReturnValue('production');
		let rows: Row[] = [{ name: 'USER', value: 'before', source: 'user' }];
		getEnv.mockImplementation(async () => rows.map(row => ({ ...row })));
		setEnv.mockImplementation(async (_project: string, _app: string, _env: string, vars: Record<string, string>) => {
			rows = Object.entries(vars).map(([name, value]) => ({ name, value, source: 'user' }));
		});
		updateApp.mockImplementation(async (_project: string, _name: string, spec: AppSpec) => ({ ...makeApp(), spec }));
	});

	// A revealed value must stay revealed across the refetch that follows a
	// save; the E2E "manual saves preserve ... projections" lost its input to
	// that refetch (CAI-165 E2E measurement, run 33313012705).
	it('keeps a revealed row revealed after a save refetches the section', async () => {
		render(VariablesTab, { props: { project: 'demo', app: makeApp() } });
		const panel = runtimePanel();
		await waitFor(() => expect(within(panel).getByText('USER')).toBeTruthy());
		const userRow = within(panel).getByText('USER').closest('div.group') as HTMLElement;
		await fireEvent.click(within(userRow).getByTitle('Reveal'));
		expect(within(userRow).getByRole('textbox')).toBeTruthy();

		const header = panel.querySelector('.flex.items-center.justify-between') as HTMLElement;
		const iconButtons = Array.from(header.querySelectorAll('button')).filter(b => b.querySelector('svg'));
		await fireEvent.click(iconButtons[iconButtons.length - 1]);
		await fireEvent.input(within(panel).getByPlaceholderText('VARIABLE_NAME'), { target: { value: 'NEW' } });
		await fireEvent.input(within(panel).getByPlaceholderText('value or binding ref'), { target: { value: 'one' } });
		await fireEvent.click(within(panel).getByRole('button', { name: 'Add' }));
		await waitFor(() => expect(setEnv).toHaveBeenCalled());
		await waitFor(() => expect(getEnv.mock.calls.length).toBeGreaterThan(1));

		const again = within(panel).getByText('USER').closest('div.group') as HTMLElement;
		await waitFor(() => expect(within(again).getByRole('textbox')).toBeTruthy());
	});
});
