import {render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import * as api from 'api';
import * as client from 'client';
import React from 'react';

import HoursPage from './HoursPage';
import SettingsPage from './SettingsPage';
import WFHPage from './WFHPage';

jest.mock('api', () => ({
    ...jest.requireActual('api'),
    fetchHours: jest.fn(),
    fetchWFH: jest.fn(),
    requestWFH: jest.fn(),
    cancelWFH: jest.fn(),
    fetchSettings: jest.fn(),
}));
jest.mock('client', () => ({...jest.requireActual('client'), fetchHealth: jest.fn()}));
const mocked = api as jest.Mocked<typeof api>;
const mockedClient = client as jest.Mocked<typeof client>;
const TEAM = 'tttttttttttttttttttttttttt';

const hours = (over: Partial<api.Hours> = {}): api.Hours => ({
    range: 'week',
    label: 'Mon 23 Nov – Fri 27 Nov',
    days: [
        {day: '2026-11-23', label: 'Mon 23', seconds: 28800, modes: ['office'], open: false},
        {day: '2026-11-24', label: 'Tue 24', seconds: 0, modes: [], open: false},
        {day: '2026-11-25', label: 'Wed 25', seconds: 25200, modes: ['wfh'], open: false},
        {day: '2026-11-27', label: 'Fri 27', seconds: 21600, modes: ['office'], open: true},
    ],
    team_seconds: 75600,
    all_seconds: 82800,
    by_org: [{team: {id: 'A', name: 'Bittrix'}, seconds: 75600}, {team: {id: 'B', name: 'InkYank'}, seconds: 7200}],
    has_expected: true,
    expected_seconds: 86400,
    expected_until: 'Thu 26 Nov',
    worked_until_yesterday: 61200,
    per_day_hours: 8,
    ...over,
});

test('hours: totals, expected, per day with modes, by organization; range tabs', async () => {
    mocked.fetchHours.mockResolvedValue(hours());
    render(<HoursPage teamId={TEAM}/>);

    expect(await screen.findByText('21h 0m', {selector: '.att-big'})).toBeInTheDocument();
    expect(screen.getByText('23h 0m', {selector: '.att-big'})).toBeInTheDocument();
    expect(screen.getByText('Up to Thu 26 Nov: 17h 0m of 24h 0m expected')).toBeInTheDocument();
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '71');
    const days = within(screen.getByLabelText('Per day')).getAllByRole('listitem');
    expect(days).toHaveLength(4);
    expect(days[0]).toHaveTextContent('Mon 238h 0m🏢');
    expect(days[1]).toHaveTextContent('–');
    expect(days[2]).toHaveTextContent('🏠');
    expect(days[3]).toHaveTextContent('● now');
    expect(screen.getByText('InkYank')).toBeInTheDocument();
    expect(mocked.fetchHours).toHaveBeenLastCalledWith(TEAM, 'week');

    mocked.fetchHours.mockResolvedValue(hours({range: 'month', has_expected: false, by_org: []}));
    await userEvent.click(screen.getByRole('tab', {name: 'Month'}));
    expect(mocked.fetchHours).toHaveBeenLastCalledWith(TEAM, 'month');
    expect(await screen.findByRole('tab', {name: 'Month'})).toHaveAttribute('aria-selected', 'true');
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
});

const wfh = (requests: api.WFHItem[]): api.WFHState => ({team: {id: TEAM, name: 'Bittrix'}, today: '2026-11-27', pending: requests.filter((r) => r.status === 'pending').length, requests});
const item = (id: number, status: string, over: Partial<api.WFHItem> = {}): api.WFHItem => ({
    id, dates: `Mon ${id} Dec`, start: '2026-12-01', end: '2026-12-01', half_day: false, reason: 'Plumber', status, can_cancel: status === 'pending', ...over,
});

test('wfh: list with status, decided by, cancel with confirmation', async () => {
    mocked.fetchWFH.mockResolvedValue(wfh([item(1, 'pending'), item(2, 'approved', {decided_by: 'janarthanan'}), item(3, 'rejected', {decided_by: 'barath', note: 'client demo'})]));
    mocked.cancelWFH.mockResolvedValue({message: 'cancelled', wfh: wfh([item(1, 'cancelled', {can_cancel: false})])});
    render(<WFHPage teamId={TEAM}/>);

    const rows = await screen.findAllByRole('listitem');
    expect(rows[0]).toHaveTextContent('Pending');
    expect(rows[1]).toHaveTextContent('Approvedby @janarthanan');
    expect(rows[2]).toHaveTextContent('Rejectedby @barath: client demo');
    expect(within(rows[1]).queryByText('Cancel')).not.toBeInTheDocument();

    await userEvent.click(within(rows[0]).getByText('Cancel'));
    expect(mocked.cancelWFH).not.toHaveBeenCalled(); // asks first
    await userEvent.click(screen.getByText('Yes, cancel it'));
    expect(mocked.cancelWFH).toHaveBeenCalledWith(TEAM, 1);
    expect(await screen.findByText('Cancelled')).toBeInTheDocument();
});

test('wfh: request form sends; field errors from the bot shown; half day only for one day', async () => {
    mocked.fetchWFH.mockResolvedValue(wfh([]));
    mocked.requestWFH.mockRejectedValueOnce(new api.FormError('x', {start_date: 'You already have a pending or approved request covering these dates.'}));
    mocked.requestWFH.mockResolvedValueOnce(wfh([item(4, 'pending')]));
    render(<WFHPage teamId={TEAM}/>);

    expect(await screen.findByText('No requests yet.')).toBeInTheDocument();
    await userEvent.click(screen.getByText('+ Request work from home'));
    const form = within(screen.getByRole('form', {name: 'Request work from home'}));
    const [from, to] = form.getAllByDisplayValue('');
    await userEvent.type(from, '2026-12-01');
    await userEvent.type(form.getByPlaceholderText(/Plumber/), 'Plumber visit');
    await userEvent.click(form.getByRole('checkbox'));
    await userEvent.click(form.getByText('Send request'));
    expect(mocked.requestWFH).toHaveBeenCalledWith(TEAM, {start_date: '2026-12-01', end_date: '', half_day: true, reason: 'Plumber visit'});
    expect(await screen.findByText(/already have a pending or approved request/)).toBeInTheDocument();

    await userEvent.type(to, '2026-12-03');
    expect(form.getByRole('checkbox')).toBeDisabled(); // half day is for one day
    await userEvent.click(form.getByText('Send request'));
    expect(mocked.requestWFH).toHaveBeenLastCalledWith(TEAM, {start_date: '2026-12-01', end_date: '2026-12-03', half_day: false, reason: 'Plumber visit'});
    expect(await screen.findByText(/Request sent to the admins/)).toBeInTheDocument();
    expect(screen.getByText('Pending')).toBeInTheDocument();
});

test('settings: GitHub link, my settings, connection checks', async () => {
    mocked.fetchSettings.mockResolvedValue({team: {id: TEAM, name: 'Bittrix'}, is_admin: false, github_set_up: true, github_org: 'bittrixtechnologies', github_login: 'vairabarath', expected_hours: 8, tracked: true, reminders_on: false});
    mockedClient.fetchHealth.mockResolvedValue({ok: true, version: '0.7.0'});
    render(<SettingsPage teamId={TEAM}/>);

    expect(await screen.findByRole('link', {name: '✅ vairabarath'})).toHaveAttribute('href', 'https://github.com/vairabarath');
    expect(screen.getByText('bittrixtechnologies')).toBeInTheDocument();
    expect(screen.getByText('8h (all organizations)')).toBeInTheDocument();
    expect(screen.getByText('Off')).toBeInTheDocument();
    const checks = within(screen.getByLabelText('Connection'));
    expect(await checks.findByText('Plugin 0.7.0')).toBeInTheDocument();
    expect(checks.getByText('Attendance service')).toBeInTheDocument();
    expect(checks.getByText('GitHub project for Bittrix')).toBeInTheDocument();
});

test('settings: not linked; service unreachable', async () => {
    mocked.fetchSettings.mockRejectedValueOnce(new Error('Couldn’t reach the attendance service.'));
    mockedClient.fetchHealth.mockResolvedValue({ok: false, version: '0.7.0', error: 'the shared secret is missing'});
    render(<SettingsPage teamId={TEAM}/>);
    expect(await screen.findByText('Attendance service: Couldn’t reach the attendance service.')).toBeInTheDocument();
    expect(await screen.findByText('Plugin: the shared secret is missing')).toBeInTheDocument();

    mocked.fetchSettings.mockResolvedValue({team: {id: TEAM, name: 'Bittrix'}, is_admin: false, github_set_up: true, github_org: 'o', expected_hours: 8, tracked: true, reminders_on: true});
    mockedClient.fetchHealth.mockResolvedValue({ok: true, version: '0.7.0'});
    await userEvent.click(screen.getByText('Check again'));
    expect(await screen.findByText(/Not linked yet/)).toBeInTheDocument();
});
